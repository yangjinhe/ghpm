// Package gh is a small GitHub REST client with ETag caching and rate limit
// awareness, tailored to release lookups.
package gh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ghpm/internal/config"
)

// UserAgent identifies ghpm to GitHub.
var UserAgent = "ghpm/dev"

// Asset is one downloadable release file.
type Asset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	URL         string `json:"browser_download_url"`
	APIURL      string `json:"url"`
	Digest      string `json:"digest"`
	ContentType string `json:"content_type"`
}

// DownloadSource returns the URL and headers to use for an asset. With a
// token, the API asset endpoint is used so private repositories work; the
// authorization header is dropped automatically by net/http on cross-host
// redirects to the signed storage URL.
func (c *Client) DownloadSource(a Asset) (string, map[string]string) {
	if c.token != "" && a.APIURL != "" {
		return a.APIURL, map[string]string{
			"Accept":        "application/octet-stream",
			"Authorization": "Bearer " + c.token,
		}
	}
	return a.URL, nil
}

// Release is the subset of the release API ghpm cares about.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	Assets      []Asset   `json:"assets"`
}

// AssetNames returns just the names, in API order.
func (r *Release) AssetNames() []string {
	out := make([]string, len(r.Assets))
	for i, a := range r.Assets {
		out[i] = a.Name
	}
	return out
}

// FindAsset returns the asset with this exact name.
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}

// RateLimit is the latest known API quota.
type RateLimit struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

// Client talks to the GitHub API.
type Client struct {
	http        *http.Client
	apiBase     string
	token       string
	cacheDir    string
	ttl         time.Duration
	BypassCache bool

	mu    sync.Mutex
	limit RateLimit
}

// New builds a client from configuration. cacheDir may be empty to disable
// metadata caching.
func New(cfg config.GitHub, proxy, cacheDir string, ttl time.Duration) (*Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 8
	if strings.TrimSpace(proxy) != "" {
		u, err := url.Parse(strings.TrimSpace(proxy))
		if err != nil {
			return nil, fmt.Errorf("invalid proxy %q: %w", proxy, err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	token := ""
	if cfg.TokenEnv != "" {
		token = os.Getenv(cfg.TokenEnv)
	}
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	if err := checkAPIBase(cfg.APIBase); err != nil {
		return nil, err
	}
	if cacheDir != "" {
		cacheDir = filepath.Join(cacheDir, "releases")
	}
	return &Client{
		http: &http.Client{
			Transport: tr,
			Timeout:   45 * time.Second,
		},
		apiBase:  strings.TrimRight(cfg.APIBase, "/"),
		token:    token,
		cacheDir: cacheDir,
		ttl:      ttl,
	}, nil
}

// HasToken reports whether requests are authenticated.
func (c *Client) HasToken() bool { return c.token != "" }

// RateLimit returns the most recently observed quota.
func (c *Client) RateLimit() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limit
}

// ResolveRelease fetches the release selected by channel:
// "" / "latest" -> latest stable, "prerelease" -> newest non-draft,
// "tag:v1.2.3" -> that exact tag.
func (c *Client) ResolveRelease(ctx context.Context, repo, channel string) (*Release, error) {
	channel = strings.TrimSpace(channel)
	switch {
	case channel == "", channel == "latest":
		var rel Release
		if err := c.getJSON(ctx, c.repoURL(repo, "/releases/latest"), &rel); err != nil {
			return nil, err
		}
		return &rel, nil
	case channel == "prerelease":
		var rels []Release
		if err := c.getJSON(ctx, c.repoURL(repo, "/releases?per_page=20"), &rels); err != nil {
			return nil, err
		}
		for i := range rels {
			if !rels[i].Draft {
				return &rels[i], nil
			}
		}
		return nil, fmt.Errorf("repository %s has no published releases", repo)
	case strings.HasPrefix(channel, "tag:"):
		tag := strings.TrimSpace(strings.TrimPrefix(channel, "tag:"))
		if tag == "" {
			return nil, errors.New("empty tag in channel")
		}
		var rel Release
		if err := c.getJSON(ctx, c.repoURL(repo, "/releases/tags/"+url.PathEscape(tag)), &rel); err != nil {
			return nil, err
		}
		return &rel, nil
	}
	return nil, fmt.Errorf("unknown channel %q", channel)
}

// ReleaseByTag fetches one release by tag.
func (c *Client) ReleaseByTag(ctx context.Context, repo, tag string) (*Release, error) {
	var rel Release
	if err := c.getJSON(ctx, c.repoURL(repo, "/releases/tags/"+url.PathEscape(tag)), &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (c *Client) repoURL(repo, suffix string) string {
	return c.apiBase + "/repos/" + repo + suffix
}

type cacheEntry struct {
	ETag string          `json:"etag"`
	At   time.Time       `json:"at"`
	Body json.RawMessage `json:"body"`
}

func (c *Client) cachePath(u string) string {
	if c.cacheDir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(u))
	return filepath.Join(c.cacheDir, hex.EncodeToString(sum[:16])+".json")
}

func (c *Client) readCache(u string) (*cacheEntry, error) {
	p := c.cachePath(u)
	if p == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (c *Client) writeCache(u string, e *cacheEntry) {
	p := c.cachePath(u)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err == nil {
		if tmp.Close() == nil {
			_ = os.Rename(name, p)
			return
		}
	}
	tmp.Close()
}

// getJSON performs a cached GET. Fresh cache entries are used without a
// request; stale ones are revalidated with If-None-Match (304s are free).
func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	cached, _ := c.readCache(u)
	if cached != nil && !c.BypassCache && c.ttl > 0 && time.Since(cached.At) < c.ttl {
		if err := json.Unmarshal(cached.Body, out); err == nil {
			return nil
		}
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 800 * time.Millisecond):
			}
		}
		body, etag, status, err := c.do(ctx, u, cached)
		if err != nil {
			lastErr = err
			continue
		}
		switch status {
		case http.StatusOK:
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("decode %s: %w", u, err)
			}
			c.writeCache(u, &cacheEntry{ETag: etag, At: time.Now(), Body: body})
			return nil
		case http.StatusNotModified:
			if cached != nil {
				if err := json.Unmarshal(cached.Body, out); err != nil {
					return fmt.Errorf("decode cached %s: %w", u, err)
				}
				cached.At = time.Now()
				c.writeCache(u, cached)
				return nil
			}
			return fmt.Errorf("unexpected 304 for %s", u)
		case http.StatusNotFound:
			hint := "check owner/repo and that the release exists"
			if c.token == "" {
				hint += "; private repositories need GITHUB_TOKEN"
			}
			return fmt.Errorf("not found: %s (%s)", u, hint)
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			return c.limitError(u)
		case 500, 502, 503, 504:
			lastErr = fmt.Errorf("github returned %s for %s", http.StatusText(status), u)
			continue
		default:
			return fmt.Errorf("github returned %d for %s: %s", status, u, snippet(body))
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("request failed: %s", u)
	}
	return lastErr
}

func (c *Client) do(ctx context.Context, u string, cached *cacheEntry) ([]byte, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", UserAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if cached != nil && cached.ETag != "" {
		req.Header.Set("If-None-Match", cached.ETag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	c.recordLimit(resp.Header)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", 0, err
	}
	return body, resp.Header.Get("ETag"), resp.StatusCode, nil
}

func (c *Client) recordLimit(h http.Header) {
	limit, _ := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, _ := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	reset, _ := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if limit == 0 && remaining == 0 && reset == 0 {
		return
	}
	c.mu.Lock()
	c.limit = RateLimit{Limit: limit, Remaining: remaining, Reset: time.Unix(reset, 0)}
	c.mu.Unlock()
}

func (c *Client) limitError(u string) error {
	rl := c.RateLimit()
	hint := "set GITHUB_TOKEN to raise the limit to 5000/hour"
	if c.token != "" {
		hint = "token may be invalid or lack access"
	}
	if rl.Remaining == 0 && !rl.Reset.IsZero() {
		return fmt.Errorf("github rate limit exhausted (reset at %s); %s (%s)",
			rl.Reset.Local().Format("15:04:05"), hint, u)
	}
	return fmt.Errorf("github refused the request (403/429); %s (%s)", hint, u)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// PruneCache deletes release cache entries older than maxAge.
func PruneCache(cacheDir string, maxAge time.Duration) error {
	dir := filepath.Join(cacheDir, "releases")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// checkAPIBase keeps tokens off plain HTTP endpoints other than loopback.
func checkAPIBase(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid api_base %q: %w", raw, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return nil
		}
	}
	return fmt.Errorf("api_base must use https (got %q)", raw)
}
