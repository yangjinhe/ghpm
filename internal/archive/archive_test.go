package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type file struct {
	name string
	mode int64
	body string
}

func makeTarGz(t *testing.T, files []file) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		hdr := &tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(f.name, "/") {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(f.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractAndFindBinary(t *testing.T) {
	src := makeTarGz(t, []file{
		{name: "pkg/", mode: 0o755},
		{name: "pkg/tool", mode: 0o755, body: "#!/bin/sh\n"},
		{name: "pkg/README.md", mode: 0o644, body: "docs"},
	})
	dest := t.TempDir()
	if _, err := Extract(src, dest, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "tool")); err != nil {
		t.Fatalf("strip_components failed: %v", err)
	}
	bin, err := FindBinary(dest, "tool")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(bin) != "tool" {
		t.Errorf("bin = %s", bin)
	}
	// Without a name the single executable is chosen.
	bin, err = FindBinary(dest, "")
	if err != nil || filepath.Base(bin) != "tool" {
		t.Errorf("auto find: %s %v", bin, err)
	}
	if _, err := FindBinary(dest, "missing"); err == nil {
		t.Error("want error for missing binary")
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	src := makeTarGz(t, []file{{name: "../escape.txt", mode: 0o644, body: "x"}})
	dest := t.TempDir()
	if _, err := Extract(src, dest, 0); err == nil {
		t.Fatal("want traversal error")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); !os.IsNotExist(err) {
		t.Error("file escaped the destination")
	}
}

func TestExtractRejectsAbsolute(t *testing.T) {
	src := makeTarGz(t, []file{{name: "/tmp/ghpm-absolute-test", mode: 0o644, body: "x"}})
	if _, err := Extract(src, t.TempDir(), 0); err == nil {
		t.Fatal("want absolute path error")
	}
}

func TestExtractZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "dir/tool", Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("binary")); err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Create("dir/readme.txt"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	files, err := Extract(path, dest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("extracted %v", files)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "dir", "tool")); err != nil || string(data) != "binary" {
		t.Errorf("zip content: %q %v", data, err)
	}
}

func TestEntryPath(t *testing.T) {
	cases := []struct {
		in    string
		strip int
		want  string
		skip  bool
	}{
		{"./a/b", 0, "a/b", false},
		{"a/b/c", 1, "b/c", false},
		{"a", 1, "", true},
		{"a/../b", 0, "a/../b", false}, // safeJoin rejects it later
		{"dir/", 0, "dir", false},
	}
	for _, c := range cases {
		got, skip := entryPath(c.in, c.strip)
		if got != c.want || skip != c.skip {
			t.Errorf("entryPath(%q,%d) = %q,%v want %q,%v", c.in, c.strip, got, skip, c.want, c.skip)
		}
	}
}
