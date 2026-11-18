//go:build darwin

package installer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ghpm/internal/download"
)

// TestInstallDMG builds a real UDZO disk image with hdiutil and installs the
// app bundle inside it. It runs on the macOS CI runner.
func TestInstallDMG(t *testing.T) {
	if _, err := exec.LookPath("hdiutil"); err != nil {
		t.Skip("hdiutil not available")
	}
	src := t.TempDir()
	bundle := filepath.Join(src, "Motty.app")
	macosDir := filepath.Join(bundle, "Contents", "MacOS")
	if err := os.MkdirAll(macosDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macosDir, "motty"), []byte("dmg-binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("motty", filepath.Join(macosDir, "motty-current")); err != nil {
		t.Fatal(err)
	}

	dmgPath := filepath.Join(t.TempDir(), "motty.dmg")
	out, err := exec.Command("hdiutil", "create", "-quiet", "-volname", "Motty",
		"-srcfolder", src, "-ov", "-format", "UDZO", dmgPath).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create test dmg: %v %s", err, out)
	}
	data, err := os.ReadFile(dmgPath)
	if err != nil {
		t.Fatal(err)
	}

	srv, closeSrv := newAppServer(t, map[string][]byte{"/dmg": data})
	defer closeSrv()
	root := t.TempDir()
	fetcher, _ := download.New("")
	req := appRequest(root, srv.URL+"/dmg", "1.0.0")
	req.App.Install.Type = "" // inferred from the .dmg asset name
	req.AssetName = "motty-1.0.0-darwin-arm64.dmg"

	res, err := Install(context.Background(), fetcher, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != "app" || res.BinRel != "Motty.app" {
		t.Fatalf("dmg install = %+v", res)
	}
	exe := filepath.Join(res.BinPath, "Contents", "MacOS", "motty")
	if data, err := os.ReadFile(exe); err != nil || string(data) != "dmg-binary" {
		t.Errorf("copied executable = %q, %v", data, err)
	}
	if info, err := os.Stat(exe); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("executable mode = %v, %v", info, err)
	}
	link := filepath.Join(res.BinPath, "Contents", "MacOS", "motty-current")
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("bundle symlink was not preserved: %v %v", info, err)
	}
	target, err := os.Readlink(res.LinkPath)
	if err != nil || target != res.BinPath {
		t.Errorf("app link = %q, %v; want %q", target, err, res.BinPath)
	}
}
