package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"ghpm/internal/config"
)

func TestETagRevalidation(t *testing.T) {
	var requests, notModified int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "1800000000")
		if r.Header.Get("If-None-Match") == `"v1"` {
			atomic.AddInt32(&notModified, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.2.3",
			"assets":   []map[string]any{{"name": "tool.tar.gz", "browser_download_url": "https://example/x"}},
		})
	}))
	defer srv.Close()

	c, err := New(config.GitHub{APIBase: srv.URL}, "", t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rel, err := c.ResolveRelease(ctx, "acme/tool", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v1.2.3" || len(rel.Assets) != 1 {
		t.Errorf("release = %+v", rel)
	}
	// Force revalidation: the server answers 304 and the cached body is used.
	c.BypassCache = true
	rel, err = c.ResolveRelease(ctx, "acme/tool", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v1.2.3" {
		t.Errorf("cached release = %+v", rel)
	}
	if atomic.LoadInt32(&notModified) != 1 {
		t.Errorf("expected one 304, got %d", notModified)
	}
	if rl := c.RateLimit(); rl.Limit != 5000 || rl.Remaining != 4999 || !rl.Reset.After(time.Unix(1700000000, 0)) {
		t.Errorf("rate limit = %+v", rl)
	}
}

func TestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c, _ := New(config.GitHub{APIBase: srv.URL}, "", "", 0)
	_, err := c.ResolveRelease(context.Background(), "acme/missing", "latest")
	if err == nil {
		t.Fatal("want error")
	}
}

func TestPrereleaseSelection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/tool/releases" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v2.0.0-rc1", "draft": false, "prerelease": true},
			{"tag_name": "v1.9.0", "draft": false},
		})
	}))
	defer srv.Close()
	c, _ := New(config.GitHub{APIBase: srv.URL}, "", "", 0)
	rel, err := c.ResolveRelease(context.Background(), "acme/tool", "prerelease")
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v2.0.0-rc1" {
		t.Errorf("tag = %q", rel.TagName)
	}
}

func TestDownloadSource(t *testing.T) {
	t.Setenv("GHPM_TOKEN", "secret")
	c, err := New(config.GitHub{APIBase: "https://api.github.com", TokenEnv: "GHPM_TOKEN"}, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	a := Asset{URL: "https://github.com/x", APIURL: "https://api.github.com/assets/1"}
	url, headers := c.DownloadSource(a)
	if url != a.APIURL || headers["Authorization"] != "Bearer secret" || headers["Accept"] != "application/octet-stream" {
		t.Errorf("authenticated source = %q %v", url, headers)
	}

	t.Setenv("GHPM_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	c2, _ := New(config.GitHub{APIBase: "https://api.github.com", TokenEnv: "GHPM_TOKEN"}, "", "", 0)
	url, headers = c2.DownloadSource(a)
	if url != a.URL || headers != nil {
		t.Errorf("anonymous source = %q %v", url, headers)
	}
}

func TestLatestTagsGraphQL(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer gql-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&requests, 1)
		var req struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode query: %v", err)
			return
		}
		data := map[string]any{}
		re := regexp.MustCompile(`r(\d+):repository\(owner:"([^"]+)",name:"([^"]+)"\)`)
		for _, m := range re.FindAllStringSubmatch(req.Query, -1) {
			name := m[2] + "/" + m[3]
			if name == "acme/none" {
				data["r"+m[1]] = map[string]any{"latestRelease": nil}
				continue
			}
			data["r"+m[1]] = map[string]any{
				"latestRelease": map[string]any{"tagName": "v2.0.0"},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	t.Setenv("GQL_TOKEN", "gql-token")
	c, err := New(config.GitHub{APIBase: srv.URL, TokenEnv: "GQL_TOKEN"}, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var repos []string
	for i := 0; i < 59; i++ {
		repos = append(repos, fmt.Sprintf("acme/app%02d", i))
	}
	repos = append(repos, "acme/none")
	tags, err := c.LatestTags(context.Background(), repos)
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Errorf("requests = %d, want 2 (60 repos, 50 per query)", got)
	}
	if len(tags) != 59 {
		t.Errorf("tags = %d, want 59", len(tags))
	}
	if tags["acme/app00"] != "v2.0.0" {
		t.Errorf("tag = %q", tags["acme/app00"])
	}
	if _, ok := tags["acme/none"]; ok {
		t.Error("repository without a release must be omitted")
	}
}

func TestLatestTagsRequiresToken(t *testing.T) {
	t.Setenv("GQL_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	c, _ := New(config.GitHub{APIBase: "https://api.github.com", TokenEnv: "GQL_TOKEN"}, "", "", 0)
	if _, err := c.LatestTags(context.Background(), []string{"acme/app"}); err == nil {
		t.Fatal("want error without a token")
	}
}

func TestGraphQLURLEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://api.github.com":     "https://api.github.com/graphql",
		"https://ghe.example/api/v3": "https://ghe.example/api/graphql",
		"https://ghe.example":        "https://ghe.example/graphql",
	}
	for base, want := range cases {
		c := &Client{apiBase: base}
		if got := c.graphqlURL(); got != want {
			t.Errorf("graphqlURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestResolveReleaseFreshRevalidates(t *testing.T) {
	var tag, etag atomic.Value
	tag.Store("v1.0.0")
	etag.Store(`"e1"`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag.Load().(string) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag.Load().(string))
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag.Load().(string)})
	}))
	defer srv.Close()

	c, err := New(config.GitHub{APIBase: srv.URL}, "", t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rel, err := c.ResolveRelease(ctx, "acme/tool", "latest")
	if err != nil || rel.TagName != "v1.0.0" {
		t.Fatalf("first: %+v %v", rel, err)
	}
	tag.Store("v1.1.0")
	etag.Store(`"e2"`)

	// Within the TTL the cached body is returned.
	rel, err = c.ResolveRelease(ctx, "acme/tool", "latest")
	if err != nil || rel.TagName != "v1.0.0" {
		t.Fatalf("cached: %+v %v", rel, err)
	}
	// Fresh revalidates and sees the new tag immediately.
	rel, err = c.ResolveReleaseFresh(ctx, "acme/tool", "latest")
	if err != nil || rel.TagName != "v1.1.0" {
		t.Fatalf("fresh: %+v %v", rel, err)
	}
}
