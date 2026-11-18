//go:build darwin

package installer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStripQuarantineDarwin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(bin, quarantineAttr, []byte("0081;00000000;ghpm-test;"), 0); err != nil {
		t.Skipf("cannot set quarantine attribute here: %v", err)
	}

	removed, err := StripQuarantine(dir)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	buf := make([]byte, 256)
	if _, err := unix.Getxattr(bin, quarantineAttr, buf); !errors.Is(err, unix.ENOATTR) {
		t.Fatalf("quarantine attribute still present: %v", err)
	}

	// A second pass must be quiet: missing attributes are not failures.
	removed, err = StripQuarantine(dir)
	if err != nil || removed != 0 {
		t.Fatalf("second strip = %d, %v; want 0, nil", removed, err)
	}
}
