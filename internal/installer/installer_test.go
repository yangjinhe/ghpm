package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
