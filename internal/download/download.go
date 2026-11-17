// Package download fetches release assets with resume, retries, mirror
// prefixes, progress reporting and checksum verification.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Progress is a point-in-time download measurement.
type Progress struct {
	Downloaded  int64
	Total       int64
	BytesPerSec float64
	Done        bool
}

// Percent returns 0..100, or -1 when the total is unknown.
func (p Progress) Percent() float64 {
	if p.Total <= 0 {
		return -1
	}
	return float64(p.Downloaded) / float64(p.Total) * 100
}

// Options configures one download.
type Options struct {
	URL       string
	Dest      string
	Prefix    string // mirror prefix, joined with the URL or {url} substitution
	Proxy     string
	UserAgent string
	Progress  func(Progress)
	// ExpectedSHA256, when non-empty, is verified; a mismatch removes Dest.
	ExpectedSHA256 string
	// Headers are added to the request (e.g. Authorization for private
	// GitHub assets).
	Headers map[string]string
	// MaxRetries is the number of retries per URL (default 2).
	MaxRetries int
}

// Fetcher performs downloads.
type Fetcher struct {
	client *http.Client
}

// New builds a Fetcher honouring a proxy setting.
func New(proxy string) (*Fetcher, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 20 * time.Second
	tr.ResponseHeaderTimeout = 60 * time.Second
	tr.MaxIdleConnsPerHost = 4
	if strings.TrimSpace(proxy) != "" {
		u, err := url.Parse(strings.TrimSpace(proxy))
		if err != nil {
			return nil, fmt.Errorf("invalid proxy %q: %w", proxy, err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return requireSecureURL(req.URL.String())
		},
	}
	return &Fetcher{client: client}, nil
}

// requireSecureURL rejects plain HTTP except for loopback hosts (used by
// tests and local mirrors).
func requireSecureURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("refusing insecure download URL: %s", raw)
	}
	return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
}

// MirrorURL applies a download prefix to a GitHub URL.
func MirrorURL(prefix, raw string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return raw
	}
	if strings.Contains(prefix, "{url}") {
		return strings.ReplaceAll(prefix, "{url}", raw)
	}
	return strings.TrimRight(prefix, "/") + "/" + raw
}

// Fetch downloads opts.URL to opts.Dest. The file is written to Dest+".part"
// and renamed only after a complete, verified transfer.
func (f *Fetcher) Fetch(ctx context.Context, opts Options) (string, error) {
	if opts.Dest == "" {
		return "", errors.New("download: empty destination")
	}
	if err := os.MkdirAll(filepath.Dir(opts.Dest), 0o755); err != nil {
		return "", err
	}
	if info, err := os.Stat(opts.Dest); err == nil && info.Mode().IsRegular() {
		if opts.ExpectedSHA256 == "" {
			sum, err := HashFile(opts.Dest)
			if err == nil {
				return sum, nil
			}
		} else {
			sum, err := HashFile(opts.Dest)
			if err == nil && strings.EqualFold(sum, normalizeSHA(opts.ExpectedSHA256)) {
				opts.report(Progress{Downloaded: info.Size(), Total: info.Size(), Done: true})
				return sum, nil
			}
			_ = os.Remove(opts.Dest)
		}
	}

	urls := []string{MirrorURL(opts.Prefix, opts.URL)}
	if urls[0] != opts.URL {
		urls = append(urls, opts.URL)
	}
	retries := opts.MaxRetries
	if retries <= 0 {
		retries = 2
	}
	var lastErr error
	for _, u := range urls {
		if err := requireSecureURL(u); err != nil {
			lastErr = err
			continue
		}
		for attempt := 0; attempt <= retries; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(time.Duration(attempt) * time.Second):
				}
			}
			sum, err := f.fetchOne(ctx, u, opts)
			if err == nil {
				return sum, nil
			}
			lastErr = err
			if errors.Is(err, errChecksumMismatch) || errors.Is(err, context.Canceled) {
				return "", err
			}
			var permanent *permanentError
			if errors.As(err, &permanent) {
				break // try the next candidate URL, not the same one again
			}
		}
	}
	return "", lastErr
}

var errChecksumMismatch = errors.New("checksum mismatch")

// permanentError marks a failure that retrying the same URL will not fix,
// such as 401/404.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func (f *Fetcher) fetchOne(ctx context.Context, u string, opts Options) (string, error) {
	part := opts.Dest + ".part"
	var offset int64
	if info, err := os.Stat(part); err == nil {
		offset = info.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if opts.UserAgent != "" {
		req.Header.Set("User-Agent", opts.UserAgent)
	}
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", u, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		// resume as requested
	case http.StatusOK:
		offset = 0 // server ignored Range, restart
	case http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(part)
		return "", fmt.Errorf("resume failed for %s; retrying", u)
	default:
		err := fmt.Errorf("download %s: HTTP %d", u, resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
			resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return "", &permanentError{err: err}
		}
		return "", err
	}

	total := resp.ContentLength
	if total > 0 {
		total += offset
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	fh, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return "", err
	}
	if offset > 0 {
		if _, err := fh.Seek(offset, io.SeekStart); err != nil {
			fh.Close()
			return "", err
		}
	}

	hasher := sha256.New()
	if offset > 0 {
		// Hash the bytes already on disk before appending.
		if err := hashInto(hasher, part, offset); err != nil {
			fh.Close()
			return "", err
		}
	}

	pw := &progressWriter{
		w:        io.MultiWriter(fh, hasher),
		start:    time.Now(),
		base:     offset,
		total:    total,
		callback: opts.Progress,
	}
	_, copyErr := io.Copy(pw, resp.Body)
	syncErr := fh.Sync()
	closeErr := fh.Close()
	if copyErr != nil {
		// Keep the .part file so the next attempt can resume.
		return "", fmt.Errorf("download %s: %w", u, copyErr)
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if total > 0 {
		info, statErr := os.Stat(part)
		if statErr != nil {
			return "", fmt.Errorf("download %s: cannot stat partial file: %w", u, statErr)
		}
		if info.Size() != total {
			return "", fmt.Errorf("download %s: incomplete (%d of %d bytes)", u, info.Size(), total)
		}
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	if opts.ExpectedSHA256 != "" && !strings.EqualFold(sum, normalizeSHA(opts.ExpectedSHA256)) {
		_ = os.Remove(part)
		return "", fmt.Errorf("%w for %s: got %s want %s", errChecksumMismatch, opts.Dest, sum, normalizeSHA(opts.ExpectedSHA256))
	}
	if err := os.Rename(part, opts.Dest); err != nil {
		return "", err
	}
	opts.report(Progress{Downloaded: total, Total: total, Done: true})
	return sum, nil
}

func (o Options) report(p Progress) {
	if o.Progress != nil {
		o.Progress(p)
	}
}

type progressWriter struct {
	w        io.Writer
	start    time.Time
	base     int64
	written  int64
	total    int64
	callback func(Progress)
	last     time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	now := time.Now()
	if p.callback != nil && (now.Sub(p.last) > 120*time.Millisecond || err != nil) {
		p.last = now
		elapsed := now.Sub(p.start).Seconds()
		speed := 0.0
		if elapsed > 0 {
			speed = float64(p.written) / elapsed
		}
		p.callback(Progress{Downloaded: p.base + p.written, Total: p.total, BytesPerSec: speed})
	}
	return n, err
}

func hashInto(w io.Writer, path string, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyN(w, f, limit)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// HashFile returns the lowercase hex SHA-256 of a file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func normalizeSHA(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "sha256:")
	return s
}

var bsdChecksum = regexp.MustCompile(`(?im)^SHA256\s*\(([^)]+)\)\s*=\s*([0-9a-f]{64})\s*$`)

// FindChecksum extracts the expected SHA-256 for assetName from a checksum
// manifest. It understands GNU coreutils, BSD and single-hash formats.
func FindChecksum(text, assetName string) (string, bool) {
	base := filepath.Base(assetName)
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		hash := normalizeSHA(strings.TrimPrefix(fields[0], "*"))
		if !isSHA256(hash) {
			// Try "hash filename" where hash is second (e.g. "SHA256 (f) = h")
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		name = strings.TrimPrefix(name, "./")
		if strings.EqualFold(filepath.Base(name), base) || strings.EqualFold(name, assetName) {
			return hash, true
		}
	}
	if m := bsdChecksum.FindStringSubmatch(text); m != nil {
		if strings.EqualFold(filepath.Base(m[1]), base) {
			return strings.ToLower(m[2]), true
		}
	}
	// Single hash file (e.g. foo.tar.gz.sha256 containing just the digest).
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) == 1 && isSHA256(normalizeSHA(f[0])) {
			return normalizeSHA(f[0]), true
		}
	}
	return "", false
}

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
