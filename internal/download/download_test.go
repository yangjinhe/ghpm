package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindChecksum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	cases := []string{
		sum + "  tool.tar.gz\n",
		sum + " *tool.tar.gz\n",
		"SHA256 (tool.tar.gz) = " + sum + "\n",
		sum + "\n",
	}
	for _, text := range cases {
		got, ok := FindChecksum(text, "tool.tar.gz")
		if !ok || got != sum {
			t.Errorf("FindChecksum(%q) = %q, %v", text, got, ok)
		}
	}
	if _, ok := FindChecksum(sum+"  other.zip\n", "tool.tar.gz"); ok {
		t.Error("must not match a different file")
	}
}

func TestMirrorURL(t *testing.T) {
	raw := "https://github.com/a/b/releases/download/v1/x.tar.gz"
	if got := MirrorURL("", raw); got != raw {
		t.Errorf("empty prefix = %q", got)
	}
	if got := MirrorURL("https://ghfast.top", raw); got != "https://ghfast.top/"+raw {
		t.Errorf("join = %q", got)
	}
	if got := MirrorURL("https://m.example/{url}", raw); got != "https://m.example/"+raw {
		t.Errorf("template = %q", got)
	}
}

func TestFetchResumeAndVerify(t *testing.T) {
	content := bytes.Repeat([]byte("abcdefghij"), 10000)
	sumArr := sha256.Sum256(content)
	sum := hex.EncodeToString(sumArr[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "tool.bin", time.Now(), bytes.NewReader(content))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "tool.bin")
	// Simulate an interrupted download with the first half on disk.
	if err := os.WriteFile(dest+".part", content[:len(content)/2], 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	var last Progress
	got, err := f.Fetch(context.Background(), Options{
		URL: srv.URL, Dest: dest, ExpectedSHA256: sum,
		Progress: func(p Progress) { last = p },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != sum {
		t.Errorf("hash = %q want %q", got, sum)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("resumed content mismatch (%d bytes)", len(data))
	}
	if last.Total != int64(len(content)) || !last.Done {
		t.Errorf("final progress = %+v", last)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Error(".part should be renamed away")
	}
}

func TestFetchChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()
	f, _ := New("")
	dest := filepath.Join(t.TempDir(), "f.bin")
	wrong := strings.Repeat("00", 32)
	_, err := f.Fetch(context.Background(), Options{URL: srv.URL, Dest: dest, ExpectedSHA256: wrong})
	if err == nil {
		t.Fatal("want checksum error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("destination must not exist after a mismatch")
	}
	if _, statErr := os.Stat(dest + ".part"); !os.IsNotExist(statErr) {
		t.Error(".part must be removed after a mismatch")
	}
}

func TestFetchPrefixFallback(t *testing.T) {
	content := []byte("hello")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/broken") {
			http.Error(w, "nope", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(content)
	}))
	defer srv.Close()
	f, _ := New("")
	dest := filepath.Join(t.TempDir(), "f.bin")
	// The prefix produces a bad URL; Fetch must fall back to the direct URL.
	_, err := f.Fetch(context.Background(), Options{
		URL: srv.URL + "/file", Dest: dest, Prefix: srv.URL + "/broken",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, content) {
		t.Errorf("content = %q", got)
	}
}
