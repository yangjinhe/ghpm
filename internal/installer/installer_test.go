package installer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ghpm/internal/config"
	"ghpm/internal/download"
)

func makeArchive(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "tool", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
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

func TestInstallLayoutAndUpgrade(t *testing.T) {
	versions := map[string][]byte{
		"/1": makeArchive(t, "v1"),
		"/2": makeArchive(t, "v2"),
		"/3": makeArchive(t, "v3"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := versions[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	root := t.TempDir()
	app := config.App{
		ID: "tool", Repo: "acme/tool",
		Match:   config.Match{Platforms: map[string]string{"linux/amd64": "tool_{version}_linux_amd64.tar.gz"}},
		Install: config.Install{Type: "archive", Bin: "tool"},
	}
	fetcher, _ := download.New("")
	req := Request{
		App: app, Tag: "v1.0.0", Version: "1.0.0",
		AssetName: "tool_1.0.0_linux_amd64.tar.gz", AssetURL: srv.URL + "/1",
		DataDir: filepath.Join(root, "data"), BinDir: filepath.Join(root, "bin"),
		CacheDir: filepath.Join(root, "cache"), PruneKeep: 2, Verify: "if-available",
	}
	res, err := Install(context.Background(), fetcher, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.BinRel != "tool" {
		t.Errorf("bin rel = %q", res.BinRel)
	}
	versionDir := filepath.Join(root, "data", "apps", "tool", "1.0.0")
	entries, _ := os.ReadDir(versionDir)
	if len(entries) != 1 || entries[0].Name() != "tool" {
		t.Errorf("version dir should only hold the binary, got %v", entries)
	}

	// Upgrade twice; prune_keep=2 should remove 1.0.0.
	req.Tag, req.Version, req.AssetName, req.AssetURL = "v2.0.0", "2.0.0", "tool_2.0.0_linux_amd64.tar.gz", srv.URL+"/2"
	if _, err := Install(context.Background(), fetcher, req); err != nil {
		t.Fatal(err)
	}
	req.Tag, req.Version, req.AssetName, req.AssetURL = "v3.0.0", "3.0.0", "tool_3.0.0_linux_amd64.tar.gz", srv.URL+"/3"
	res, err = Install(context.Background(), fetcher, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "1.0.0" {
		t.Errorf("removed = %v", res.Removed)
	}
	if _, err := os.Stat(filepath.Join(versionDir, "..", "1.0.0")); !os.IsNotExist(err) {
		t.Error("old version still present")
	}
	data, err := os.ReadFile(filepath.Join(root, "bin", "tool"))
	if err != nil || string(data) != "v3" {
		t.Errorf("linked content = %q %v", data, err)
	}
	// The downloaded archive stays in the cache, not in the version dir.
	if _, err := os.Stat(filepath.Join(root, "cache", "downloads", "tool", "3.0.0", "tool_3.0.0_linux_amd64.tar.gz")); err != nil {
		t.Errorf("cache asset missing: %v", err)
	}
}

func TestInstallRefusesForeignFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(makeArchive(t, "v1"))
	}))
	defer srv.Close()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(binDir, "tool")
	if err := os.WriteFile(foreign, []byte("owned by user"), 0o755); err != nil {
		t.Fatal(err)
	}
	fetcher, _ := download.New("")
	req := Request{
		App: config.App{
			ID: "tool", Repo: "acme/tool",
			Install: config.Install{Type: "archive", Bin: "tool"},
		},
		Tag: "v1.0.0", Version: "1.0.0",
		AssetName: "tool_1.0.0_linux_amd64.tar.gz", AssetURL: srv.URL,
		DataDir: filepath.Join(root, "data"), BinDir: binDir,
		CacheDir: filepath.Join(root, "cache"), PruneKeep: 2,
	}
	if _, err := Install(context.Background(), fetcher, req); !errors.Is(err, ErrExistingBinary) {
		t.Fatalf("want ErrExistingBinary, got %v", err)
	}
	req.Force = true
	if _, err := Install(context.Background(), fetcher, req); err != nil {
		t.Fatalf("force install: %v", err)
	}
}

func TestBinaryInstallUsesAppIDAsLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#!/bin/sh\necho hi\n"))
	}))
	defer srv.Close()
	root := t.TempDir()
	fetcher, _ := download.New("")
	req := Request{
		App: config.App{ID: "mytool", Repo: "acme/mytool", Install: config.Install{Type: "binary"}},
		Tag: "v1.0.0", Version: "1.0.0",
		AssetName: "mytool-linux-x86_64", AssetURL: srv.URL,
		DataDir: filepath.Join(root, "data"), BinDir: filepath.Join(root, "bin"),
		CacheDir: filepath.Join(root, "cache"), PruneKeep: 2,
	}
	res, err := Install(context.Background(), fetcher, req)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.LinkPath) != "mytool" {
		t.Errorf("link = %q", res.LinkPath)
	}
	if filepath.Base(res.BinRel) != "mytool-linux-x86_64" {
		t.Errorf("binary name changed: %q", res.BinRel)
	}
	data, err := os.ReadFile(filepath.Join(root, "bin", "mytool"))
	if err != nil || len(data) == 0 {
		t.Errorf("link content: %v", err)
	}
}

func TestPkgInstallUsesMacOSInstaller(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install.type pkg is macOS only")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake flat package"))
	}))
	defer srv.Close()
	root := t.TempDir()
	var got []string
	fetcher, _ := download.New("")
	req := Request{
		App:       config.App{ID: "pkgtool", Repo: "acme/pkgtool", Install: config.Install{Type: "pkg"}},
		Tag:       "v1.0.0",
		Version:   "1.0.0",
		AssetName: "pkgtool_1.0.0_darwin_arm64.pkg",
		AssetURL:  srv.URL,
		DataDir:   filepath.Join(root, "data"),
		BinDir:    filepath.Join(root, "bin"),
		CacheDir:  filepath.Join(root, "cache"),
		PruneKeep: 2,
		SudoRunner: func(ctx context.Context, name string, args ...string) error {
			got = append([]string{name}, args...)
			return nil
		},
	}
	res, err := Install(context.Background(), fetcher, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != "pkg" {
		t.Errorf("type = %q", res.Type)
	}
	idx := -1
	for i, a := range got {
		if a == "installer" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("installer not invoked: %v", got)
	}
	args := got[idx+1:]
	want := []string{"-pkg", filepath.Join(root, "cache", "packages", req.AssetName), "-target", "/"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args = %v, want %v", args, want)
			break
		}
	}
}

func TestPkgInstallRejectedOffMacOS(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this checks the non-macOS error path")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("package"))
	}))
	defer srv.Close()
	root := t.TempDir()
	fetcher, _ := download.New("")
	req := Request{
		App:       config.App{ID: "pkgtool", Repo: "acme/pkgtool", Install: config.Install{Type: "pkg"}},
		Tag:       "v1.0.0",
		Version:   "1.0.0",
		AssetName: "pkgtool_1.0.0_darwin_arm64.pkg",
		AssetURL:  srv.URL,
		DataDir:   filepath.Join(root, "data"),
		BinDir:    filepath.Join(root, "bin"),
		CacheDir:  filepath.Join(root, "cache"),
		PruneKeep: 2,
	}
	if _, err := Install(context.Background(), fetcher, req); err == nil || !strings.Contains(err.Error(), "macOS") {
		t.Fatalf("want macOS-only error, got %v", err)
	}
}

type zipEntry struct {
	name    string
	mode    os.FileMode
	body    string
	symlink bool
}

func makeZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if e.symlink {
			mode = os.ModeSymlink | 0o777
		}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mottyZip(t *testing.T, body string) []byte {
	t.Helper()
	return makeZip(t, []zipEntry{
		{name: "Motty.app/Contents/Info.plist", mode: 0o644, body: "<plist/>"},
		{name: "Motty.app/Contents/MacOS/motty", mode: 0o644, body: body},
		{name: "Motty.app/Contents/Resources/AppIcon.icns", mode: 0o644, body: "icon"},
		{name: "Motty.app/Contents/Resources/current", mode: 0o777, body: "AppIcon.icns", symlink: true},
	})
}

func newAppServer(t *testing.T, versions map[string][]byte) (*httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := versions[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	return srv, srv.Close
}

func appRequest(root, url, version string) Request {
	return Request{
		App:       config.App{ID: "motty", Repo: "t8y2/motty", Install: config.Install{Type: "app"}},
		Tag:       "v" + version,
		Version:   version,
		AssetName: "motty-" + version + "-darwin-arm64.zip",
		AssetURL:  url,
		DataDir:   filepath.Join(root, "data"),
		BinDir:    filepath.Join(root, "bin"),
		AppsDir:   filepath.Join(root, "Applications"),
		CacheDir:  filepath.Join(root, "cache"),
		PruneKeep: 3,
	}
}

func TestInstallAppBundle(t *testing.T) {
	srv, closeSrv := newAppServer(t, map[string][]byte{
		"/1": mottyZip(t, "app-binary"),
		"/2": mottyZip(t, "app-binary-v2"),
	})
	defer closeSrv()
	root := t.TempDir()
	fetcher, _ := download.New("")

	req := appRequest(root, srv.URL+"/1", "1.0.0")
	res, err := Install(context.Background(), fetcher, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != "app" {
		t.Errorf("type = %q, want app", res.Type)
	}
	if res.BinRel != "Motty.app" {
		t.Errorf("bin rel = %q", res.BinRel)
	}
	if filepath.Base(res.LinkPath) != "Motty.app" {
		t.Errorf("link = %q", res.LinkPath)
	}
	target, err := os.Readlink(res.LinkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != res.BinPath {
		t.Errorf("link target = %q, want %q", target, res.BinPath)
	}
	exe := filepath.Join(res.BinPath, "Contents", "MacOS", "motty")
	if info, err := os.Stat(exe); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("bundle executable mode not restored: %v %v", info, err)
	}
	link := filepath.Join(res.BinPath, "Contents", "Resources", "current")
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("bundle symlink was not preserved: %v %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "bin", "Motty.app")); !os.IsNotExist(err) {
		t.Errorf("app must not be linked into bin dir: %v", err)
	}

	// Upgrading repoints the same /Applications-style link.
	req = appRequest(root, srv.URL+"/2", "2.0.0")
	res2, err := Install(context.Background(), fetcher, req)
	if err != nil {
		t.Fatal(err)
	}
	target, err = os.Readlink(res2.LinkPath)
	if err != nil {
		t.Fatal(err)
	}
	if target != res2.BinPath || !strings.Contains(target, string(filepath.Separator)+"2.0.0"+string(filepath.Separator)) {
		t.Errorf("upgraded link = %q, want version 2 %q", target, res2.BinPath)
	}
}

func TestInstallAppBundleAutoDetect(t *testing.T) {
	srv, closeSrv := newAppServer(t, map[string][]byte{"/1": mottyZip(t, "app")})
	defer closeSrv()
	root := t.TempDir()
	fetcher, _ := download.New("")
	req := appRequest(root, srv.URL+"/1", "1.0.0")
	req.App.Install.Type = "" // inferred as archive from the .zip name
	res, err := Install(context.Background(), fetcher, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != "app" || res.BinRel != "Motty.app" {
		t.Errorf("auto detect = %+v", res)
	}
	if _, err := os.Readlink(res.LinkPath); err != nil {
		t.Errorf("app link missing: %v", err)
	}
}

func TestArchiveWithCLIAndAppKeepsCLI(t *testing.T) {
	payload := makeZip(t, []zipEntry{
		{name: "tool", mode: 0o755, body: "cli"},
		{name: "Tool.app/Contents/Info.plist", mode: 0o644, body: "<plist/>"},
		{name: "Tool.app/Contents/MacOS/tool", mode: 0o755, body: "gui"},
	})
	srv, closeSrv := newAppServer(t, map[string][]byte{"/1": payload})
	defer closeSrv()
	root := t.TempDir()
	fetcher, _ := download.New("")
	req := appRequest(root, srv.URL+"/1", "1.0.0")
	req.App.Install.Type = ""
	res, err := Install(context.Background(), fetcher, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != "archive" || res.BinRel != "tool" {
		t.Errorf("CLI should win when both exist: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "tool")); err != nil {
		t.Errorf("CLI link missing: %v", err)
	}
}

func TestAppTypeRequiresBundle(t *testing.T) {
	payload := makeZip(t, []zipEntry{{name: "tool", mode: 0o755, body: "cli"}})
	srv, closeSrv := newAppServer(t, map[string][]byte{"/1": payload})
	defer closeSrv()
	root := t.TempDir()
	fetcher, _ := download.New("")
	_, err := Install(context.Background(), fetcher, appRequest(root, srv.URL+"/1", "1.0.0"))
	if err == nil || !strings.Contains(err.Error(), ".app") {
		t.Fatalf("want missing bundle error, got %v", err)
	}
}

func TestDMGRequiresMacOS(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("the macOS path is covered by the darwin test")
	}
	root := t.TempDir()
	req := Request{
		App:       config.App{ID: "motty", Repo: "t8y2/motty", Install: config.Install{Type: "dmg"}},
		Tag:       "v1.0.0",
		Version:   "1.0.0",
		AssetName: "motty-1.0.0-darwin-arm64.dmg",
		AssetURL:  "http://127.0.0.1:1/motty.dmg",
		DataDir:   filepath.Join(root, "data"),
		BinDir:    filepath.Join(root, "bin"),
		AppsDir:   filepath.Join(root, "Applications"),
		CacheDir:  filepath.Join(root, "cache"),
	}
	// The guard fires before any download, so a nil fetcher is safe here.
	if _, err := Install(context.Background(), nil, req); err == nil || !strings.Contains(err.Error(), "macOS") {
		t.Fatalf("want macOS-only error, got %v", err)
	}
}
