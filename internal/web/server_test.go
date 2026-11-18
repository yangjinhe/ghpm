package web

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ghpm/internal/app"
	"ghpm/internal/config"
)

func tarGz(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
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

// testEnv builds a service backed by an in-process GitHub impostor.
func testEnv(t *testing.T) (*app.Service, config.Paths, func()) {
	t.Helper()
	archive := tarGz(t, "tool", "v1")
	release := map[string]any{
		"tag_name": "v1.0.0", "name": "v1.0.0", "draft": false, "prerelease": false,
		"assets": []map[string]any{{
			"id": 1, "name": "tool_1.0.0_linux_amd64.tar.gz",
			"size": len(archive), "browser_download_url": "", "url": "",
		}},
	}
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/acme/tool/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(release)
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".tar.gz") {
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	release["assets"].([]map[string]any)[0]["browser_download_url"] = base + "/download/tool_1.0.0_linux_amd64.tar.gz"
	release["assets"].([]map[string]any)[0]["url"] = base + "/assets/tool_1.0.0_linux_amd64.tar.gz"

	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	paths := config.DefaultPaths()
	paths.ConfigFile = filepath.Join(dir, "config.yaml")
	paths.ConfigDir = dir
	paths.DataDir = filepath.Join(dir, "data")
	paths.BinDir = filepath.Join(dir, "bin")
	cfg := config.DefaultsConfig(paths)
	cfg.Path = paths.ConfigFile
	cfg.Source = paths
	cfg.Defaults.DataDir = paths.DataDir
	cfg.Defaults.BinDir = paths.BinDir
	cfg.Defaults.GitHub.APIBase = base
	cfg.Defaults.Verify = "skip"
	cfg.Defaults.BinDir = paths.BinDir
	cfg.Apps = []config.App{{
		ID: "tool", Repo: "acme/tool", Channel: "latest",
		Match: config.Match{Platforms: map[string]string{
			runtime.GOOS + "/" + runtime.GOARCH: "tool_{version}_linux_amd64.tar.gz",
		}},
		Install: config.Install{Type: "archive", Bin: "tool", Link: "tool"},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	svc, err := app.New(app.Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	return svc, paths, srv.Close
}

func TestAppsAndStatic(t *testing.T) {
	svc, _, closer := testEnv(t)
	defer closer()
	srv := httptest.NewServer(New(svc).Handler(false))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/apps")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apps status = %d", resp.StatusCode)
	}
	var body appsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Apps) != 1 || body.Apps[0].ID != "tool" || body.Apps[0].Status != "not installed" {
		t.Errorf("apps = %+v", body.Apps)
	}
	if body.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("platform = %q", body.Platform)
	}

	resp, err = http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(page, []byte("ghpm")) {
		t.Errorf("index status = %d, %d bytes", resp.StatusCode, len(page))
	}
	if ct := resp.Header.Get("Content-Security-Policy"); ct == "" {
		t.Error("missing CSP header")
	}
}

func TestInstallJobAndSSE(t *testing.T) {
	svc, paths, closer := testEnv(t)
	defer closer()
	ts := httptest.NewServer(New(svc).Handler(false))
	defer ts.Close()

	payload := strings.NewReader(`{"ids":["tool"]}`)
	resp, err := http.Post(ts.URL+"/api/install", "application/json", payload)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, b)
	}
	var job jobView
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}

	// Follow the job over SSE until the terminal event.
	stream, err := http.Get(ts.URL + "/api/jobs/" + job.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if got := stream.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type = %q", got)
	}
	deadline := time.Now().Add(10 * time.Second)
	scanner := bufio.NewScanner(stream.Body)
	sawDone := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var msg sseMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &msg); err != nil {
			t.Fatalf("bad SSE payload: %v", err)
		}
		if msg.Type == "done" {
			sawDone = true
			if msg.Job.Status != "done" || msg.Job.Error != "" {
				t.Errorf("job = %+v", msg.Job)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SSE stream timed out")
		}
	}
	if !sawDone {
		t.Fatal("no done event before the stream ended")
	}

	data, err := os.ReadFile(filepath.Join(paths.BinDir, "tool"))
	if err != nil || string(data) != "v1" {
		t.Fatalf("installed binary = %q, err = %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(paths.BinDir, "tool")); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityGuards(t *testing.T) {
	svc, _, closer := testEnv(t)
	defer closer()
	handler := New(svc).Handler(false)

	req := httptest.NewRequest(http.MethodGet, "http://evil.example/api/apps", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-loopback host status = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/check", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain POST status = %d, want 415", rec.Code)
	}

	// A loopback Host with JSON is accepted.
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/check", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("json POST status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
}

func TestUnknownJob(t *testing.T) {
	svc, _, closer := testEnv(t)
	defer closer()
	ts := httptest.NewServer(New(svc).Handler(false))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/jobs/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestInvalidJSON(t *testing.T) {
	svc, _, closer := testEnv(t)
	defer closer()
	handler := New(svc).Handler(false)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/install", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHiddenElementsStayHidden(t *testing.T) {
	// Regression test: .drawer sets display:flex, which used to override the
	// user-agent [hidden] rule and made the close button look broken.
	css, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "[hidden]") || !strings.Contains(string(css), "display: none !important") {
		t.Error("style.css must force [hidden] to display:none")
	}
	html, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `id="drawer"`) || !strings.Contains(string(html), "hidden") {
		t.Error("drawer markup should start hidden")
	}
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), `$("#drawer").hidden = true`) {
		t.Error("drawer close handler must set hidden")
	}
	if !strings.Contains(string(js), "removeApp(a.id, false)") {
		t.Error("row remove action must keep the config entry")
	}
}

func TestRemoveEndpointDeletesConfig(t *testing.T) {
	svc, paths, closer := testEnv(t)
	defer closer()
	ts := httptest.NewServer(New(svc).Handler(false))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/remove",
		strings.NewReader(`{"id":"tool","purge":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	data, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "id: tool") {
		t.Errorf("config still lists the removed app:\n%s", data)
	}

	appsResp, err := http.Get(ts.URL + "/api/apps")
	if err != nil {
		t.Fatal(err)
	}
	defer appsResp.Body.Close()
	var body appsResponse
	if err := json.NewDecoder(appsResp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Apps) != 0 {
		t.Errorf("apps after remove = %+v", body.Apps)
	}
}
