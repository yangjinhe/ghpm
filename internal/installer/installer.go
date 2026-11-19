// Package installer turns a downloaded release asset into an installed,
// symlinked binary under the user's data directory.
package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-version"

	"ghpm/internal/archive"
	"ghpm/internal/config"
	"ghpm/internal/download"
	"ghpm/internal/platform"
)

// Request is one installation or upgrade.
type Request struct {
	App       config.App
	Platform  platform.Platform
	Tag       string
	Version   string
	AssetName string
	AssetURL  string
	Headers   map[string]string
	SHA256    string
	DataDir   string
	BinDir    string
	AppsDir   string
	CacheDir  string
	// ReservedLinks maps absolute link paths to the app id that owns them.
	// It keeps two apps from silently sharing one symlink.
	ReservedLinks map[string]string
	Proxy         string
	Prefix        string
	UserAgent     string
	Verify        string
	PruneKeep     int
	Force         bool
	Progress      func(download.Progress)
	SudoRunner    func(ctx context.Context, name string, args ...string) error
}

// Result describes a completed install.
type Result struct {
	ID       string
	Version  string
	Tag      string
	Asset    string
	Type     string
	SHA256   string
	BinPath  string
	BinRel   string
	LinkPath string
	Versions []string
	Removed  []string
	Warnings []string
}

// ErrExistingBinary means the link target is a real file ghpm did not create.
var ErrExistingBinary = errors.New("refusing to overwrite a file not managed by ghpm")

// Install performs the whole local side of an install: download, extract,
// atomically swap in the new version directory, then repoint the symlink.
func Install(ctx context.Context, fetcher *download.Fetcher, req Request) (res Result, err error) {
	if req.AssetURL == "" {
		return res, errors.New("installer: empty asset URL")
	}
	if req.Version == "" {
		req.Version = strings.TrimPrefix(req.Tag, "v")
	}
	ver := sanitizeVersion(req.Version)
	if ver == "" {
		return res, errors.New("installer: cannot determine version")
	}

	typeName := req.App.Install.Type
	if typeName == "" {
		typeName = inferType(req.AssetName)
	}
	// Configs created before dmg support (or hand-edited matches) may pair
	// install.type archive/app with a disk image; route by the actual asset.
	if (typeName == "archive" || typeName == "app") && strings.HasSuffix(strings.ToLower(req.AssetName), ".dmg") {
		typeName = "dmg"
	}
	if typeName == "dmg" && runtime.GOOS != "darwin" {
		return res, errors.New("install.type dmg is only supported on macOS")
	}

	appDir := filepath.Join(req.DataDir, "apps", req.App.ID)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return res, err
	}
	versionDir := filepath.Join(appDir, ver)
	tmpDir, err := os.MkdirTemp(appDir, ".tmp-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmpDir)

	// Downloads live in the cache (per app and version) so a reinstall does
	// not hit the network again. The version directory only receives the
	// unpacked result.
	assetFile := filepath.Join(req.CacheDir, "downloads", req.App.ID, ver, filepath.Base(req.AssetName))
	switch typeName {
	case "deb", "rpm", "pkg":
		// System package formats share one cache; they are not extracted into
		// a version directory.
		assetFile = filepath.Join(req.CacheDir, "packages", filepath.Base(req.AssetName))
	}
	sum, err := fetcher.Fetch(ctx, download.Options{
		URL:            req.AssetURL,
		Dest:           assetFile,
		Prefix:         req.Prefix,
		Proxy:          req.Proxy,
		UserAgent:      req.UserAgent,
		Progress:       req.Progress,
		Headers:        req.Headers,
		ExpectedSHA256: req.SHA256,
	})
	if err != nil {
		return res, err
	}
	res.SHA256 = sum
	if req.SHA256 == "" && req.Verify == "required" {
		return res, errors.New("no checksum available but verify=required")
	}

	// Build the version directory contents in tmpDir.
	var binRel string
	appMode := false
	switch typeName {
	case "archive", "app":
		if _, err := archive.Extract(assetFile, tmpDir, req.App.Install.Stripping); err != nil {
			return res, fmt.Errorf("extract %s: %w", req.AssetName, err)
		}
		bundle, hasBundle, err := archive.FindAppBundle(tmpDir, req.App.ID, req.AssetName)
		if err != nil {
			return res, err
		}
		switch {
		case typeName == "app":
			if !hasBundle {
				return res, fmt.Errorf("install.type app: %s does not contain a .app bundle", req.AssetName)
			}
			appMode = true
		case hasBundle:
			// Only take over when the bundle is the sole payload; archives
			// that also ship a CLI binary keep the normal install path.
			// install.bin is an explicit request for a CLI inside the bundle.
			if req.App.Install.Bin != "" {
				break
			}
			outside, err := archive.HasExecutableOutside(tmpDir, bundle)
			if err != nil {
				return res, err
			}
			appMode = !outside
		}
		if appMode {
			binRel, err = filepath.Rel(tmpDir, bundle)
			if err != nil {
				return res, err
			}
			break
		}
		bin, err := archive.FindBinary(tmpDir, req.App.Install.Bin, req.App.ID, req.AssetName)
		if err != nil {
			return res, err
		}
		binRel, err = filepath.Rel(tmpDir, bin)
		if err != nil {
			return res, err
		}
		if err := os.Chmod(bin, 0o755); err != nil {
			return res, err
		}
	case "binary", "appimage":
		binRel = filepath.Base(assetFile)
		if req.App.Install.Bin != "" {
			binRel = filepath.Base(req.App.Install.Bin)
		}
		if err := copyFile(assetFile, filepath.Join(tmpDir, binRel), 0o755); err != nil {
			return res, err
		}
	case "deb", "rpm", "pkg":
		if err := installSystemPackage(ctx, req, typeName, assetFile); err != nil {
			return res, err
		}
		res.ID = req.App.ID
		res.Version = req.Version
		res.Tag = req.Tag
		res.Asset = req.AssetName
		res.Type = typeName
		return res, nil
	case "dmg":
		bundle, warnings, err := installDMG(ctx, assetFile, tmpDir, req.App.ID, req.AssetName)
		if err != nil {
			return res, err
		}
		res.Warnings = append(res.Warnings, warnings...)
		appMode = true
		binRel, err = filepath.Rel(tmpDir, bundle)
		if err != nil {
			return res, err
		}
	case "script":
		if !req.App.Install.AllowScript {
			return res, errors.New("script installs are disabled; set install.allow_scripts: true to opt in")
		}
		if err := runScript(ctx, req, assetFile); err != nil {
			return res, err
		}
		res.ID, res.Version, res.Tag, res.Asset = req.App.ID, req.Version, req.Tag, req.AssetName
		res.Type = typeName
		return res, nil
	default:
		return res, fmt.Errorf("unknown install type %q", typeName)
	}

	if appMode {
		if err := archive.MakeAppBundleExecutable(filepath.Join(tmpDir, binRel)); err != nil {
			return res, err
		}
	}

	// Swap in the new version directory.
	if err := os.RemoveAll(versionDir); err != nil {
		return res, err
	}
	if err := os.Rename(tmpDir, versionDir); err != nil {
		return res, err
	}
	// macOS refuses to execute quarantined binaries. Clear the attribute on
	// the freshly extracted tree before linking it into place.
	if _, err := StripQuarantine(versionDir); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("could not clear macOS quarantine attribute: %v", err))
	}
	binAbs := filepath.Join(versionDir, binRel)

	linkName := req.App.Install.Link
	if linkName == "" {
		switch {
		case appMode:
			// Keep the bundle name, e.g. Motty.app.
			linkName = filepath.Base(binRel)
		case req.App.Install.Bin != "":
			linkName = filepath.Base(req.App.Install.Bin)
		case typeName == "archive":
			// Archives usually contain the canonical binary name.
			linkName = filepath.Base(binRel)
		default:
			// Release binaries often embed version and platform in the file
			// name; use the app id for a stable command name.
			linkName = req.App.ID
		}
	}
	if linkName == "" || linkName == "." || linkName == string(filepath.Separator) {
		linkName = req.App.ID
	}
	linkPath := filepath.Join(req.BinDir, linkName)
	if appMode {
		dir, warnings, err := prepareAppsDir(req.AppsDir)
		if err != nil {
			return res, err
		}
		res.Warnings = append(res.Warnings, warnings...)
		linkPath = filepath.Join(dir, linkName)
	}
	if owner, ok := req.ReservedLinks[filepath.Clean(linkPath)]; ok && owner != req.App.ID {
		if req.App.Install.Link != "" {
			return res, fmt.Errorf("link %s is already used by app %q; set install.link to another name", linkPath, owner)
		}
		fallback := req.App.ID
		if appMode && !strings.HasSuffix(strings.ToLower(fallback), ".app") {
			fallback += ".app"
		}
		fallbackPath := filepath.Join(filepath.Dir(linkPath), fallback)
		if other, ok := req.ReservedLinks[filepath.Clean(fallbackPath)]; ok && other != req.App.ID {
			return res, fmt.Errorf("link %s is used by app %q and fallback %s by %q; set install.link",
				linkPath, owner, fallbackPath, other)
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"link %s is used by app %q; linking %s as %s", linkPath, owner, req.App.ID, fallbackPath))
		linkName = fallback
		linkPath = fallbackPath
	}
	if err := makeSymlink(linkPath, binAbs, req.Force); err != nil {
		return res, err
	}

	res.ID = req.App.ID
	res.Version = req.Version
	res.Tag = req.Tag
	res.Asset = req.AssetName
	res.Type = typeName
	if appMode {
		res.Type = "app"
	}
	res.BinPath = binAbs
	res.BinRel = binRel
	res.LinkPath = linkPath
	res.Versions = listVersions(appDir)

	keep := req.PruneKeep
	if keep <= 0 {
		keep = 3
	}
	res.Versions, res.Removed = prune(appDir, res.Versions, keep, ver)
	return res, nil
}

func inferType(asset string) string {
	lower := strings.ToLower(asset)
	switch {
	case strings.HasSuffix(lower, ".deb"):
		return "deb"
	case strings.HasSuffix(lower, ".rpm"):
		return "rpm"
	case strings.HasSuffix(lower, ".pkg"):
		return "pkg"
	case strings.HasSuffix(lower, ".dmg"):
		return "dmg"
	case strings.HasSuffix(lower, ".appimage"):
		return "appimage"
	case archive.IsArchive(asset):
		return "archive"
	}
	return "binary"
}

var unsafeVersionChars = regexp.MustCompile(`[^A-Za-z0-9._+-]+`)

func sanitizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = unsafeVersionChars.ReplaceAllString(v, "_")
	v = strings.Trim(v, "._-")
	if len(v) > 64 {
		v = v[:64]
	}
	return v
}

func makeSymlink(linkPath, target string, force bool) error {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(linkPath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			if info.IsDir() {
				return fmt.Errorf("%w: %s is an existing directory; remove it manually", ErrExistingBinary, linkPath)
			}
			if !force {
				return fmt.Errorf("%w: %s (use --force to replace)", ErrExistingBinary, linkPath)
			}
		}
	}
	tmp := filepath.Join(filepath.Dir(linkPath), fmt.Sprintf(".%s.ghpm-%d", filepath.Base(linkPath), time.Now().UnixNano()))
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// installDMG mounts a disk image read-only, copies the application bundle
// into destDir with ditto (preserving modes, symlinks and signature data) and
// detaches the image. Only called on macOS.
func installDMG(ctx context.Context, dmg, destDir, appID, assetName string) (string, []string, error) {
	mountPoint, err := os.MkdirTemp("", "ghpm-dmg-*")
	if err != nil {
		return "", nil, err
	}
	attached := false
	detach := func() error {
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(dctx, "hdiutil", "detach", "-quiet", mountPoint).CombinedOutput()
		if err != nil {
			_, _ = exec.CommandContext(dctx, "hdiutil", "detach", "-force", "-quiet", mountPoint).CombinedOutput()
			return fmt.Errorf("detach %s: %v: %s", mountPoint, err, commandError(out))
		}
		return nil
	}
	defer func() {
		if attached {
			_ = detach()
		}
		_ = os.RemoveAll(mountPoint)
	}()

	out, err := func() ([]byte, error) {
		// A damaged image or a hanging mount must not block install forever.
		actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return exec.CommandContext(actx, "hdiutil", "attach",
			"-nobrowse", "-readonly", "-noautoopen", "-quiet", "-mountpoint", mountPoint, dmg).CombinedOutput()
	}()
	if err != nil {
		return "", nil, fmt.Errorf("mount %s: %v: %s", filepath.Base(dmg), err, commandError(out))
	}
	attached = true

	bundle, ok, err := archive.FindAppBundle(mountPoint, appID, assetName)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return "", nil, fmt.Errorf("%s does not contain an .app bundle", filepath.Base(dmg))
	}
	dst := filepath.Join(destDir, filepath.Base(bundle))
	out, err = exec.CommandContext(ctx, "ditto", bundle, dst).CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("copy %s from disk image: %v: %s", filepath.Base(bundle), err, commandError(out))
	}
	var warnings []string
	if err := detach(); err != nil {
		warnings = append(warnings, err.Error())
	}
	attached = false
	return dst, warnings, nil
}

func commandError(out []byte) string {
	s := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// prepareAppsDir returns a writable directory for application bundle links.
// When the default /Applications is not writable (non-admin users) it falls
// back to ~/Applications and explains the move in a warning.
func prepareAppsDir(dir string) (string, []string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", nil, errors.New("no apps directory configured for .app bundles; set defaults.apps_dir")
	}
	if writableDir(dir) {
		return dir, nil, nil
	}
	if filepath.Clean(dir) == "/Applications" {
		home, err := os.UserHomeDir()
		if err == nil {
			fallback := filepath.Join(home, "Applications")
			if writableDir(fallback) {
				return fallback, []string{fmt.Sprintf("%s is not writable; linked the application into %s instead", dir, fallback)}, nil
			}
		}
	}
	return "", nil, fmt.Errorf("cannot write to apps directory %s; set defaults.apps_dir", dir)
}

func writableDir(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".ghpm-write-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func listVersions(appDir string) []string {
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, e.Name())
	}
	sortVersions(out)
	return out
}

func sortVersions(vs []string) {
	sort.SliceStable(vs, func(i, j int) bool {
		vi, ei := version.NewVersion(vs[i])
		vj, ej := version.NewVersion(vs[j])
		if ei == nil && ej == nil {
			return vi.LessThan(vj)
		}
		if ei == nil {
			return true
		}
		if ej == nil {
			return false
		}
		return vs[i] < vs[j]
	})
}

// prune removes the oldest version directories beyond keep, never touching
// current.
func prune(appDir string, versions []string, keep int, current string) (kept, removed []string) {
	kept = append([]string{}, versions...)
	if keep < 1 {
		keep = 1
	}
	for len(kept) > keep {
		idx := -1
		for i, v := range kept {
			if v != current {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		victim := kept[idx]
		_ = os.RemoveAll(filepath.Join(appDir, victim))
		kept = append(kept[:idx], kept[idx+1:]...)
		removed = append(removed, victim)
	}
	return kept, removed
}

func installSystemPackage(ctx context.Context, req Request, typ, file string) error {
	run := req.SudoRunner
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
			return cmd.Run()
		}
	}
	mode := req.App.Install.Sudo
	if mode == "" {
		mode = "prompt"
	}
	prefix := []string{}
	if os.Geteuid() != 0 && mode != "never" {
		prefix = []string{"sudo"}
	}
	runCmd := func(name string, args ...string) error {
		full := append(append([]string{}, prefix...), append([]string{name}, args...)...)
		return run(ctx, full[0], full[1:]...)
	}

	switch typ {
	case "deb":
		if _, err := exec.LookPath("apt-get"); err == nil {
			args := append([]string{"install", "-y"}, req.App.Install.Args...)
			args = append(args, file)
			return runCmd("apt-get", args...)
		}
		args := append([]string{"-i"}, req.App.Install.Args...)
		args = append(args, file)
		return runCmd("dpkg", args...)
	case "rpm":
		if _, err := exec.LookPath("dnf"); err == nil {
			args := append([]string{"install", "-y"}, req.App.Install.Args...)
			args = append(args, file)
			return runCmd("dnf", args...)
		}
		if _, err := exec.LookPath("zypper"); err == nil {
			args := append([]string{"install", "-y"}, req.App.Install.Args...)
			args = append(args, file)
			return runCmd("zypper", args...)
		}
		return runCmd("rpm", "-Uvh", file)
	case "pkg":
		if runtime.GOOS != "darwin" {
			return errors.New("install.type pkg is only supported on macOS")
		}
		args := append([]string{"-pkg", file, "-target", "/"}, req.App.Install.Args...)
		return runCmd("installer", args...)
	}
	return fmt.Errorf("unsupported package type %q", typ)
}

func runScript(ctx context.Context, req Request, file string) error {
	_ = os.Chmod(file, 0o755)
	if req.SudoRunner != nil {
		return req.SudoRunner(ctx, file)
	}
	cmd := exec.CommandContext(ctx, file)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}

// SwitchLink atomically repoints linkPath at target.
func SwitchLink(linkPath, target string, force bool) error {
	return makeSymlink(linkPath, target, force)
}

// PruneVersions removes old version directories, keeping the newest keep
// entries plus current.
func PruneVersions(appDir string, keep int, current string) (kept, removed []string) {
	return prune(appDir, listVersions(appDir), keep, current)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}
