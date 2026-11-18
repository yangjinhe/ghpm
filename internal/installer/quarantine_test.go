package installer

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestStripQuarantineWalksNonDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bin", "nested/lib"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("bin", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	var seen []string
	removed, err := stripQuarantine(root, func(p string, symlink bool) error {
		seen = append(seen, filepath.Base(p))
		if filepath.Base(p) == "lib" {
			return errNoQuarantine
		}
		if symlink {
			t.Logf("%s came through as a symlink", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	sort.Strings(seen)
	want := []string{"bin", "lib", "link"}
	if len(seen) != len(want) {
		t.Fatalf("visited = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("visited = %v, want %v", seen, want)
		}
	}
}

func TestStripQuarantineReturnsFirstFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bin"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("operation not permitted")
	if _, err := stripQuarantine(root, func(string, bool) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("want %v, got %v", boom, err)
	}
}

func TestStripQuarantineMissingRoot(t *testing.T) {
	if _, err := stripQuarantine(filepath.Join(t.TempDir(), "missing"), func(string, bool) error {
		return nil
	}); err == nil {
		t.Fatal("missing root should report an error")
	}
}

func TestStripQuarantineNonDarwinIsNoop(t *testing.T) {
	if quarantineSupported {
		t.Skip("macOS clears attributes for real; see the darwin test")
	}
	removed, err := StripQuarantine(t.TempDir())
	if err != nil || removed != 0 {
		t.Fatalf("StripQuarantine = %d, %v; want 0, nil", removed, err)
	}
}
