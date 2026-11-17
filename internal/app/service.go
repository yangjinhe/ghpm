// Package app orchestrates ghpm's high level operations: add, install, check,
// update, rollback and remove. Both the CLI and TUI drive this package.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	semver "github.com/hashicorp/go-version"

	"ghpm/internal/archive"
	"ghpm/internal/config"
	"ghpm/internal/download"
	"ghpm/internal/gh"
	"ghpm/internal/installer"
	"ghpm/internal/platform"
	"ghpm/internal/resolver"
	"ghpm/internal/state"
	"ghpm/internal/version"
)

// Options configures a Service.
type Options struct {
	ConfigPath string
	Proxy      string
	NoCache    bool
}

// Service ties configuration, GitHub, downloads, installation and state
// together.
type Service struct {
	Cfg      *config.Config
	Paths    config.Paths
	DataDir  string
	BinDir   string
	CacheDir string
	GH       *gh.Client
	Fetcher  *download.Fetcher
	Store    *state.Store
}

// New opens the configuration and prepares all dependencies.
func New(opts Options) (*Service, error) {
	paths := config.DefaultPaths()
	if opts.ConfigPath != "" {
		paths.ConfigFile = config.ExpandPath(opts.ConfigPath)
		paths.ConfigDir = filepath.Dir(paths.ConfigFile)
	}
	cfg, err := config.Load(paths)
	if err != nil {
		return nil, err
	}
	cfg.ExpandPaths()

	dataDir := cfg.Defaults.DataDir
	if dataDir == "" {
		dataDir = paths.DataDir
	}
	binDir := cfg.Defaults.BinDir
	if binDir == "" {
		binDir = paths.BinDir
	}
	paths.DataDir = dataDir
	paths.BinDir = binDir
	paths.StateFile = filepath.Join(dataDir, "state.json")

	proxy := cfg.Defaults.Proxy
	if opts.Proxy != "" {
		proxy = opts.Proxy
	}
	ttl := time.Duration(cfg.Defaults.GitHub.CacheTTLMin) * time.Minute
	ghClient, err := gh.New(cfg.Defaults.GitHub, proxy, paths.CacheDir, ttl)
	if err != nil {
		return nil, err
	}
	if opts.NoCache {
		ghClient.BypassCache = true
	}
	gh.UserAgent = "ghpm/" + version.String()
	fetcher, err := download.New(proxy)
	if err != nil {
		return nil, err
	}
	store, err := state.Open(paths.StateFile)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{dataDir, paths.CacheDir, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	return &Service{
		Cfg: cfg, Paths: paths, DataDir: dataDir, BinDir: binDir,
		CacheDir: paths.CacheDir, GH: ghClient, Fetcher: fetcher, Store: store,
	}, nil
}

// SaveConfig persists the configuration.
func (s *Service) SaveConfig() error { return s.Cfg.Save() }

// Event is a progress notification used by the TUI and CLI spinners.
type Event struct {
	Kind     string // start, resolve, download, install, done, error, check
	ID       string
	Message  string
	Download download.Progress
	Err      error
}

// ProgressFunc receives events. Implementations must be safe for concurrent
// use.
type ProgressFunc func(Event)

func (p ProgressFunc) emit(e Event) {
	if p != nil {
		p(e)
	}
}

// VersionFromTag strips a leading v/V when followed by a digit.
func VersionFromTag(tag string) string {
	t := strings.TrimSpace(tag)
	if len(t) > 1 && (t[0] == 'v' || t[0] == 'V') && t[1] >= '0' && t[1] <= '9' {
		return t[1:]
	}
	return t
}

// AddOptions configures `ghpm add`.
type AddOptions struct {
	Input      string
	ID         string
	Platform   platform.Platform
	Asset      string
	Channel    string
	Prerelease bool
	Install    bool
	Progress   ProgressFunc
}

// AddResult reports what was configured.
type AddResult struct {
	App       config.App
	Chosen    resolver.Candidate
	Platforms []string
	Warnings  []string
	Installed *InstallResult
}

// Add resolves a repository, generates its match section and saves the
// configuration.
func (s *Service) Add(ctx context.Context, opts AddOptions) (*AddResult, error) {
	repo, err := config.ParseRepo(opts.Input)
	if err != nil {
		return nil, err
	}
	id := opts.ID
	if id == "" {
		id = filepath.Base(repo)
	}
	if !appIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid id %q; use letters, digits, '.', '_' or '-'", id)
	}
	if s.Cfg.AppByID(id) != nil {
		return nil, fmt.Errorf("app %q is already configured (edit %s or use --id)", id, s.Cfg.Path)
	}
	for _, a := range s.Cfg.Apps {
		if strings.EqualFold(a.Repo, repo) {
			return nil, fmt.Errorf("%s is already configured as %q", repo, a.ID)
		}
	}
	channel := strings.TrimSpace(opts.Channel)
	if channel == "" {
		if opts.Prerelease || s.Cfg.Prerelease() {
			channel = "prerelease"
		} else {
			channel = "latest"
		}
	}
	if channel != "latest" && channel != "prerelease" && !strings.HasPrefix(channel, "tag:") {
		return nil, fmt.Errorf("channel must be latest, prerelease or tag:<tag>")
	}
	rel, err := s.GH.ResolveReleaseFresh(ctx, repo, channel)
	if err != nil {
		return nil, err
	}
	tag := rel.TagName
	ver := VersionFromTag(tag)
	target := opts.Platform
	if target.OS == "" || target.Arch == "" {
		target = platform.Current()
	}
	gen, err := resolver.Generate(rel.AssetNames(), target, tag, ver, opts.Asset, s.Cfg.Defaults.Prefer)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w (assets: %s)", repo, tag, err, strings.Join(rel.AssetNames(), ", "))
	}
	a := config.App{
		ID:      id,
		Repo:    repo,
		Channel: channel,
		Match:   gen.Match,
		Install: gen.Install,
	}
	s.Cfg.Apps = append(s.Cfg.Apps, a)
	if err := s.Cfg.Save(); err != nil {
		s.Cfg.Apps = s.Cfg.Apps[:len(s.Cfg.Apps)-1]
		return nil, err
	}
	out := &AddResult{App: a, Chosen: gen.Chosen, Platforms: resolver.SortedPlatforms(gen.Platforms)}
	if !s.Cfg.CheckBinDir() {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%s is not on PATH", s.BinDir))
	}
	if opts.Install {
		ir := s.InstallOne(ctx, id, InstallOptions{Progress: opts.Progress})
		out.Installed = &ir
	}
	return out, nil
}

var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// InstallOptions configures one install.
type InstallOptions struct {
	Platform platform.Platform
	Tag      string
	Force    bool
	Progress ProgressFunc
}

// InstallResult reports one app's outcome.
type InstallResult struct {
	ID       string
	Tag      string
	Version  string
	Previous string
	Asset    string
	Type     string
	SHA256   string
	Link     string
	Warnings []string
	Removed  []string
	Err      error
}

// InstallOne installs or upgrades a single configured app.
func (s *Service) InstallOne(ctx context.Context, id string, opts InstallOptions) InstallResult {
	res := InstallResult{ID: id}
	app := s.Cfg.AppByID(id)
	if app == nil {
		res.Err = fmt.Errorf("app %q is not configured; run `ghpm add <repo>` first", id)
		opts.Progress.emit(Event{Kind: "error", ID: id, Err: res.Err})
		return res
	}
	target := opts.Platform
	if target.OS == "" || target.Arch == "" {
		target = platform.Current()
	}
	channel := app.Channel
	if opts.Tag != "" {
		channel = "tag:" + opts.Tag
	}
	opts.Progress.emit(Event{Kind: "resolve", ID: id, Message: "resolving " + app.Repo})
	rel, err := s.GH.ResolveReleaseFresh(ctx, app.Repo, channel)
	if err != nil {
		res.Err = err
		opts.Progress.emit(Event{Kind: "error", ID: id, Err: err})
		return res
	}
	tag := rel.TagName
	ver := VersionFromTag(tag)
	res.Tag, res.Version = tag, ver

	name, err := resolver.Resolve(*app, rel.AssetNames(), target, tag, ver)
	if err != nil {
		res.Err = fmt.Errorf("%s %s for %s: %w", app.Repo, tag, target, err)
		opts.Progress.emit(Event{Kind: "error", ID: id, Err: res.Err})
		return res
	}
	asset, ok := rel.FindAsset(name)
	if !ok {
		res.Err = fmt.Errorf("asset %q disappeared from release %s", name, tag)
		opts.Progress.emit(Event{Kind: "error", ID: id, Err: res.Err})
		return res
	}

	verify := s.Cfg.VerifyMode(*app)
	sha, warnings, err := s.checksumFor(ctx, rel, asset, *app, verify)
	if err != nil {
		res.Err = err
		opts.Progress.emit(Event{Kind: "error", ID: id, Err: err})
		return res
	}
	res.Warnings = append(res.Warnings, warnings...)
	if verify == "skip" {
		sha = ""
	}

	prior, _ := s.Store.Load()
	var prev *state.AppState
	if rec, ok := prior.Apps[id]; ok {
		prev = rec
		res.Previous = rec.Version
	}

	assetURL, assetHeaders := s.GH.DownloadSource(asset)
	opts.Progress.emit(Event{Kind: "install", ID: id, Message: fmt.Sprintf("installing %s %s", app.Repo, tag)})
	ir, err := installer.Install(ctx, s.Fetcher, installer.Request{
		App:       *app,
		Platform:  target,
		Tag:       tag,
		Version:   ver,
		AssetName: asset.Name,
		AssetURL:  assetURL,
		Headers:   assetHeaders,
		SHA256:    sha,
		DataDir:   s.DataDir,
		BinDir:    s.BinDir,
		CacheDir:  s.CacheDir,
		Proxy:     s.proxy(),
		Prefix:    s.Cfg.Defaults.GitHub.DownloadPrefix,
		UserAgent: "ghpm/" + version.String(),
		Verify:    verify,
		PruneKeep: s.Cfg.Defaults.PruneKeep,
		Force:     opts.Force,
		Progress:  func(dp download.Progress) { opts.Progress.emit(Event{Kind: "download", ID: id, Download: dp}) },
	})
	if err != nil {
		res.Err = err
		opts.Progress.emit(Event{Kind: "error", ID: id, Err: err})
		return res
	}

	rec := &state.AppState{
		ID:            id,
		Repo:          app.Repo,
		Channel:       app.Channel,
		Tag:           tag,
		Version:       ir.Version,
		Asset:         asset.Name,
		AssetURL:      asset.URL,
		SHA256:        ir.SHA256,
		InstallType:   ir.Type,
		BinPath:       ir.BinPath,
		BinRel:        ir.BinRel,
		LinkPath:      ir.LinkPath,
		Versions:      ir.Versions,
		InstalledAt:   time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
		LatestVersion: ir.Version,
		LatestTag:     tag,
		LastChecked:   time.Now().UTC(),
	}
	if prev != nil {
		rec.Pinned = prev.Pinned
		rec.InstalledAt = prev.InstalledAt
	}
	if err := s.Store.Set(rec); err != nil {
		res.Err = fmt.Errorf("installed, but could not record state: %w", err)
		opts.Progress.emit(Event{Kind: "error", ID: id, Err: res.Err})
		return res
	}
	res.Asset = asset.Name
	res.Type = ir.Type
	res.SHA256 = ir.SHA256
	res.Link = ir.LinkPath
	res.Removed = ir.Removed
	opts.Progress.emit(Event{Kind: "done", ID: id, Message: fmt.Sprintf("%s %s", id, ir.Version)})
	return res
}

func (s *Service) proxy() string { return s.Cfg.Defaults.Proxy }

// checksumFor finds a SHA-256 for asset: API digest first, then a checksum
// manifest from the release.
func (s *Service) checksumFor(ctx context.Context, rel *gh.Release, asset gh.Asset, app config.App, verify string) (string, []string, error) {
	if verify == "skip" {
		return "", nil, nil
	}
	if d := digestSHA(asset.Digest); d != "" {
		return d, nil, nil
	}
	csName := resolver.ChecksumAsset(rel.AssetNames(), asset.Name)
	if csName == "" {
		if verify == "required" {
			return "", nil, fmt.Errorf("no checksum available for %s and verify=required", asset.Name)
		}
		return "", []string{fmt.Sprintf("no checksum published for %s; integrity not verified", asset.Name)}, nil
	}
	csAsset, ok := rel.FindAsset(csName)
	if !ok {
		if verify == "required" {
			return "", nil, fmt.Errorf("checksum asset %q vanished", csName)
		}
		return "", []string{fmt.Sprintf("checksum asset %q missing", csName)}, nil
	}
	dest := filepath.Join(s.CacheDir, "checksums", sanitizeFile(app.ID), sanitizeFile(rel.TagName), filepath.Base(csName))
	var text []byte
	if data, err := os.ReadFile(dest); err == nil && len(data) > 0 {
		text = data
	} else {
		csURL, csHeaders := s.GH.DownloadSource(csAsset)
		if _, err := s.Fetcher.Fetch(ctx, download.Options{
			URL:       csURL,
			Dest:      dest,
			Headers:   csHeaders,
			Prefix:    s.Cfg.Defaults.GitHub.DownloadPrefix,
			Proxy:     s.proxy(),
			UserAgent: "ghpm/" + version.String(),
		}); err != nil {
			if verify == "required" {
				return "", nil, fmt.Errorf("download checksum manifest: %w", err)
			}
			return "", []string{fmt.Sprintf("could not download checksum manifest %s: %v", csName, err)}, nil
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			return "", nil, err
		}
		text = data
	}
	sum, ok := download.FindChecksum(string(text), asset.Name)
	if !ok {
		if verify == "required" {
			return "", nil, fmt.Errorf("%s does not list %s", csName, asset.Name)
		}
		return "", []string{fmt.Sprintf("%s does not list %s; integrity not verified", csName, asset.Name)}, nil
	}
	return sum, nil, nil
}

func digestSHA(d string) string {
	d = strings.TrimSpace(strings.ToLower(d))
	if strings.HasPrefix(d, "sha256:") && len(d) == len("sha256:")+64 {
		return strings.TrimPrefix(d, "sha256:")
	}
	return ""
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeFile(s string) string { return unsafeFilename.ReplaceAllString(s, "_") }

// InstallMany installs several apps with the configured concurrency.
func (s *Service) InstallMany(ctx context.Context, ids []string, opts InstallOptions) []InstallResult {
	results := make([]InstallResult, len(ids))
	sem := make(chan struct{}, s.Cfg.Defaults.Concurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = s.InstallOne(ctx, id, opts)
		}(i, id)
	}
	wg.Wait()
	return results
}

// ConfiguredIDs returns ids of configured apps in file order.
func (s *Service) ConfiguredIDs() []string {
	out := make([]string, len(s.Cfg.Apps))
	for i, a := range s.Cfg.Apps {
		out[i] = a.ID
	}
	return out
}

// MissingIDs returns configured apps without an install record.
func (s *Service) MissingIDs() []string {
	st, err := s.Store.Load()
	if err != nil {
		return s.ConfiguredIDs()
	}
	var out []string
	for _, a := range s.Cfg.Apps {
		if _, ok := st.Apps[a.ID]; !ok {
			out = append(out, a.ID)
		}
	}
	return out
}

// CheckResult reports update availability for one app.
type CheckResult struct {
	ID               string
	InstalledVersion string
	LatestVersion    string
	LatestTag        string
	UpdateAvailable  bool
	Comparable       bool
	Pinned           bool
	Installed        bool
	Err              error
}

// Check queries GitHub for every configured app, updating cached latest
// versions in state.
func (s *Service) Check(ctx context.Context, ids []string, progress ProgressFunc) []CheckResult {
	return s.check(ctx, ids, progress, false)
}

// CheckFresh revalidates release metadata instead of trusting a fresh cache
// entry. The update command uses it so newly published releases appear at
// once.
func (s *Service) CheckFresh(ctx context.Context, ids []string, progress ProgressFunc) []CheckResult {
	return s.check(ctx, ids, progress, true)
}

func (s *Service) check(ctx context.Context, ids []string, progress ProgressFunc, fresh bool) []CheckResult {
	apps := s.Cfg.Apps
	if len(ids) > 0 {
		want := map[string]bool{}
		for _, id := range ids {
			want[id] = true
		}
		var filtered []config.App
		for _, a := range apps {
			if want[a.ID] {
				filtered = append(filtered, a)
			}
		}
		apps = filtered
	}
	st, _ := s.Store.Load()
	results := make([]CheckResult, len(apps))
	sem := make(chan struct{}, s.Cfg.Defaults.Concurrency)
	var wg sync.WaitGroup
	for i, a := range apps {
		wg.Add(1)
		go func(i int, a config.App) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := CheckResult{ID: a.ID}
			rec := st.Apps[a.ID]
			if rec != nil {
				r.Installed = true
				r.InstalledVersion = rec.Version
				r.Pinned = rec.Pinned
			}
			progress.emit(Event{Kind: "check", ID: a.ID, Message: "checking " + a.Repo})
			if rec != nil && rec.Pinned {
				r.LatestVersion = r.InstalledVersion
				r.LatestTag = rec.Tag
				r.Comparable = true
				results[i] = r
				progress.emit(Event{Kind: "check", ID: a.ID, Message: "pinned"})
				return
			}
			if strings.HasPrefix(a.Channel, "tag:") {
				r.LatestTag = strings.TrimPrefix(a.Channel, "tag:")
				r.LatestVersion = VersionFromTag(r.LatestTag)
				r.Comparable = true
				results[i] = r
				return
			}
			var rel *gh.Release
			var err error
			if fresh {
				rel, err = s.GH.ResolveReleaseFresh(ctx, a.Repo, a.Channel)
			} else {
				rel, err = s.GH.ResolveRelease(ctx, a.Repo, a.Channel)
			}
			if err != nil {
				r.Err = err
				results[i] = r
				if rec != nil {
					_ = s.Store.TouchCheck(a.ID, "", "", err)
				}
				progress.emit(Event{Kind: "check", ID: a.ID, Err: err})
				return
			}
			r.LatestTag = rel.TagName
			r.LatestVersion = VersionFromTag(rel.TagName)
			newer, comparable := CompareVersions(r.InstalledVersion, r.LatestVersion)
			r.UpdateAvailable = newer
			r.Comparable = comparable
			results[i] = r
			if rec != nil {
				_ = s.Store.TouchCheck(a.ID, r.LatestVersion, r.LatestTag, nil)
			}
			progress.emit(Event{Kind: "check", ID: a.ID, Message: r.LatestVersion})
		}(i, a)
	}
	wg.Wait()
	return results
}

// CompareVersions reports whether latest is newer than current and whether
// both parsed as semantic versions.
func CompareVersions(current, latest string) (newer, comparable bool) {
	if current == "" {
		return latest != "", true
	}
	cv, err1 := semver.NewVersion(current)
	lv, err2 := semver.NewVersion(latest)
	if err1 == nil && err2 == nil {
		return lv.GreaterThan(cv), true
	}
	return !strings.EqualFold(current, latest), false
}

// UpdateIDs returns configured installed apps with an update available.
func (s *Service) UpdateIDs() []string {
	st, err := s.Store.Load()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range s.Cfg.Apps {
		rec := st.Apps[a.ID]
		if rec == nil || rec.Pinned || strings.HasPrefix(a.Channel, "tag:") {
			continue
		}
		if rec.LatestVersion == "" {
			continue
		}
		if newer, _ := CompareVersions(rec.Version, rec.LatestVersion); newer {
			out = append(out, a.ID)
		}
	}
	return out
}

// Rollback repoints an app at a previously installed version.
func (s *Service) Rollback(id, toVersion string) (InstallResult, error) {
	res := InstallResult{ID: id}
	app := s.Cfg.AppByID(id)
	if app == nil {
		return res, fmt.Errorf("app %q is not configured", id)
	}
	st, err := s.Store.Load()
	if err != nil {
		return res, err
	}
	rec := st.Apps[id]
	if rec == nil {
		return res, fmt.Errorf("app %q is not installed", id)
	}
	appDir := filepath.Join(s.DataDir, "apps", id)
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return res, err
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			versions = append(versions, e.Name())
		}
	}
	sortVersions(versions)
	target := toVersion
	if target == "" {
		idx := -1
		for i, v := range versions {
			if v == rec.Version {
				idx = i
				break
			}
		}
		if idx <= 0 {
			return res, fmt.Errorf("no earlier version of %q is installed", id)
		}
		target = versions[idx-1]
	}
	versionDir := filepath.Join(appDir, target)
	if info, err := os.Stat(versionDir); err != nil || !info.IsDir() {
		return res, fmt.Errorf("version %q of %q is not installed", target, id)
	}
	if rec.LinkPath == "" {
		return res, fmt.Errorf("%q was installed by a package manager and has no ghpm symlink to roll back", id)
	}
	// The relative path of the binary can differ between versions (release
	// archives often embed the version in their top-level directory), so only
	// reuse the recorded path when it exists in this version.
	binRel := ""
	if rec.BinRel != "" {
		if _, err := os.Stat(filepath.Join(versionDir, rec.BinRel)); err == nil {
			binRel = rec.BinRel
		}
	}
	if binRel == "" {
		bin, err := archive.FindBinary(versionDir, app.Install.Bin, app.ID)
		if err != nil {
			return res, err
		}
		binRel, err = filepath.Rel(versionDir, bin)
		if err != nil {
			return res, err
		}
	}
	binAbs := filepath.Join(versionDir, binRel)
	if _, err := os.Stat(binAbs); err != nil {
		return res, fmt.Errorf("binary missing in version %s: %w", target, err)
	}
	if err := installer.SwitchLink(rec.LinkPath, binAbs, true); err != nil {
		return res, err
	}
	prev := rec.Version
	rec.Version = target
	rec.BinPath = binAbs
	rec.BinRel = binRel
	rec.Tag = ""
	rec.UpdatedAt = time.Now().UTC()
	if err := s.Store.Set(rec); err != nil {
		return res, err
	}
	res.Previous = prev
	res.Version = target
	res.Link = rec.LinkPath
	return res, nil
}

// sortVersions orders version directory names with semver rules, falling back
// to lexical order for unparseable tags.
func sortVersions(vs []string) {
	sort.SliceStable(vs, func(i, j int) bool {
		vi, ei := semver.NewVersion(vs[i])
		vj, ej := semver.NewVersion(vs[j])
		switch {
		case ei == nil && ej == nil:
			return vi.LessThan(vj)
		case ei == nil:
			return true
		case ej == nil:
			return false
		}
		return vs[i] < vs[j]
	})
}

// SetPinned pins or unpins an installed app.
func (s *Service) SetPinned(id string, pinned bool) error {
	return s.Store.Update(func(st *state.State) error {
		rec, ok := st.Apps[id]
		if !ok {
			return fmt.Errorf("app %q is not installed", id)
		}
		rec.Pinned = pinned
		return nil
	})
}

// Remove deletes an app's files and record. purge also removes its config.
func (s *Service) Remove(id string, purge bool) ([]string, error) {
	var removed []string
	app := s.Cfg.AppByID(id)
	st, err := s.Store.Load()
	if err != nil {
		return nil, err
	}
	rec := st.Apps[id]
	if app == nil && rec == nil {
		return nil, fmt.Errorf("unknown app %q", id)
	}
	if rec != nil && rec.LinkPath != "" {
		if info, err := os.Lstat(rec.LinkPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(rec.LinkPath)
			if target == "" || strings.HasPrefix(target, s.DataDir) || strings.HasPrefix(target, s.Cfg.Defaults.DataDir) {
				if err := os.Remove(rec.LinkPath); err == nil {
					removed = append(removed, rec.LinkPath)
				}
			}
		}
	}
	appDir := filepath.Join(s.DataDir, "apps", id)
	if _, err := os.Stat(appDir); err == nil {
		if err := os.RemoveAll(appDir); err != nil {
			return removed, err
		}
		removed = append(removed, appDir)
	}
	if rec != nil {
		if err := s.Store.Delete(id); err != nil {
			return removed, err
		}
	}
	if purge && app != nil {
		var kept []config.App
		for _, a := range s.Cfg.Apps {
			if a.ID != id {
				kept = append(kept, a)
			}
		}
		s.Cfg.Apps = kept
		if err := s.Cfg.Save(); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// Prune removes old versions for the given ids (all when empty).
func (s *Service) Prune(ids []string, keep int) (map[string][]string, error) {
	st, err := s.Store.Load()
	if err != nil {
		return nil, err
	}
	if keep <= 0 {
		keep = s.Cfg.Defaults.PruneKeep
	}
	if keep <= 0 {
		keep = 3
	}
	targets := ids
	if len(targets) == 0 {
		targets = s.ConfiguredIDs()
	}
	removed := map[string][]string{}
	for _, id := range targets {
		rec := st.Apps[id]
		current := ""
		if rec != nil {
			current = rec.Version
		}
		appDir := filepath.Join(s.DataDir, "apps", id)
		if _, err := os.Stat(appDir); err != nil {
			continue
		}
		kept, gone := installer.PruneVersions(appDir, keep, current)
		if len(gone) > 0 {
			removed[id] = gone
		}
		if rec != nil {
			rec.Versions = kept
			if err := s.Store.Set(rec); err != nil {
				return removed, err
			}
		}
	}
	return removed, nil
}

// AppView is the merged config + state view used by list and the TUI.
type AppView struct {
	ID              string
	Repo            string
	Channel         string
	Description     string
	Installed       bool
	CurrentVersion  string
	LatestVersion   string
	LatestTag       string
	UpdateAvailable bool
	Comparable      bool
	Pinned          bool
	BinPath         string
	LinkPath        string
	LastChecked     time.Time
	CheckError      string
	Config          config.App
	State           *state.AppState
}

// Views merges configuration with installation state.
func (s *Service) Views() ([]AppView, error) {
	st, err := s.Store.Load()
	if err != nil {
		return nil, err
	}
	byID := map[string]AppView{}
	for _, a := range s.Cfg.Apps {
		byID[a.ID] = AppView{
			ID: a.ID, Repo: a.Repo, Channel: a.Channel, Description: a.Description,
			Config: a,
		}
	}
	for id, rec := range st.Apps {
		v, ok := byID[id]
		if !ok {
			v = AppView{ID: id, Repo: rec.Repo, Channel: rec.Channel}
		}
		v.Installed = true
		v.CurrentVersion = rec.Version
		v.LatestVersion = rec.LatestVersion
		v.LatestTag = rec.LatestTag
		v.Pinned = rec.Pinned
		v.BinPath = rec.BinPath
		v.LinkPath = rec.LinkPath
		v.LastChecked = rec.LastChecked
		v.CheckError = rec.CheckError
		v.State = rec
		if rec.LatestVersion != "" {
			v.UpdateAvailable, v.Comparable = CompareVersions(rec.Version, rec.LatestVersion)
		} else {
			v.Comparable = true
		}
		byID[id] = v
	}
	out := make([]AppView, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID) })
	return out, nil
}

// Release fetches a release for inspection (used by `ghpm assets`).
func (s *Service) Release(ctx context.Context, repo, channel string) (*gh.Release, error) {
	repo, err := config.ParseRepo(repo)
	if err != nil {
		return nil, err
	}
	return s.GH.ResolveRelease(ctx, repo, channel)
}

// Doctor reports environment diagnostics.
type Doctor struct {
	Version    string
	OS         string
	Arch       string
	ConfigPath string
	DataDir    string
	BinDir     string
	CacheDir   string
	TokenSet   bool
	BinOnPath  bool
	RateLimit  gh.RateLimit
	Apps       int
	Installed  int
	Issues     []string
}

// Diagnostics gathers doctor output.
func (s *Service) Diagnostics() Doctor {
	st, _ := s.Store.Load()
	d := Doctor{
		Version: version.String(), OS: platform.Current().OS, Arch: platform.Current().Arch,
		ConfigPath: s.Cfg.Path, DataDir: s.DataDir, BinDir: s.BinDir, CacheDir: s.CacheDir,
		TokenSet: s.GH.HasToken(), BinOnPath: s.Cfg.CheckBinDir(), RateLimit: s.GH.RateLimit(),
		Apps: len(s.Cfg.Apps),
	}
	if st != nil {
		d.Installed = len(st.Apps)
	}
	if !d.BinOnPath {
		d.Issues = append(d.Issues, fmt.Sprintf("%s is not on PATH; add it to use installed tools", s.BinDir))
	}
	if !d.TokenSet {
		d.Issues = append(d.Issues, "GITHUB_TOKEN is not set; API is limited to 60 requests/hour")
	}
	for _, dir := range []string{s.DataDir, s.BinDir} {
		f, err := os.CreateTemp(dir, ".ghpm-write-test-*")
		if err != nil {
			d.Issues = append(d.Issues, fmt.Sprintf("%s is not writable: %v", dir, err))
			continue
		}
		f.Close()
		os.Remove(f.Name())
	}
	if d.RateLimit.Remaining == 0 && !d.RateLimit.Reset.IsZero() && time.Now().Before(d.RateLimit.Reset) {
		d.Issues = append(d.Issues, fmt.Sprintf("GitHub rate limit exhausted until %s", d.RateLimit.Reset.Local().Format("15:04:05")))
	}
	return d
}

// ErrNotInstalled is returned by operations that need an installed app.
var ErrNotInstalled = errors.New("app is not installed")

// CheckErrors summarises failed update checks.
func CheckErrors(results []CheckResult) error {
	var msgs []string
	for _, r := range results {
		if r.Err != nil {
			msgs = append(msgs, r.ID+": "+r.Err.Error())
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("%d check(s) failed: %s", len(msgs), strings.Join(msgs, "; "))
}
