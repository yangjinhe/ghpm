package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/blake2b"

	"ghpm/internal/config"
	"ghpm/internal/platform"
	"ghpm/internal/signature"
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

// hostKey and the asset helpers keep the fixtures valid on any GOOS/GOARCH,
// so the suite also runs on the macOS CI runner.
func hostKey() string { return runtime.GOOS + "/" + runtime.GOARCH }

func tarAsset(prefix, version string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", prefix, version, runtime.GOOS, runtime.GOARCH)
}

func toolAsset(version string) string { return tarAsset("tool", version) }
func badAsset() string                { return tarAsset("bad", "1.0.0") }
func signedAsset() string             { return tarAsset("signed", "1.0.0") }

func matchPattern(prefix string) string {
	return prefix + "_{version}_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
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
	toolName := toolAsset(ver)
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
			"name":                 badAsset(),
			"size":                 len(f.archives[badAsset()]),
			"browser_download_url": f.base + "/download/" + badAsset(),
			"url":                  f.base + "/assets/" + badAsset(),
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
	toolName := toolAsset(f.version)
	out := fmt.Sprintf("%s  %s\n", sha(f.archives[toolName]), toolName)
	if f.withBad {
		out += fmt.Sprintf("%s  %s\n", strings.Repeat("0", 64), badAsset())
	}
	return out
}

func newFakeGitHub(t *testing.T, withBad bool) (*fakeGitHub, func()) {
	t.Helper()
	f := &fakeGitHub{
		tag: "v1.0.0", version: "1.0.0", withBad: withBad,
		archives: map[string][]byte{},
	}
	f.archives[toolAsset("1.0.0")] = tarGz(t, "tool", "v1")
	f.archives[toolAsset("1.1.0")] = tarGz(t, "tool-v2", "v2")
	f.archives[badAsset()] = tarGz(t, "bad", "bad")

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
			hostKey(): matchPattern("tool"),
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
	if res.SHA256 != sha(f.archives[toolAsset("1.0.0")]) {
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
			hostKey(): badAsset(),
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
	if got := res.App.Match.Platforms[hostKey()]; got != matchPattern("tool") {
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

func TestInstallVerifiesMinisignChecksums(t *testing.T) {
	asset := tarGz(t, "signed", "payload")
	assetName := signedAsset()
	checksums := fmt.Sprintf("%s  %s\n", sha(asset), assetName)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID := make([]byte, 8)
	if _, err := rand.Read(keyID); err != nil {
		t.Fatal(err)
	}
	sum := blake2b.Sum512([]byte(checksums))
	sigBlob := append(append([]byte("Ed"), keyID...), ed25519.Sign(priv, sum[:])...)
	sigFile := []byte("untrusted comment: signature from minisign secret key\n" +
		base64.StdEncoding.EncodeToString(sigBlob) + "\ntrusted comment: timestamp:1\tfile:checksums.txt\n")
	pubFile := base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID...), pub...))

	files := map[string][]byte{assetName: asset, "checksums.txt": []byte(checksums), "checksums.txt.minisig": sigFile}
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/acme/signed/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		assets := []map[string]any{}
		for i, name := range []string{assetName, "checksums.txt", "checksums.txt.minisig"} {
			assets = append(assets, map[string]any{
				"id": i + 1, "name": name, "size": len(files[name]),
				"browser_download_url": base + "/download/" + name,
				"url":                  base + "/assets/" + name,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.0.0", "name": "v1.0.0", "draft": false, "prerelease": false, "assets": assets,
		})
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/download/")
		data, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	app := config.App{
		ID: "signed", Repo: "acme/signed", Channel: "latest",
		Match:   config.Match{Platforms: map[string]string{hostKey(): assetName}},
		Install: config.Install{Type: "archive", Bin: "signed"},
		Signature: signature.Config{
			Type: "minisign", Key: pubFile, Mode: "required",
		},
	}
	paths, _ := testConfig(t, base, app)
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	res := svc.InstallOne(context.Background(), "signed", InstallOptions{})
	if res.Err != nil {
		t.Fatalf("install with valid signature: %v", res.Err)
	}
	if data, err := os.ReadFile(filepath.Join(paths.BinDir, "signed")); err != nil || string(data) != "payload" {
		t.Fatalf("binary = %q, err = %v", data, err)
	}

	// Sign the same manifest with a different key and drop the cached copy:
	// the next install must fail instead of silently accepting the checksum.
	_ = os.RemoveAll(filepath.Join(svc.CacheDir, "signatures"))
	otherPub, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = otherPub
	wrongBlob := append(append([]byte("Ed"), keyID...), ed25519.Sign(otherPriv, sum[:])...)
	files["checksums.txt.minisig"] = []byte("untrusted comment: signature from minisign secret key\n" +
		base64.StdEncoding.EncodeToString(wrongBlob) + "\n")
	res = svc.InstallOne(context.Background(), "signed", InstallOptions{})
	if res.Err == nil || !strings.Contains(strings.ToLower(res.Err.Error()), "signature") {
		t.Fatalf("want signature failure, got %v", res.Err)
	}
}

func TestCheckUsesGraphQLBatch(t *testing.T) {
	var restCalls, graphqlCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			graphqlCalls.Add(1)
			var req struct{ Query string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			data := map[string]any{}
			re := regexp.MustCompile(`r(\d+):repository\(owner:"([^"]+)",name:"([^"]+)"\)`)
			for _, m := range re.FindAllStringSubmatch(req.Query, -1) {
				data["r"+m[1]] = map[string]any{"latestRelease": map[string]any{"tagName": "v2.0.0"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		restCalls.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	appA := config.App{ID: "a", Repo: "acme/a", Channel: "latest"}
	appB := config.App{ID: "b", Repo: "acme/b", Channel: "latest"}
	paths, _ := testConfig(t, srv.URL, appA, appB)
	t.Setenv("GITHUB_TOKEN", "batch-token")
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	results := svc.CheckFresh(context.Background(), nil, nil)
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.ID, r.Err)
		}
		if r.LatestTag != "v2.0.0" || !r.UpdateAvailable {
			t.Errorf("%s = %+v", r.ID, r)
		}
	}
	if graphqlCalls.Load() != 1 {
		t.Errorf("graphql calls = %d, want 1", graphqlCalls.Load())
	}
	if restCalls.Load() != 0 {
		t.Errorf("rest calls = %d, want 0", restCalls.Load())
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
	if res.SHA256 != sha(f.archives[toolAsset("1.0.0")]) {
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

func appZip(t *testing.T, execBody string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		mode os.FileMode
		body string
	}{
		{"Motty.app/Contents/Info.plist", 0o644, "<plist/>"},
		{"Motty.app/Contents/MacOS/motty", 0o644, execBody},
	} {
		hdr := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		hdr.SetMode(f.mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallAppBundleAndRemove(t *testing.T) {
	payload := appZip(t, "app-binary")
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/t8y2/motty/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.0.0", "name": "v1.0.0", "draft": false, "prerelease": false,
			"assets": []map[string]any{{
				"id": 1, "name": "motty-1.0.0-darwin-arm64.zip", "size": len(payload),
				"browser_download_url": base + "/download/motty.zip",
				"url":                  base + "/assets/motty.zip",
			}},
		})
	})
	mux.HandleFunc("/download/motty.zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	appsDir := filepath.Join(t.TempDir(), "Applications")
	app := config.App{
		ID: "motty", Repo: "t8y2/motty", Channel: "latest",
		Match: config.Match{Platforms: map[string]string{
			"darwin/arm64": "motty-{version}-darwin-arm64.zip",
		}},
	}
	paths, cfg := testConfig(t, base, app)
	cfg.Defaults.AppsDir = appsDir
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	res := svc.InstallOne(context.Background(), "motty", InstallOptions{
		Platform: platform.Platform{OS: "darwin", Arch: "arm64"},
	})
	if res.Err != nil {
		t.Fatalf("install: %v", res.Err)
	}
	if res.Type != "app" {
		t.Errorf("type = %q", res.Type)
	}
	if filepath.Dir(res.Link) != appsDir {
		t.Errorf("link = %q, want under %q", res.Link, appsDir)
	}
	if info, err := os.Lstat(res.Link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("app link missing: %v %v", info, err)
	}
	st, err := svc.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if rec := st.Apps["motty"]; rec == nil || rec.LinkPath != res.Link || rec.BinRel != "Motty.app" {
		t.Fatalf("state = %+v", rec)
	}

	if _, err := svc.Remove("motty", false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Lstat(res.Link); !os.IsNotExist(err) {
		t.Errorf("app link still present after remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.DataDir, "apps", "motty")); !os.IsNotExist(err) {
		t.Errorf("app version dir still present: %v", err)
	}
}

func TestResolveAppIdentifier(t *testing.T) {
	paths, _ := testConfig(t, "https://api.github.com", toolApp())
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"tool":                          "tool",
		"TOOL":                          "tool",
		"acme/tool":                     "tool",
		"ACME/TOOL":                     "tool",
		"https://github.com/acme/tool":  "tool",
		"git@github.com:acme/tool.git":  "tool",
		"github.com/acme/tool/releases": "tool",
	}
	for input, want := range cases {
		got, err := svc.ResolveID(input)
		if err != nil || got != want {
			t.Errorf("ResolveID(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := svc.ResolveID("other/repo"); err == nil || !strings.Contains(err.Error(), "no app configured") {
		t.Errorf("unknown repo error = %v", err)
	}
	if _, err := svc.ResolveID("nope"); err == nil {
		t.Error("unknown id should fail")
	}
}

func TestAddAmbiguousAssetsSuggestsAsset(t *testing.T) {
	f, closeSrv := newFakeGitHub(t, true) // tool + bad assets, same platform
	defer closeSrv()
	paths, _ := testConfig(t, f.base)
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Add(context.Background(), AddOptions{Input: "acme/tool"})
	var choice *AssetChoiceError
	if !errors.As(err, &choice) {
		t.Fatalf("want AssetChoiceError, got %v", err)
	}
	if len(choice.Candidates) < 2 {
		t.Fatalf("candidates = %+v", choice.Candidates)
	}
	msg := err.Error()
	for _, want := range []string{"--asset", toolAsset("1.0.0"), badAsset()} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q is missing %q", msg, want)
		}
	}

	// Retrying with --asset (AddOptions.Asset) resolves the choice.
	res, err := svc.Add(context.Background(), AddOptions{
		Input: "acme/tool", ID: "bad", Asset: badAsset(),
	})
	if err != nil {
		t.Fatalf("explicit asset: %v", err)
	}
	if res.App.ID != "bad" || res.Chosen.Name != badAsset() {
		t.Errorf("result = %+v, chosen %+v", res.App, res.Chosen)
	}
}

func TestAddSameRepoDifferentIDs(t *testing.T) {
	f, closeSrv := newFakeGitHub(t, false)
	defer closeSrv()
	paths, _ := testConfig(t, f.base)
	svc, err := New(Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := svc.Add(ctx, AddOptions{Input: "acme/tool"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Add(ctx, AddOptions{Input: "acme/tool", ID: "tool-gui"}); err != nil {
		t.Fatalf("same repo with another id: %v", err)
	}
	if len(svc.Cfg.Apps) != 2 {
		t.Fatalf("apps = %+v", svc.Cfg.Apps)
	}
	if _, err := svc.ResolveID("acme/tool"); err == nil || !strings.Contains(err.Error(), "multiple apps") {
		t.Fatalf("repo resolution should be ambiguous, got %v", err)
	}
	id, err := svc.ResolveID("tool-gui")
	if err != nil || id != "tool-gui" {
		t.Fatalf("ResolveID(tool-gui) = %q, %v", id, err)
	}
	if id, err := svc.ResolveID("TOOL"); err != nil || id != "tool" {
		t.Fatalf("ResolveID(TOOL) = %q, %v", id, err)
	}
}
