package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ghpm/internal/config"
)

type cliGitHub struct {
	mu      sync.Mutex
	tag     string
	version string
	files   map[string][]byte
	base    string
}

func (f *cliGitHub) setVersion(tag, version, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tag, f.version = tag, version
	name := fmt.Sprintf("tool_%s_linux_amd64.tar.gz", version)
	f.files[name] = tarGzCLI("tool", content)
}

func (f *cliGitHub) release() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := fmt.Sprintf("tool_%s_linux_amd64.tar.gz", f.version)
	return map[string]any{
		"tag_name": f.tag, "name": f.tag, "draft": false, "prerelease": false,
		"assets": []map[string]any{{
			"id": 1, "name": name, "size": len(f.files[name]),
			"browser_download_url": f.base + "/download/" + name,
			"url":                  f.base + "/assets/" + name,
		}},
	}
}

func (f *cliGitHub) file(name string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[name]
	return b, ok
}

func tarGzCLI(name, body string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func newCLIEnv(t *testing.T) (*cliGitHub, string, func()) {
	t.Helper()
	gh := &cliGitHub{tag: "v1.0.0", version: "1.0.0", files: map[string][]byte{}}
	gh.setVersion("v1.0.0", "1.0.0", "v1")
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/tool/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(gh.release())
	})
	mux.HandleFunc("/repos/acme/tool/releases/tags/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(gh.release())
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/download/")
		if b, ok := gh.file(name); ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	gh.base = srv.URL

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("GHPM_CONFIG", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("PATH", os.Getenv("PATH"))

	cfgPath := filepath.Join(home, ".config", "ghpm", "config.yaml")
	paths := config.DefaultPaths()
	cfg := config.DefaultsConfig(paths)
	cfg.Path = cfgPath
	cfg.Source = paths
	cfg.Defaults.Verify = "skip"
	cfg.Defaults.GitHub.APIBase = srv.URL
	cfg.Apps = []config.App{{
		ID: "tool", Repo: "acme/tool", Channel: "latest",
		Match: config.Match{Platforms: map[string]string{
			"linux/amd64":  "tool_{version}_linux_amd64.tar.gz",
			"linux/arm64":  "tool_{version}_linux_amd64.tar.gz",
			"darwin/amd64": "tool_{version}_linux_amd64.tar.gz",
			"darwin/arm64": "tool_{version}_linux_amd64.tar.gz",
		}},
		Install: config.Install{Type: "archive", Bin: "tool", Link: "tool"},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return gh, cfgPath, srv.Close
}

// runCLI executes a command in-process, returning stdout.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	flags = rootFlags{}
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	outCh := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		outCh <- b
	}()
	cmd := newRoot()
	cmd.SetArgs(args)
	cmd.SetOut(w)
	cmd.SetErr(w)
	runErr := cmd.Execute()
	_ = w.Close()
	os.Stdout = oldStdout
	out := <-outCh
	_ = r.Close()
	return string(out), runErr
}

func TestCLIWorkflow(t *testing.T) {
	gh, cfg, closer := newCLIEnv(t)
	defer closer()
	bin := filepath.Join(os.Getenv("HOME"), ".local", "bin", "tool")

	out, err := runCLI(t, "-c", cfg, "install", "acme/tool", "--json")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	var installed []installResultJSON
	if err := json.Unmarshal([]byte(out), &installed); err != nil {
		t.Fatalf("decode install output %q: %v", out, err)
	}
	if len(installed) != 1 || installed[0].Version != "1.0.0" || installed[0].Error != "" {
		t.Fatalf("install result = %+v", installed)
	}
	if data, err := os.ReadFile(bin); err != nil || string(data) != "v1" {
		t.Fatalf("binary = %q, %v", data, err)
	}

	// list reports the installation.
	out, err = runCLI(t, "-c", cfg, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []listRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decode list %q: %v", out, err)
	}
	if len(rows) != 1 || !rows[0].Installed || rows[0].CurrentVersion != "1.0.0" {
		t.Fatalf("list = %+v", rows)
	}

	// A new release is picked up by update.
	gh.setVersion("v1.1.0", "1.1.0", "v2")
	out, err = runCLI(t, "-c", cfg, "update", "--yes", "--json")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(bin); err != nil || string(data) != "v2" {
		t.Fatalf("upgraded binary = %q, %v", data, err)
	}

	// pin excludes it from checks.
	if out, err := runCLI(t, "-c", cfg, "pin", "acme/tool"); err != nil {
		t.Fatalf("pin: %v %s", err, out)
	}
	out, err = runCLI(t, "-c", cfg, "check", "--json")
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	var checks []checkJSON
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("decode check %q: %v", out, err)
	}
	if len(checks) != 1 || !checks[0].Pinned {
		t.Fatalf("check = %+v", checks)
	}
	if _, err := runCLI(t, "-c", cfg, "pin", "https://github.com/acme/tool", "--unpin"); err != nil {
		t.Fatal(err)
	}

	// Roll back to the previous version.
	if out, err := runCLI(t, "-c", cfg, "rollback", "acme/tool"); err != nil {
		t.Fatalf("rollback: %v %s", err, out)
	}
	if data, err := os.ReadFile(bin); err != nil || string(data) != "v1" {
		t.Fatalf("rolled back binary = %q, %v", data, err)
	}

	// Remove deletes the symlink and state.
	if out, err := runCLI(t, "-c", cfg, "remove", "https://github.com/acme/tool", "--yes"); err != nil {
		t.Fatalf("remove: %v %s", err, out)
	}
	if _, err := os.Lstat(bin); !os.IsNotExist(err) {
		t.Errorf("symlink still present: %v", err)
	}
}

func TestCLIErrors(t *testing.T) {
	_, cfg, closer := newCLIEnv(t)
	defer closer()

	out, err := runCLI(t, "-c", cfg, "install", "missing")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("err = %v, out = %s", err, out)
	}

	out, err = runCLI(t, "-c", cfg, "remove", "nobody/none", "--yes")
	if err == nil || !strings.Contains(err.Error(), "no app configured") {
		t.Errorf("unknown repo remove: err = %v, out = %s", err, out)
	}

	out, err = runCLI(t, "-c", cfg, "install", "tool", "--arch", "sparc")
	if err == nil {
		t.Errorf("invalid architecture should fail, out = %s", out)
	}
}
