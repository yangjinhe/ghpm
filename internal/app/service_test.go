package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ghpm/internal/config"
)

func tarGz(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type fakeGitHub struct {
	mu       sync.Mutex
	tag      string
	version  string
	withBad  bool
	auth     bool
	archives map[string][]byte
	base     string
}

func (f *fakeGitHub) requireAuth() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth
}

func (f *fakeGitHub) setAuth(v bool) {
	f.mu.Lock()
	f.auth = v
	f.mu.Unlock()
}

func (f *fakeGitHub) setTag(tag, version string) {
	f.mu.Lock()
	f.tag, f.version = tag, version
	f.mu.Unlock()
}

func (f *fakeGitHub) release() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	ver := f.version
	toolName := fmt.Sprintf("tool_%s_linux_amd64.tar.gz", ver)
	assets := []map[string]any{
		{
			"id":                   1,
			"name":                 toolName,
			"size":                 len(f.archives[toolName]),
			"browser_download_url": f.base + "/download/" + toolName,
			"url":                  f.base + "/assets/" + toolName,
		},
	}
	if f.withBad {
		assets = append(assets, map[string]any{
			"id":                   2,
			"name":                 "bad_1.0.0_linux_amd64.tar.gz",
			"size":                 len(f.archives["bad_1.0.0_linux_amd64.tar.gz"]),
			"browser_download_url": f.base + "/download/bad_1.0.0_linux_amd64.tar.gz",
			"url":                  f.base + "/assets/bad_1.0.0_linux_amd64.tar.gz",
		})
	}
	assets = append(assets, map[string]any{
		"id":                   3,
		"name":                 "checksums.txt",
		"size":                 200,
		"browser_download_url": f.base + "/download/checksums.txt",
		"url":                  f.base + "/assets/checksums.txt",
	})
	return map[string]any{
		"tag_name": f.tag, "name": f.tag, "draft": false, "prerelease": false,
		"html_url": "https://example.invalid", "assets": assets,
	}
}

func (f *fakeGitHub) checksums() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	toolName := fmt.Sprintf("tool_%s_linux_amd64.tar.gz", f.version)
	out := fmt.Sprintf("%s  %s\n", sha(f.archives[toolName]), toolName)
	if f.withBad {
		out += fmt.Sprintf("%s  bad_1.0.0_linux_amd64.tar.gz\n", strings.Repeat("0", 64))
	}
	return out
}

func newFakeGitHub(t *testing.T, withBad bool) (*fakeGitHub, func()) {
	t.Helper()
	f := &fakeGitHub{
		tag: "v1.0.0", version: "1.0.0", withBad: withBad,
		archives: map[string][]byte{},
	}
	f.archives["tool_1.0.0_linux_amd64.tar.gz"] = tarGz(t, "tool", "v1")
	f.archives["tool_1.1.0_linux_amd64.tar.gz"] = tarGz(t, "tool-v2", "v2")
	f.archives["bad_1.0.0_linux_amd64.tar.gz"] = tarGz(t, "bad", "bad")

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/tool/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(f.release())
	})
	mux.HandleFunc("/repos/acme/tool/releases/tags/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(f.release())
	})
	serve := func(w http.ResponseWriter, r *http.Request, name string) {
		if f.requireAuth() && r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if name == "checksums.txt" {
			_, _ = w.Write([]byte(f.checksums()))
			return
		}
		f.mu.Lock()
		data, ok := f.archives[name]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, strings.TrimPrefix(r.URL.Path, "/download/"))
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, strings.TrimPrefix(r.URL.Path, "/assets/"))
	})
	srv := httptest.NewServer(mux)
	f.base = srv.URL
	return f, srv.Close
}

func testConfig(t *testing.T, apiBase string, apps ...config.App) (config.Paths, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	paths := config.DefaultPaths()
	paths.ConfigFile = filepath.Join(dir, "config", "config.yaml")
	paths.ConfigDir = filepath.Join(dir, "config")
	paths.DataDir = filepath.Join(dir, "data")
	paths.BinDir = filepath.Join(dir, "bin")

	cfg := config.DefaultsConfig(paths)
	cfg.Path = paths.ConfigFile
	cfg.Source = paths
	cfg.Defaults.DataDir = paths.DataDir
	cfg.Defaults.BinDir = paths.BinDir
	cfg.Defaults.GitHub.APIBase = apiBase
	cfg.Defaults.Concurrency = 2
	cfg.Apps = apps
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return paths, cfg
}

func toolApp() config.App {
	return config.App{
		ID: "tool", Repo: "acme/tool", Channel: "latest",
		Match: config.Match{Platforms: map[string]string{
			"linux/amd64": "tool_{version}_linux_amd64.tar.gz",
		}},
		Install: config.Install{Type: "archive", Link: "tool"},
	}
}

func TestInstallCheckUpdateRollback(t *testing.T) {
	f, closeSrv := newFakeGitHub(t, false)
	defer closeSrv()
	paths, _ := testConfig(t, f.base, toolApp())

	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	res := svc.InstallOne(ctx, "tool", InstallOptions{})
	if res.Err != nil {
		t.Fatalf("install: %v", res.Err)
	}
	if res.Version != "1.0.0" {
		t.Errorf("version = %q", res.Version)
	}
	if res.SHA256 != sha(f.archives["tool_1.0.0_linux_amd64.tar.gz"]) {
		t.Errorf("sha256 = %q", res.SHA256)
	}
	data, err := os.ReadFile(filepath.Join(paths.BinDir, "tool"))
	if err != nil {
		t.Fatalf("read link: %v", err)
	}
	if string(data) != "v1" {
		t.Errorf("binary content = %q", data)
	}
	st, err := svc.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if rec := st.Apps["tool"]; rec == nil || rec.Version != "1.0.0" || rec.LinkPath == "" {
		t.Fatalf("state record = %+v", rec)
	}

	// Publish v1.1.0.
	f.setTag("v1.1.0", "1.1.0")
	checks := svc.CheckFresh(ctx, nil, nil)
	if len(checks) != 1 || checks[0].Err != nil {
		t.Fatalf("check: %+v", checks)
	}
	if !checks[0].UpdateAvailable || checks[0].LatestVersion != "1.1.0" {
		t.Errorf("check result = %+v", checks[0])
	}
	if ids := svc.UpdateIDs(); len(ids) != 1 || ids[0] != "tool" {
		t.Errorf("update ids = %v", ids)
	}

	res = svc.InstallOne(ctx, "tool", InstallOptions{})
	if res.Err != nil {
		t.Fatalf("upgrade: %v", res.Err)
	}
	if res.Previous != "1.0.0" || res.Version != "1.1.0" {
		t.Errorf("upgrade versions = %+v", res)
	}
	data, _ = os.ReadFile(filepath.Join(paths.BinDir, "tool"))
	if string(data) != "v2" {
		t.Errorf("upgraded content = %q", data)
	}

	rb, err := svc.Rollback("tool", "")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rb.Version != "1.0.0" {
		t.Errorf("rollback version = %q", rb.Version)
	}
	data, _ = os.ReadFile(filepath.Join(paths.BinDir, "tool"))
	if string(data) != "v1" {
		t.Errorf("rollback content = %q", data)
	}
}

func TestInstallChecksumMismatch(t *testing.T) {
	f, closeSrv := newFakeGitHub(t, true)
	defer closeSrv()
	bad := config.App{
		ID: "bad", Repo: "acme/tool", Channel: "latest",
		Match: config.Match{Platforms: map[string]string{
			"linux/amd64": "bad_1.0.0_linux_amd64.tar.gz",
		}},
		Install: config.Install{Type: "archive", Bin: "bad"},
	}
	paths, _ := testConfig(t, f.base, bad)
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	res := svc.InstallOne(context.Background(), "bad", InstallOptions{})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", res.Err)
	}
	if _, err := os.Lstat(filepath.Join(paths.BinDir, "bad")); !os.IsNotExist(err) {
		t.Error("link must not be created after a checksum failure")
	}
}

func TestAddGeneratesConfig(t *testing.T) {
	f, closeSrv := newFakeGitHub(t, false)
	defer closeSrv()
	paths, _ := testConfig(t, f.base)
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Add(context.Background(), AddOptions{Input: "https://github.com/acme/tool"})
	if err != nil {
		t.Fatal(err)
	}
	if res.App.ID != "tool" || res.App.Repo != "acme/tool" {
		t.Errorf("app = %+v", res.App)
	}
	if got := res.App.Match.Platforms["linux/amd64"]; got != "tool_{version}_linux_amd64.tar.gz" {
		t.Errorf("match = %q", got)
	}
	if res.App.Install.Type != "archive" {
		t.Errorf("install type = %q", res.App.Install.Type)
	}
	// Reload from disk to verify persistence.
	cfg, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].ID != "tool" {
		t.Errorf("persisted apps = %+v", cfg.Apps)
	}
	if _, err := svc.Add(context.Background(), AddOptions{Input: "acme/tool"}); err == nil {
		t.Error("adding the same repo twice should fail")
	}
}

func TestPinnedSkipsUpdates(t *testing.T) {
	f, closeSrv := newFakeGitHub(t, false)
	defer closeSrv()
	paths, _ := testConfig(t, f.base, toolApp())
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if res := svc.InstallOne(ctx, "tool", InstallOptions{}); res.Err != nil {
		t.Fatal(res.Err)
	}
	if err := svc.SetPinned("tool", true); err != nil {
		t.Fatal(err)
	}
	f.setTag("v1.1.0", "1.1.0")
	checks := svc.CheckFresh(ctx, nil, nil)
	if len(checks) != 1 || !checks[0].Pinned || checks[0].UpdateAvailable {
		t.Errorf("pinned check = %+v", checks[0])
	}
	if ids := svc.UpdateIDs(); len(ids) != 0 {
		t.Errorf("pinned app in update list: %v", ids)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		cur, latest string
		newer       bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "1.0.0", false},
		{"1.2.0", "1.10.0", true},
		{"v2.0.0", "1.9.9", false},
		{"", "1.0.0", true},
		{"nightly", "nightly-2", true},
	}
	for _, c := range cases {
		got, _ := CompareVersions(c.cur, c.latest)
		if got != c.newer {
			t.Errorf("CompareVersions(%q,%q) = %v want %v", c.cur, c.latest, got, c.newer)
		}
	}
}

func TestPrivateRepoDownloadUsesToken(t *testing.T) {
	f, closeSrv := newFakeGitHub(t, false)
	defer closeSrv()
	f.setAuth(true)
	paths, _ := testConfig(t, f.base, toolApp())
	t.Setenv("GITHUB_TOKEN", "test-token")
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	res := svc.InstallOne(context.Background(), "tool", InstallOptions{})
	if res.Err != nil {
		t.Fatalf("private install: %v", res.Err)
	}
	if res.SHA256 != sha(f.archives["tool_1.0.0_linux_amd64.tar.gz"]) {
		t.Errorf("sha = %q", res.SHA256)
	}
	if _, err := os.ReadFile(filepath.Join(paths.BinDir, "tool")); err != nil {
		t.Errorf("binary not linked: %v", err)
	}

	// Without a token the download must fail rather than silently use the
	// public URL, which the private server rejects.
	if err := os.RemoveAll(paths.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(svc.CacheDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(paths.BinDir, "tool")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_TOKEN", "")
	svc2, err := New(Options{ConfigPath: paths.ConfigFile, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if res := svc2.InstallOne(context.Background(), "tool", InstallOptions{}); res.Err == nil {
		t.Fatal("anonymous private install should fail")
	}
}
