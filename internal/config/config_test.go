package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
