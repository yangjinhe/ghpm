package platform

import "testing"

func TestNormalizeMacOSAliases(t *testing.T) {
	for _, in := range []string{"darwin", "macos", "macOS", "osx", "mac", "apple-darwin"} {
		if got := NormalizeOS(in); got != "darwin" {
			t.Errorf("NormalizeOS(%q) = %q", in, got)
		}
	}
	for _, in := range []string{"arm64", "aarch64", "arm64e"} {
		if got := NormalizeArch(in); got != "arm64" {
			t.Errorf("NormalizeArch(%q) = %q", in, got)
		}
	}
	for _, in := range []string{"x86_64", "x86_64h", "amd64"} {
		if got := NormalizeArch(in); got != "amd64" {
			t.Errorf("NormalizeArch(%q) = %q", in, got)
		}
	}
}

func TestDetectDarwinTokens(t *testing.T) {
	cases := map[string]string{
		"tool-1.0.0-x86_64-apple-darwin.tar.gz": "darwin",
		"tool_1.0.0_macOS_arm64.tar.gz":         "darwin",
		"tool-1.0.0-osx.zip":                    "darwin",
		"tool-mac-arm64.zip":                    "darwin",
		"tool_darwin_amd64":                     "darwin",
		"tool-linux-amd64.tar.gz":               "linux",
	}
	for name, want := range cases {
		if got := DetectOS(name); got != want {
			t.Errorf("DetectOS(%q) = %q, want %q", name, got, want)
		}
	}
	if got := DetectArches("tool_1.0.0_darwin_x86_64h.tar.gz"); len(got) != 1 || got[0] != "amd64" {
		t.Errorf("x86_64h arches = %v", got)
	}
	if got := DetectArches("tool_darwin_arm64e"); len(got) != 1 || got[0] != "arm64" {
		t.Errorf("arm64e arches = %v", got)
	}
}
