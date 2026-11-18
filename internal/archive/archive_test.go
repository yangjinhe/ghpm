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
	link string
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
		if f.link != "" {
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = f.link
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

func TestFindBinaryCfstStyleArchive(t *testing.T) {
	// Upstream tars often lose the execute bit and ship helper scripts.
	src := makeTarGz(t, []file{
		{name: "cfst_linux_amd64/cfst", mode: 0o666, body: "ELF..."},
		{name: "cfst_linux_amd64/cfst_hosts.sh", mode: 0o666, body: "#!/bin/sh"},
		{name: "cfst_linux_amd64/ip.txt", mode: 0o644, body: "1.1.1.1"},
		{name: "cfst_linux_amd64/使用说明.txt", mode: 0o644, body: "docs"},
	})
	dest := t.TempDir()
	if _, err := Extract(src, dest, 0); err != nil {
		t.Fatal(err)
	}
	bin, err := FindBinary(dest, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(bin) != "cfst" {
		t.Errorf("picked %q, want cfst", bin)
	}
	if info, err := os.Stat(bin); err != nil || info.Mode().Perm()&0o111 == 0 {
		// Extraction keeps the stored mode; the installer chmods the chosen
		// binary. Selection must still work before that.
		t.Logf("mode before installer chmod: %v", info.Mode())
	}
}

func TestFindBinaryUsesHints(t *testing.T) {
	src := makeTarGz(t, []file{
		{name: "pkg/tool", mode: 0o755, body: "a"},
		{name: "pkg/helper", mode: 0o755, body: "b"},
	})
	dest := t.TempDir()
	if _, err := Extract(src, dest, 0); err != nil {
		t.Fatal(err)
	}
	bin, err := FindBinary(dest, "", "tool", "tool_1.0.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(bin) != "tool" {
		t.Errorf("picked %q, want tool", bin)
	}
}

func TestMacMetadataIgnored(t *testing.T) {
	src := makeTarGz(t, []file{
		{name: "tool", mode: 0o755, body: "#!/bin/sh\n"},
		{name: "__MACOSX/._tool", mode: 0o644, body: "appledouble"},
		{name: "._tool", mode: 0o644, body: "appledouble"},
		{name: ".DS_Store", mode: 0o644, body: "finder"},
		{name: "README.md", mode: 0o644, body: "docs"},
	})
	dest := t.TempDir()
	if _, err := Extract(src, dest, 0); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"__MACOSX/._tool", "._tool", ".DS_Store"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Errorf("%s should not be extracted (err=%v)", p, err)
		}
	}
	bin, err := FindBinary(dest, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(bin) != "tool" {
		t.Errorf("picked %q, want tool", bin)
	}
}

func TestMacMetadataInZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []file{
		{name: "tool", mode: 0o755, body: "bin"},
		{name: "__MACOSX/._tool", mode: 0o644, body: "meta"},
	} {
		hdr := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		hdr.SetMode(os.FileMode(f.mode))
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
	path := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := Extract(path, dest, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "__MACOSX")); !os.IsNotExist(err) {
		t.Errorf("__MACOSX should not be extracted: %v", err)
	}
	bin, err := FindBinary(dest, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(bin) != "tool" {
		t.Errorf("picked %q, want tool", bin)
	}
}

func TestFindAppBundle(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "Motty.app")
	if err := os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "MacOS", "motty"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok, err := FindAppBundle(root)
	if err != nil || !ok {
		t.Fatalf("FindAppBundle = %q, %v, %v", got, ok, err)
	}
	if got != bundle {
		t.Errorf("bundle = %q, want %q", got, bundle)
	}

	if _, ok, err := FindAppBundle(t.TempDir()); err != nil || ok {
		t.Errorf("empty tree should have no bundle: ok=%v err=%v", ok, err)
	}

	// A directory without Contents/Info.plist is not a bundle.
	fake := filepath.Join(root, "Fake.app", "Contents", "MacOS")
	if err := os.MkdirAll(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := FindAppBundle(root); !ok {
		t.Error("existing bundle should still be found")
	}
}

func TestExtractSymlinks(t *testing.T) {
	src := makeTarGz(t, []file{
		{name: "dir/", mode: 0o755},
		{name: "dir/tool", mode: 0o755, body: "binary"},
		{name: "link", mode: 0o777, link: "dir/tool"},
		{name: "dir/alias", mode: 0o777, link: "tool"},
	})
	dest := t.TempDir()
	if _, err := Extract(src, dest, 0); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"link", "dir/alias"} {
		info, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink", p)
		}
	}
	data, err := os.ReadFile(filepath.Join(dest, "link"))
	if err != nil || string(data) != "binary" {
		t.Errorf("link content = %q, %v", data, err)
	}
}

func TestExtractRejectsUnsafeSymlinks(t *testing.T) {
	cases := []struct {
		name  string
		files []file
		want  string
	}{
		{
			name:  "absolute",
			files: []file{{name: "link", mode: 0o777, link: "/etc/passwd"}},
			want:  "absolute target",
		},
		{
			name:  "escape",
			files: []file{{name: "link", mode: 0o777, link: "../../etc/passwd"}},
			want:  "escapes destination",
		},
		{
			name: "write through symlink",
			files: []file{
				{name: "dir", mode: 0o777, link: "."},
				{name: "dir/evil", mode: 0o644, body: "x"},
			},
			want: "writes through symlink",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := makeTarGz(t, c.files)
			dest := t.TempDir()
			_, err := Extract(src, dest, 0)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
