package gh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
