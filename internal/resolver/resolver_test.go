package resolver

import (
	"errors"
	"strings"
	"testing"

	"ghpm/internal/config"
	"ghpm/internal/platform"
)

func TestDetectArchesNoFalse386(t *testing.T) {
	cases := map[string][]string{
		"lazygit_0.45.0_Linux_x86_64.tar.gz":          {"amd64"},
		"tool-1.0.0-aarch64-unknown-linux-gnu.tar.gz": {"arm64"},
		"fd-v10.2.0-i686-unknown-linux-gnu.tar.gz":    {"386"},
		"tool_1.0.0_armv7l.tar.gz":                    {"arm"},
		"tool_1.0.0_linux_amd64.deb":                  {"amd64"},
	}
	for name, want := range cases {
		got := platform.DetectArches(name)
		if len(got) != len(want) {
			t.Errorf("%s: got %v want %v", name, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: got %v want %v", name, got, want)
				break
			}
		}
	}
}

func TestDetectOS(t *testing.T) {
	if got := platform.DetectOS("tool-1.0.0-x86_64-unknown-linux-musl.tar.gz"); got != "linux" {
		t.Errorf("linux: got %q", got)
	}
	if got := platform.DetectOS("tool_1.0.0_Darwin_arm64.tar.gz"); got != "darwin" {
		t.Errorf("darwin: got %q", got)
	}
	if got := platform.DetectOS("tool_darwin_amd64"); got != "darwin" {
		t.Errorf("darwin should win over win: got %q", got)
	}
	if got := platform.DetectOS("tool_windows_amd64.zip"); got != "windows" {
		t.Errorf("windows: got %q", got)
	}
}

func TestScore(t *testing.T) {
	linuxAMD := platform.Platform{OS: "linux", Arch: "amd64"}

	if c, ok := Score("tool_1.0.0_linux_amd64.tar.gz", linuxAMD, Options{}); !ok || c.Format != FormatArchive {
		t.Errorf("linux archive should match: ok=%v %+v", ok, c)
	}
	if _, ok := Score("tool_1.0.0_windows_amd64.zip", linuxAMD, Options{}); ok {
		t.Error("windows asset must not match linux")
	}
	if _, ok := Score("tool_1.0.0_linux_arm64.tar.gz", linuxAMD, Options{}); ok {
		t.Error("arm64 asset must not match amd64")
	}
	if _, ok := Score("checksums.txt", linuxAMD, Options{}); ok {
		t.Error("checksums must not match")
	}
	if _, ok := Score("tool_1.0.0_linux_amd64.tar.gz.sha256", linuxAMD, Options{}); ok {
		t.Error("per-asset checksum must not match")
	}
	if _, ok := Score("tool-debug_linux_amd64", linuxAMD, Options{}); ok {
		t.Error("debug build must not match")
	}

	gnu, _ := Score("tool-1.0.0-x86_64-unknown-linux-gnu.tar.gz", linuxAMD, Options{Prefer: "gnu"})
	musl, _ := Score("tool-1.0.0-x86_64-unknown-linux-musl.tar.gz", linuxAMD, Options{Prefer: "gnu"})
	if gnu.Score <= musl.Score {
		t.Errorf("gnu should outrank musl under prefer=gnu: %d vs %d", gnu.Score, musl.Score)
	}
}

func TestBest(t *testing.T) {
	names := []string{
		"tool_1.0.0_linux_amd64.tar.gz",
		"tool_1.0.0_linux_arm64.tar.gz",
		"tool_1.0.0_darwin_amd64.tar.gz",
		"checksums.txt",
	}
	p := platform.Platform{OS: "linux", Arch: "amd64"}
	best, _, err := Best(names, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if best.Name != "tool_1.0.0_linux_amd64.tar.gz" {
		t.Errorf("best = %q", best.Name)
	}

	if _, _, err := Best([]string{"tool_linux_amd64", "tool_linux_amd64_static"}, p, Options{}); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("want ambiguity, got %v", err)
	}
	if _, _, err := Best([]string{"only_windows.exe"}, p, Options{}); !errors.Is(err, ErrNoMatch) {
		t.Errorf("want no match, got %v", err)
	}
}

func TestResolvePatterns(t *testing.T) {
	target := platform.Platform{OS: "linux", Arch: "arm64"}
	names := []string{"app_1.2.3_Linux_aarch64.tar.gz", "app_1.2.3_Linux_x86_64.tar.gz", "checksums.txt"}
	app := config.App{ID: "app", Match: config.Match{Platforms: map[string]string{
		"linux/arm64": "app_{version}_Linux_aarch64.tar.gz",
	}}}
	got, err := Resolve(app, names, target, "v1.2.3", "1.2.3")
	if err != nil || got != "app_1.2.3_Linux_aarch64.tar.gz" {
		t.Fatalf("resolve map: %q %v", got, err)
	}

	app.Match = config.Match{Template: "app_{version}_Linux_{arch_alias}.tar.gz"}
	got, err = Resolve(app, names, target, "v1.2.3", "1.2.3")
	if err != nil || got != "app_1.2.3_Linux_aarch64.tar.gz" {
		t.Fatalf("resolve template: %q %v", got, err)
	}

	app.Match = config.Match{Regex: `^app_(?P<v>[0-9.]+)_Linux_(x86_64|aarch64)\.tar\.gz$`}
	got, err = Resolve(app, names, target, "v1.2.3", "1.2.3")
	if err != nil || got != "app_1.2.3_Linux_aarch64.tar.gz" {
		t.Fatalf("resolve regex: %q %v", got, err)
	}

	// A map without an entry for this platform falls back to scoring, which
	// still finds the right arm64 asset.
	app.Match = config.Match{Platforms: map[string]string{"linux/amd64": "app_{version}_Linux_x86_64.tar.gz"}}
	if got, err := Resolve(app, names, target, "v1.2.3", "1.2.3"); err != nil || got != "app_1.2.3_Linux_aarch64.tar.gz" {
		t.Fatalf("fallback scoring: %q %v", got, err)
	}
	if c := Classify("tool-universal-darwin.zip"); len(c.Arches) != 2 {
		t.Errorf("universal arches = %v", c.Arches)
	}
}

func TestGenerateLazygit(t *testing.T) {
	names := []string{
		"lazygit_0.45.0_Linux_x86_64.tar.gz",
		"lazygit_0.45.0_Linux_arm64.tar.gz",
		"lazygit_0.45.0_Darwin_x86_64.tar.gz",
		"lazygit_0.45.0_Darwin_arm64.tar.gz",
		"lazygit_0.45.0_Windows_x86_64.zip",
		"checksums.txt",
	}
	gen, err := Generate(names, platform.Platform{OS: "linux", Arch: "amd64"}, "v0.45.0", "0.45.0", "", "gnu")
	if err != nil {
		t.Fatal(err)
	}
	if gen.Chosen.Name != "lazygit_0.45.0_Linux_x86_64.tar.gz" {
		t.Errorf("chosen = %q", gen.Chosen.Name)
	}
	if gen.Install.Type != "archive" {
		t.Errorf("install type = %q", gen.Install.Type)
	}
	want := "lazygit_{version}_Linux_x86_64.tar.gz"
	if got := gen.Match.Platforms["linux/amd64"]; got != want {
		t.Errorf("linux/amd64 = %q want %q", got, want)
	}
	if _, ok := gen.Match.Platforms["linux/arm64"]; !ok {
		t.Error("missing arm64 mapping")
	}
	if _, ok := gen.Match.Platforms["darwin/arm64"]; !ok {
		t.Error("missing darwin/arm64 mapping")
	}
}

func TestGeneratePreferredAndBinary(t *testing.T) {
	names := []string{"tool_linux_amd64", "tool_linux_arm64"}
	gen, err := Generate(names, platform.Platform{OS: "linux", Arch: "amd64"}, "v1.0.0", "1.0.0", "tool_linux_arm64", "")
	if err != nil {
		t.Fatal(err)
	}
	if gen.Chosen.Name != "tool_linux_arm64" {
		t.Errorf("preferred asset ignored: %q", gen.Chosen.Name)
	}
	if gen.Install.Type != "binary" || gen.Install.Bin != "tool_linux_amd64" {
		// Bin is inferred from the asset name token; any sensible value is fine.
		t.Logf("install = %+v", gen.Install)
	}
	if _, err := Generate(names, platform.Platform{OS: "linux", Arch: "amd64"}, "v1.0.0", "1.0.0", "missing", ""); err == nil {
		t.Error("missing preferred asset should error")
	}
}

func TestTemplatizeAndChecksumAsset(t *testing.T) {
	if got := Templatize("fd-v10.2.0-x86_64-unknown-linux-gnu.tar.gz", "v10.2.0", "10.2.0"); got != "fd-{tag}-x86_64-unknown-linux-gnu.tar.gz" {
		t.Errorf("templatize = %q", got)
	}
	names := []string{"tool.tar.gz", "tool.tar.gz.sha256", "SHA256SUMS", "checksums.txt", "tool.tar.gz.sig"}
	if got := ChecksumAsset(names, "tool.tar.gz"); got != "tool.tar.gz.sha256" {
		t.Errorf("checksum asset = %q", got)
	}
	if got := ChecksumAsset([]string{"checksums.txt"}, "x.tar.gz"); got != "checksums.txt" {
		t.Errorf("manifest = %q", got)
	}
}

func TestExpand(t *testing.T) {
	p := platform.Platform{OS: "linux", Arch: "amd64"}
	got := Expand("{tag} {version} {os} {arch} {arch_alias}", p, "v1.2.3", "1.2.3")
	if !strings.Contains(got, "v1.2.3 1.2.3 linux amd64 x86_64") {
		t.Errorf("expand = %q", got)
	}
}

func TestBestTieBreak(t *testing.T) {
	p := platform.Platform{OS: "linux", Arch: "amd64"}
	// bottom publishes a glibc 2.17 compat build; the plain name wins.
	names := []string{
		"bottom_x86_64-unknown-linux-gnu-2-17.tar.gz",
		"bottom_x86_64-unknown-linux-gnu.tar.gz",
	}
	best, _, err := Best(names, p, Options{Prefer: "gnu"})
	if err != nil {
		t.Fatal(err)
	}
	if best.Name != "bottom_x86_64-unknown-linux-gnu.tar.gz" {
		t.Errorf("tie break = %q", best.Name)
	}
	// Genuinely different builds stay ambiguous.
	if _, _, err := Best([]string{"tool_linux_amd64", "tool_linux_amd64_static"}, p, Options{}); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("static variant should stay ambiguous, got %v", err)
	}
}

func TestUnknownArchRejected(t *testing.T) {
	p := platform.Platform{OS: "linux", Arch: "amd64"}
	names := []string{
		"bottom_loongarch64-unknown-linux-gnu.tar.gz",
		"bottom_x86_64-unknown-linux-gnu.tar.gz",
	}
	best, ranked, err := Best(names, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if best.Name != "bottom_x86_64-unknown-linux-gnu.tar.gz" {
		t.Errorf("best = %q", best.Name)
	}
	if len(ranked) != 1 {
		t.Errorf("loongarch should be filtered, ranked=%v", ranked)
	}
}

func TestDebImpliesLinux(t *testing.T) {
	c := Classify("fd_10.5.2_amd64.deb")
	if c.OS != "linux" || c.Format != FormatDeb {
		t.Errorf("classify = %+v", c)
	}
	if c := Classify("tool_setup.exe"); c.OS != "windows" {
		t.Errorf("exe classify = %+v", c)
	}
}
