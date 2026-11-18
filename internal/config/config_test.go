package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ghpm/internal/signature"
)

func TestParseRepo(t *testing.T) {
	cases := map[string]string{
		"jesseduffield/lazygit":                           "jesseduffield/lazygit",
		"https://github.com/sharkdp/fd":                   "sharkdp/fd",
		"https://github.com/sharkdp/fd/":                  "sharkdp/fd",
		"https://github.com/sharkdp/fd/releases/latest":   "sharkdp/fd",
		"http://github.com/BurntSushi/ripgrep?tab=readme": "BurntSushi/ripgrep",
		"git@github.com:cli/cli.git":                      "cli/cli",
		"github.com/derailed/k9s":                         "derailed/k9s",
		"https://gitlab.com/foo/bar":                      "",
	}
	for in, want := range cases {
		got, err := ParseRepo(in)
		if want == "" {
			if err == nil {
				t.Errorf("ParseRepo(%q) = %q, want error", in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRepo(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	paths := DefaultPaths()
	paths.ConfigFile = filepath.Join(dir, "config.yaml")
	paths.ConfigDir = dir
	paths.DataDir = filepath.Join(dir, "data")
	paths.BinDir = filepath.Join(dir, "bin")

	cfg := DefaultsConfig(paths)
	cfg.Apps = []App{{
		ID:      "lazygit",
		Repo:    "jesseduffield/lazygit",
		Channel: "latest",
		Match: Match{Platforms: map[string]string{
			"linux/amd64": "lazygit_{version}_Linux_x86_64.tar.gz",
			"linux/arm64": "lazygit_{version}_Linux_arm64.tar.gz",
		}},
		Install: Install{Type: "archive", Bin: "lazygit"},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Apps) != 1 {
		t.Fatalf("got %d apps", len(loaded.Apps))
	}
	a := loaded.Apps[0]
	if a.ID != "lazygit" || a.Repo != "jesseduffield/lazygit" {
		t.Errorf("unexpected app: %+v", a)
	}
	if got := a.Match.Platforms["linux/amd64"]; got != "lazygit_{version}_Linux_x86_64.tar.gz" {
		t.Errorf("inline match map lost: %q", got)
	}
	if a.Install.Type != "archive" || a.Install.Bin != "lazygit" {
		t.Errorf("install section lost: %+v", a.Install)
	}
	if loaded.Defaults.GitHub.APIBase != "https://api.github.com" {
		t.Errorf("defaults not applied: %+v", loaded.Defaults.GitHub)
	}
}

func TestValidate(t *testing.T) {
	cfg := DefaultsConfig(DefaultPaths())
	cfg.Apps = []App{
		{ID: "dup", Repo: "a/b"},
		{ID: "dup", Repo: "c/d"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
	cfg.Apps = []App{{ID: "bad id", Repo: "a/b"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("want invalid id error")
	}
	cfg.Apps = []App{{ID: "ok", Repo: "a/b", Channel: "nightly"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("want invalid channel error")
	}
}

func TestLoadMissingCreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	paths := DefaultPaths()
	paths.ConfigFile = filepath.Join(dir, "nested", "config.yaml")
	cfg, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Concurrency != 4 || cfg.Defaults.Verify != "if-available" {
		t.Errorf("unexpected defaults: %+v", cfg.Defaults)
	}
	if _, err := os.Stat(paths.ConfigFile); !os.IsNotExist(err) {
		t.Error("Load must not create the file")
	}
}

func TestSignatureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	paths := DefaultPaths()
	paths.ConfigFile = filepath.Join(dir, "config.yaml")
	cfg := DefaultsConfig(paths)
	cfg.Apps = []App{
		{ID: "signed", Repo: "acme/signed", Signature: signature.Config{Type: "minisign", Key: "RWQtest", Mode: "required"}},
		{ID: "plain", Repo: "acme/plain"},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "signature:") != 1 {
		t.Errorf("empty signature blocks must be omitted:\n%s", data)
	}
	loaded, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.AppByID("signed")
	if got == nil || got.Signature.Type != "minisign" || got.Signature.Key != "RWQtest" || !got.Signature.Enabled() {
		t.Fatalf("signature not reloaded: %+v", got)
	}
	if plain := loaded.AppByID("plain"); plain == nil || plain.Signature.Configured() {
		t.Fatalf("plain app gained a signature block: %+v", plain)
	}
}

func TestSignatureValidation(t *testing.T) {
	cfg := DefaultsConfig(DefaultPaths())
	cfg.Apps = []App{{ID: "bad", Repo: "a/b", Signature: signature.Config{Type: "cosign"}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "signature.type") {
		t.Fatalf("want signature.type error, got %v", err)
	}
	cfg.Apps = []App{{ID: "skip", Repo: "a/b", Verify: "skip", Signature: signature.Config{Mode: "if-available"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("signature + verify: skip must be rejected")
	}
}

func TestDefaultPathsPerOS(t *testing.T) {
	emptyEnv := func(string) string { return "" }
	home := "/Users/alice"
	darwin := defaultPaths("darwin", home, emptyEnv)
	if darwin.ConfigFile != "/Users/alice/Library/Application Support/ghpm/config.yaml" {
		t.Errorf("darwin config = %q", darwin.ConfigFile)
	}
	if darwin.DataDir != "/Users/alice/Library/Application Support/ghpm" {
		t.Errorf("darwin data = %q", darwin.DataDir)
	}
	if darwin.CacheDir != "/Users/alice/Library/Caches/ghpm" {
		t.Errorf("darwin cache = %q", darwin.CacheDir)
	}
	if darwin.LogDir != "/Users/alice/Library/Logs/ghpm" {
		t.Errorf("darwin log = %q", darwin.LogDir)
	}
	if darwin.BinDir != "/Users/alice/.local/bin" {
		t.Errorf("darwin bin = %q", darwin.BinDir)
	}

	linux := defaultPaths("linux", "/home/alice", emptyEnv)
	if linux.ConfigFile != "/home/alice/.config/ghpm/config.yaml" || linux.CacheDir != "/home/alice/.cache/ghpm" {
		t.Errorf("linux paths = %+v", linux)
	}

	// Explicit XDG variables win on macOS too.
	xdg := defaultPaths("darwin", home, func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return "/tmp/xdg-config"
		}
		return ""
	})
	if xdg.ConfigFile != "/tmp/xdg-config/ghpm/config.yaml" {
		t.Errorf("XDG override = %q", xdg.ConfigFile)
	}
}

func TestPkgInstallTypeAccepted(t *testing.T) {
	cfg := DefaultsConfig(DefaultPaths())
	cfg.Apps = []App{{ID: "pkged", Repo: "a/b", Install: Install{Type: "pkg"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("pkg install type rejected: %v", err)
	}
	cfg.Apps[0].Install.Type = "dmg"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dmg install type rejected: %v", err)
	}
}

func TestAppsDirDefaults(t *testing.T) {
	emptyEnv := func(string) string { return "" }
	darwin := defaultPaths("darwin", "/Users/alice", emptyEnv)
	if darwin.AppsDir != "/Applications" {
		t.Errorf("darwin apps dir = %q", darwin.AppsDir)
	}
	linux := defaultPaths("linux", "/home/alice", emptyEnv)
	if linux.AppsDir != "" {
		t.Errorf("linux apps dir = %q, want empty", linux.AppsDir)
	}
	override := defaultPaths("darwin", "/Users/alice", func(k string) string {
		if k == "GHPM_APPS_DIR" {
			return "/Volumes/External/Applications"
		}
		return ""
	})
	if override.AppsDir != "/Volumes/External/Applications" {
		t.Errorf("GHPM_APPS_DIR override = %q", override.AppsDir)
	}

	cfg := DefaultsConfig(darwin)
	if cfg.Defaults.AppsDir != "/Applications" {
		t.Errorf("defaults config apps dir = %q", cfg.Defaults.AppsDir)
	}
}

func TestAppInstallTypeAccepted(t *testing.T) {
	cfg := DefaultsConfig(DefaultPaths())
	cfg.Apps = []App{{ID: "gui", Repo: "a/b", Install: Install{Type: "app"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("app install type rejected: %v", err)
	}
}
