// Package config loads, validates and persists ghpm's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Paths holds every filesystem location ghpm uses.
type Paths struct {
	ConfigDir  string
	ConfigFile string
	DataDir    string
	CacheDir   string
	BinDir     string
	StateFile  string
	LogDir     string
}

// DefaultPaths computes XDG based paths, honouring GHPM_CONFIG.
func DefaultPaths() Paths {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(homeDir(), ".config")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(homeDir(), ".local", "share")
	}
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		cacheHome = filepath.Join(homeDir(), ".cache")
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(homeDir(), ".local", "state")
	}

	configDir := filepath.Join(configHome, "ghpm")
	configFile := filepath.Join(configDir, "config.yaml")
	if env := os.Getenv("GHPM_CONFIG"); env != "" {
		configFile = expandHome(env)
		configDir = filepath.Dir(configFile)
	}
	dataDir := filepath.Join(dataHome, "ghpm")
	if env := os.Getenv("GHPM_DATA_DIR"); env != "" {
		dataDir = expandHome(env)
	}
	binDir := filepath.Join(homeDir(), ".local", "bin")
	if env := os.Getenv("GHPM_BIN_DIR"); env != "" {
		binDir = expandHome(env)
	}
	return Paths{
		ConfigDir:  configDir,
		ConfigFile: configFile,
		DataDir:    dataDir,
		CacheDir:   filepath.Join(cacheHome, "ghpm"),
		BinDir:     binDir,
		StateFile:  filepath.Join(dataDir, "state.json"),
		LogDir:     filepath.Join(stateHome, "ghpm"),
	}
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// Config is the whole configuration document.
type Config struct {
	Version  int      `yaml:"version"`
	Defaults Defaults `yaml:"defaults"`
	Apps     []App    `yaml:"apps"`
	// Paths is resolved at load time and never serialised.
	Path   string `yaml:"-"`
	Source Paths  `yaml:"-"`
}

// Defaults are applied to every app unless the app overrides them.
type Defaults struct {
	BinDir      string `yaml:"bin_dir,omitempty"`
	DataDir     string `yaml:"data_dir,omitempty"`
	Verify      string `yaml:"verify,omitempty"`      // required | if-available | skip
	Prefer      string `yaml:"prefer,omitempty"`      // gnu | musl
	Prerelease  bool   `yaml:"prerelease,omitempty"`  // default channel for new apps
	Concurrency int    `yaml:"concurrency,omitempty"` // parallel downloads
	Proxy       string `yaml:"proxy,omitempty"`       // http(s) proxy URL
	PruneKeep   int    `yaml:"prune_keep,omitempty"`  // versions kept per app after upgrade
	GitHub      GitHub `yaml:"github,omitempty"`
}

// GitHub controls API and download endpoints.
type GitHub struct {
	TokenEnv       string `yaml:"token_env,omitempty"`       // env var holding the token
	APIBase        string `yaml:"api_base,omitempty"`        // default https://api.github.com
	DownloadPrefix string `yaml:"download_prefix,omitempty"` // e.g. https://ghfast.top
	CacheTTLMin    int    `yaml:"cache_ttl_min,omitempty"`   // release metadata cache in minutes
}

// Match describes how to find the right release asset.
type Match struct {
	Template  string            `yaml:"template,omitempty"`
	Regex     string            `yaml:"regex,omitempty"`
	Platforms map[string]string `yaml:",inline"`
}

// Install describes what to do with the downloaded asset.
type Install struct {
	Type        string   `yaml:"type,omitempty"` // binary | archive | deb | rpm | appimage | script
	Bin         string   `yaml:"bin,omitempty"`
	Link        string   `yaml:"link,omitempty"` // symlink name, defaults to bin/id
	Stripping   int      `yaml:"strip_components,omitempty"`
	Sudo        string   `yaml:"sudo,omitempty"` // never | prompt | always
	Args        []string `yaml:"args,omitempty"`
	AllowScript bool     `yaml:"allow_scripts,omitempty"`
}

// App is one managed GitHub project.
type App struct {
	ID          string  `yaml:"id"`
	Repo        string  `yaml:"repo"`
	Channel     string  `yaml:"channel,omitempty"` // latest | prerelease | tag:vX.Y.Z
	Description string  `yaml:"description,omitempty"`
	Match       Match   `yaml:"match"`
	Install     Install `yaml:"install,omitempty"`
	Verify      string  `yaml:"verify,omitempty"`
	Pinned      bool    `yaml:"pinned,omitempty"`
}

// VerifyMode returns the effective checksum policy for an app.
func (c *Config) VerifyMode(a App) string {
	if a.Verify != "" {
		return a.Verify
	}
	if c.Defaults.Verify != "" {
		return c.Defaults.Verify
	}
	return "if-available"
}

// Prerelease reports whether add should look at prereleases by default.
func (c *Config) Prerelease() bool { return c.Defaults.Prerelease }

// DefaultsConfig returns a Config populated with defaults for a fresh install.
func DefaultsConfig(paths Paths) *Config {
	cfg := &Config{
		Version: 1,
		Defaults: Defaults{
			BinDir:      paths.BinDir,
			DataDir:     paths.DataDir,
			Verify:      "if-available",
			Prefer:      "gnu",
			Concurrency: 4,
			PruneKeep:   3,
			GitHub: GitHub{
				TokenEnv:    "GITHUB_TOKEN",
				APIBase:     "https://api.github.com",
				CacheTTLMin: 30,
			},
		},
		Path:   paths.ConfigFile,
		Source: paths,
	}
	return cfg
}

// Load reads the config file, creating defaults when it does not exist.
func Load(paths Paths) (*Config, error) {
	cfg := DefaultsConfig(paths)
	data, err := os.ReadFile(paths.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		applyDefaults(cfg)
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", paths.ConfigFile, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", paths.ConfigFile, err)
	}
	cfg.Path = paths.ConfigFile
	cfg.Source = paths
	applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", paths.ConfigFile, err)
	}
	return cfg, nil
}

func applyDefaults(c *Config) {
	if c.Version == 0 {
		c.Version = 1
	}
	d := &c.Defaults
	if d.BinDir == "" {
		d.BinDir = c.Source.BinDir
	}
	if d.DataDir == "" {
		d.DataDir = c.Source.DataDir
	}
	if d.Verify == "" {
		d.Verify = "if-available"
	}
	if d.Concurrency <= 0 {
		d.Concurrency = 4
	}
	if d.PruneKeep <= 0 {
		d.PruneKeep = 3
	}
	if d.GitHub.TokenEnv == "" {
		d.GitHub.TokenEnv = "GITHUB_TOKEN"
	}
	if d.GitHub.APIBase == "" {
		d.GitHub.APIBase = "https://api.github.com"
	}
	if d.GitHub.CacheTTLMin <= 0 {
		d.GitHub.CacheTTLMin = 30
	}
	d.BinDir = expandHome(d.BinDir)
	d.DataDir = expandHome(d.DataDir)
}

// ExpandPaths fills unset per-app values.
func (c *Config) ExpandPaths() {
	c.Defaults.BinDir = expandHome(c.Defaults.BinDir)
	c.Defaults.DataDir = expandHome(c.Defaults.DataDir)
	if c.Defaults.BinDir == "" {
		c.Defaults.BinDir = c.Source.BinDir
	}
	if c.Defaults.DataDir == "" {
		c.Defaults.DataDir = c.Source.DataDir
	}
}

// Validate checks the fields that would otherwise fail deep inside an install.
func (c *Config) Validate() error {
	seen := map[string]bool{}
	for i, a := range c.Apps {
		if a.ID == "" {
			return fmt.Errorf("apps[%d]: id is required", i)
		}
		if !validID.MatchString(a.ID) {
			return fmt.Errorf("app %q: id may only contain letters, digits, '.', '_' and '-'", a.ID)
		}
		if seen[a.ID] {
			return fmt.Errorf("app %q: duplicate id", a.ID)
		}
		seen[a.ID] = true
		if _, err := ParseRepo(a.Repo); err != nil {
			return fmt.Errorf("app %q: %w", a.ID, err)
		}
		switch ch := a.Channel; {
		case ch == "", ch == "latest", ch == "prerelease":
		case strings.HasPrefix(ch, "tag:") && len(ch) > 4:
		default:
			return fmt.Errorf("app %q: channel must be latest, prerelease or tag:<tag>", a.ID)
		}
		if a.Match.Regex != "" {
			if _, err := regexp.Compile(a.Match.Regex); err != nil {
				return fmt.Errorf("app %q: invalid match.regex: %w", a.ID, err)
			}
		}
		switch a.Install.Type {
		case "", "binary", "archive", "deb", "rpm", "appimage", "script":
		default:
			return fmt.Errorf("app %q: unknown install.type %q", a.ID, a.Install.Type)
		}
		for _, k := range keysOf(a.Match.Platforms) {
			if _, err := ParsePlatformKey(k); err != nil {
				return fmt.Errorf("app %q: %w", a.ID, err)
			}
		}
	}
	return nil
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ParsePlatformKey validates "os/arch" as a match key, allowing "*".
func ParsePlatformKey(k string) (string, error) {
	parts := strings.Split(k, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid platform key %q (want os/arch)", k)
	}
	return strings.ToLower(parts[0]) + "/" + strings.ToLower(parts[1]), nil
}

// ParseRepo extracts owner/repo from the many ways a user may paste a link.
func ParseRepo(input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", errors.New("empty repository")
	}
	if strings.Contains(s, "gitlab.com") || strings.Contains(s, "bitbucket.org") {
		return "", fmt.Errorf("%q is not a github.com repository", s)
	}
	s = strings.TrimSuffix(s, "/")
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".git")
	// git@github.com:owner/repo
	if at := strings.Index(s, "@github.com:"); at >= 0 {
		s = s[at+len("@github.com:"):]
	}
	if i := strings.Index(s, "github.com/"); i >= 0 {
		s = s[i+len("github.com/"):]
	}
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	// Drop view suffixes and release paths.
	for _, sep := range []string{"/releases", "/tags", "/tree", "/blob", "/issues", "/wiki", "#"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("cannot parse %q as owner/repo", input)
	}
	owner, repo := parts[0], parts[1]
	valid := regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	if !valid.MatchString(owner) || !valid.MatchString(repo) {
		return "", fmt.Errorf("cannot parse %q as owner/repo", input)
	}
	return owner + "/" + repo, nil
}

// ExpandPath expands a leading ~ in a user supplied path.
func ExpandPath(p string) string { return expandHome(p) }

func expandHome(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

// Save writes the configuration atomically, creating its directory.
func (c *Config) Save() error {
	if c.Path == "" {
		c.Path = c.Source.ConfigFile
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	header := "# ghpm configuration - https://github.com/\n# Edit apps by hand or use `ghpm add <repo>`.\n"
	return writeAtomic(c.Path, append([]byte(header), data...), 0o644)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// AppByID returns the app with this id, or nil.
func (c *Config) AppByID(id string) *App {
	for i := range c.Apps {
		if c.Apps[i].ID == id {
			return &c.Apps[i]
		}
	}
	return nil
}

// CheckBinDir warns when the bin directory is not on PATH.
func (c *Config) CheckBinDir() bool {
	bin := c.Defaults.BinDir
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == bin {
			return true
		}
	}
	return false
}
