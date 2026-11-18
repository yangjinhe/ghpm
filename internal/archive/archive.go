// Package archive safely extracts release archives and locates binaries.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Format identifies a supported container.
type Format int

const (
	FormatNone Format = iota
	FormatTarGz
	FormatTarXz
	FormatTarZst
	FormatTarBz2
	FormatTar
	FormatZip
)

// FormatOf detects the archive format from a file name.
func FormatOf(name string) Format {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return FormatTarGz
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"):
		return FormatTarXz
	case strings.HasSuffix(lower, ".tar.zst"), strings.HasSuffix(lower, ".tzst"):
		return FormatTarZst
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return FormatTarBz2
	case strings.HasSuffix(lower, ".tar"):
		return FormatTar
	case strings.HasSuffix(lower, ".zip"):
		return FormatZip
	}
	return FormatNone
}

// IsArchive reports whether name is a supported archive.
func IsArchive(name string) bool { return FormatOf(name) != FormatNone }

const (
	maxEntries = 100000
	maxBytes   = int64(4) << 30
)

// Extract unpacks archivePath into destDir. stripComponents removes leading
// path elements (like tar --strip-components). It returns the extracted paths
// relative to destDir.
func Extract(archivePath, destDir string, stripComponents int) ([]string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	switch FormatOf(archivePath) {
	case FormatZip:
		return extractZip(archivePath, destDir, stripComponents)
	case FormatNone:
		return nil, fmt.Errorf("%s: unsupported archive format", filepath.Base(archivePath))
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var r io.Reader = f
	switch FormatOf(archivePath) {
	case FormatTarGz:
		gr, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer gr.Close()
		r = gr
	case FormatTarXz:
		xr, err := xz.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("xz: %w", err)
		}
		r = xr
	case FormatTarZst:
		zr, err := zstd.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
		defer zr.Close()
		r = zr
	case FormatTarBz2:
		r = bzip2.NewReader(f)
	}
	return extractTar(tar.NewReader(r), destDir, stripComponents)
}

func extractTar(tr *tar.Reader, destDir string, strip int) ([]string, error) {
	var out []string
	var total int64
	for i := 0; ; i++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, fmt.Errorf("tar: %w", err)
		}
		if i > maxEntries {
			return out, errors.New("archive has too many entries")
		}
		if err := validateEntryName(hdr.Name); err != nil {
			return out, err
		}
		if isMacMetadata(hdr.Name) {
			continue
		}
		rel, skip := entryPath(hdr.Name, strip)
		if skip {
			continue
		}
		dest, err := safeJoin(destDir, rel)
		if err != nil {
			return out, err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := ensureNoSymlinkParents(destDir, dest); err != nil {
				return out, err
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return out, err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxBytes {
				return out, errors.New("archive exceeds size limit")
			}
			if err := ensureNoSymlinkParents(destDir, dest); err != nil {
				return out, err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return out, err
			}
			mode := os.FileMode(hdr.Mode).Perm() & 0o755
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return out, err
			}
			n, err := io.Copy(f, io.LimitReader(tr, hdr.Size))
			cerr := f.Close()
			if err != nil {
				return out, err
			}
			if cerr != nil {
				return out, cerr
			}
			if n != hdr.Size {
				return out, fmt.Errorf("%s: truncated entry", rel)
			}
			out = append(out, rel)
		case tar.TypeSymlink:
			if err := ensureNoSymlinkParents(destDir, dest); err != nil {
				return out, err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return out, err
			}
			if err := checkLinkTarget(destDir, dest, hdr.Linkname); err != nil {
				return out, err
			}
			_ = os.Remove(dest)
			if err := os.Symlink(hdr.Linkname, dest); err != nil {
				return out, err
			}
			out = append(out, rel)
		case tar.TypeLink:
			if err := ensureNoSymlinkParents(destDir, dest); err != nil {
				return out, err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return out, err
			}
			linkRel, skip := entryPath(hdr.Linkname, strip)
			if skip {
				continue
			}
			linkDest, err := safeJoin(destDir, linkRel)
			if err != nil {
				return out, err
			}
			info, err := os.Lstat(linkDest)
			if err != nil || !info.Mode().IsRegular() {
				return out, fmt.Errorf("%s: hardlink target %q is missing", rel, hdr.Linkname)
			}
			_ = os.Remove(dest)
			if err := os.Link(linkDest, dest); err != nil {
				return out, err
			}
			out = append(out, rel)
		default:
			continue
		}
	}
	return out, nil
}

func extractZip(path, destDir string, strip int) ([]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if len(zr.File) > maxEntries {
		return nil, errors.New("archive has too many entries")
	}
	var out []string
	var total int64
	for _, f := range zr.File {
		if err := validateEntryName(f.Name); err != nil {
			return out, err
		}
		if isMacMetadata(f.Name) {
			continue
		}
		rel, skip := entryPath(f.Name, strip)
		if skip {
			continue
		}
		dest, err := safeJoin(destDir, rel)
		if err != nil {
			return out, err
		}
		if f.FileInfo().IsDir() {
			if err := ensureNoSymlinkParents(destDir, dest); err != nil {
				return out, err
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return out, err
			}
			continue
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			if err := ensureNoSymlinkParents(destDir, dest); err != nil {
				return out, err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return out, err
			}
			rc, err := f.Open()
			if err != nil {
				return out, err
			}
			target, err := io.ReadAll(io.LimitReader(rc, 4097))
			rc.Close()
			if err != nil {
				return out, err
			}
			if len(target) > 4096 {
				return out, fmt.Errorf("%s: symlink target is too long", rel)
			}
			if err := checkLinkTarget(destDir, dest, string(target)); err != nil {
				return out, err
			}
			_ = os.Remove(dest)
			if err := os.Symlink(string(target), dest); err != nil {
				return out, err
			}
			out = append(out, rel)
			continue
		}
		if !mode.IsRegular() {
			continue // device, socket, pipe
		}
		if err := ensureNoSymlinkParents(destDir, dest); err != nil {
			return out, err
		}
		total += int64(f.UncompressedSize64)
		if total > maxBytes {
			return out, errors.New("archive exceeds size limit")
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return out, err
		}
		perm := mode.Perm() & 0o755
		if perm == 0 {
			perm = 0o644
		}
		rc, err := f.Open()
		if err != nil {
			return out, err
		}
		fh, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
		if err != nil {
			rc.Close()
			return out, err
		}
		_, err = io.Copy(fh, io.LimitReader(rc, maxBytes))
		rc.Close()
		if cerr := fh.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return out, err
		}
		out = append(out, rel)
	}
	return out, nil
}

// isMacMetadata reports AppleDouble/resource-fork entries (__MACOSX, ._*)
// and Finder metadata. They are never part of an installable payload and
// would otherwise look like candidate binaries on macOS.
func isMacMetadata(name string) bool {
	n := strings.ReplaceAll(name, "\\", "/")
	for _, part := range strings.Split(n, "/") {
		switch {
		case part == "__MACOSX", part == ".DS_Store":
			return true
		case strings.HasPrefix(part, "._"):
			return true
		}
	}
	return false
}

// ensureNoSymlinkParents refuses to write through a symlink that an earlier
// archive entry created. Without this check a malicious archive could point
// a directory symlink at /etc and then write files through it.
func ensureNoSymlinkParents(root, dest string) error {
	rel, err := filepath.Rel(root, filepath.Dir(dest))
	if err != nil {
		return err
	}
	if rel == "." || rel == "" {
		return nil
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil // the remaining components will be created as real dirs
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry writes through symlink: %s", cur)
		}
	}
	return nil
}

// checkLinkTarget rejects absolute targets and targets that resolve outside
// root. App bundles legitimately use relative symlinks (frameworks), so they
// are preserved rather than skipped.
func checkLinkTarget(root, linkPath, target string) error {
	if target == "" {
		return fmt.Errorf("symlink %s has an empty target", linkPath)
	}
	norm := strings.ReplaceAll(target, "\\", "/")
	if strings.HasPrefix(norm, "/") || filepath.IsAbs(target) {
		return fmt.Errorf("symlink %s has an absolute target %q", linkPath, target)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), filepath.FromSlash(norm)))
	rootClean := filepath.Clean(root)
	if resolved != rootClean && !strings.HasPrefix(resolved, rootClean+string(filepath.Separator)) {
		return fmt.Errorf("symlink %s escapes destination: %q", linkPath, target)
	}
	return nil
}

// validateEntryName rejects absolute paths, including Windows drive letters.
func validateEntryName(name string) error {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") {
		return fmt.Errorf("archive entry has absolute path: %s", name)
	}
	if len(n) >= 2 && n[1] == ':' {
		return fmt.Errorf("archive entry has a drive path: %s", name)
	}
	return nil
}

// entryPath normalises an archive entry name and applies stripping.
// skip is true for entries that reduce to nothing.
func entryPath(name string, strip int) (string, bool) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "./")
	parts := strings.Split(name, "/")
	var kept []string
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		kept = append(kept, p)
	}
	if strip > 0 {
		if len(kept) <= strip {
			return "", true
		}
		kept = kept[strip:]
	}
	if len(kept) == 0 {
		return "", true
	}
	return strings.Join(kept, "/"), false
}

// safeJoin joins rel onto root, refusing absolute paths and traversal.
func safeJoin(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("archive entry has absolute path: %s", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes destination: %s", rel)
	}
	dest := filepath.Join(root, clean)
	rootClean := filepath.Clean(root)
	if dest != rootClean && !strings.HasPrefix(dest, rootClean+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes destination: %s", rel)
	}
	return dest, nil
}

type fileEntry struct {
	path  string
	rel   string
	depth int
	mode  os.FileMode
}

var documentExts = map[string]bool{
	".md": true, ".txt": true, ".rst": true, ".html": true, ".htm": true,
	".pdf": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".conf": true, ".cfg": true, ".service": true, ".desktop": true,
	".1": true, ".2": true, ".3": true, ".4": true, ".5": true, ".6": true,
	".7": true, ".8": true, ".9": true,
}

var scriptExts = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".ps1": true, ".bat": true, ".cmd": true,
}

// FindAppBundle locates a macOS application bundle. It prefers the shallowest
// directory ending in .app that contains Contents/Info.plist and a
// Contents/MacOS directory. ok is false when the tree has no bundle. When
// several bundles sit at the same depth, hints (app id, asset name) pick the
// matching one; otherwise the choice is reported as ambiguous.
func FindAppBundle(dir string, hints ...string) (path string, ok bool, err error) {
	type candidate struct {
		path  string
		rel   string
		depth int
	}
	var found []candidate
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() || !strings.HasSuffix(d.Name(), ".app") {
			return nil
		}
		if _, err := os.Stat(filepath.Join(p, "Contents", "Info.plist")); err != nil {
			return nil
		}
		if info, err := os.Stat(filepath.Join(p, "Contents", "MacOS")); err != nil || !info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		found = append(found, candidate{path: p, rel: rel, depth: strings.Count(rel, string(filepath.Separator))})
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if len(found) == 0 {
		return "", false, nil
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].depth < found[j].depth })
	top := found[0].depth
	var tied []candidate
	for _, c := range found {
		if c.depth == top {
			tied = append(tied, c)
		}
	}
	if len(tied) == 1 {
		return tied[0].path, true, nil
	}
	for _, hint := range hints {
		name := bundleHint(hint)
		if name == "" {
			continue
		}
		var matches []candidate
		for _, c := range tied {
			if bundleHint(filepath.Base(c.path)) == name {
				matches = append(matches, c)
			}
		}
		if len(matches) == 1 {
			return matches[0].path, true, nil
		}
	}
	return "", true, fmt.Errorf("archive contains several application bundles (%s); set install.type/install.bin",
		filepath.ToSlash(tied[0].rel)+", "+filepath.ToSlash(tied[1].rel))
}

func bundleHint(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(filepath.Base(s))), ".app")
}

// MakeAppBundleExecutable restores execute bits on the bundle's main
// executables. ZIP archives created on non-Unix hosts sometimes lose them,
// which stops LaunchServices from starting the app.
func MakeAppBundleExecutable(bundle string) error {
	macosDir := filepath.Join(bundle, "Contents", "MacOS")
	entries, err := os.ReadDir(macosDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", macosDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := os.Chmod(filepath.Join(macosDir, e.Name()), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// HasExecutableOutside reports whether dir has an executable regular file
// outside the bundle. Such archives usually ship a CLI alongside the app and
// should keep the CLI install behaviour.
func HasExecutableOutside(dir, bundle string) (bool, error) {
	bundleRel, err := filepath.Rel(dir, bundle)
	if err != nil {
		return false, err
	}
	found := false
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == bundleRel || strings.HasPrefix(rel, bundleRel+string(filepath.Separator)) || isMacMetadata(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if info.Mode().Perm()&0o111 != 0 {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}

// FindBinary locates the executable a user means. When wanted is set it is
// matched against the file base name (and full relative path suffix). When
// empty, release archives are scored: executable bits, extension-less names
// and hints (app id, asset stem) win; documentation and helper shell scripts
// are ignored. hints are optional.
func FindBinary(dir, wanted string, hints ...string) (string, error) {
	var files []fileEntry
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if isMacMetadata(rel) {
			return nil
		}
		files = append(files, fileEntry{
			path: p, rel: rel,
			depth: strings.Count(rel, string(filepath.Separator)),
			mode:  info.Mode(),
		})
		return nil
	})
	if err != nil {
		return "", err
	}

	if wanted != "" {
		var exact, suffix []fileEntry
		wantBase := strings.TrimSuffix(strings.ToLower(wanted), ".exe")
		for _, f := range files {
			base := strings.TrimSuffix(strings.ToLower(filepath.Base(f.rel)), ".exe")
			relSlash := strings.ToLower(filepath.ToSlash(f.rel))
			switch {
			case base == wantBase:
				exact = append(exact, f)
			case relSlash == strings.ToLower(wanted) ||
				strings.HasSuffix(relSlash, "/"+strings.ToLower(wanted)):
				suffix = append(suffix, f)
			}
		}
		candidates := append(exact, suffix...)
		if len(candidates) == 0 {
			return "", fmt.Errorf("binary %q not found in archive; candidates: %s", wanted, listRel(files))
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			ei := candidates[i].mode.Perm()&0o111 != 0
			ej := candidates[j].mode.Perm()&0o111 != 0
			if ei != ej {
				return ei
			}
			return candidates[i].depth < candidates[j].depth
		})
		return candidates[0].path, nil
	}

	// Drop documentation and obvious metadata.
	pool := make([]fileEntry, 0, len(files))
	for _, f := range files {
		base := strings.ToLower(filepath.Base(f.rel))
		ext := strings.ToLower(filepath.Ext(base))
		if documentExts[ext] {
			continue
		}
		if strings.HasPrefix(base, "readme") || strings.HasPrefix(base, "license") ||
			strings.HasPrefix(base, "changelog") || strings.HasPrefix(base, "copying") {
			continue
		}
		pool = append(pool, f)
	}
	if len(pool) == 0 {
		return "", errors.New("no installable file found in archive")
	}
	if len(pool) == 1 {
		return pool[0].path, nil
	}

	type scored struct {
		fileEntry
		score int
	}
	var ranked []scored
	for _, f := range pool {
		base := strings.ToLower(filepath.Base(f.rel))
		baseNoExe := strings.TrimSuffix(base, ".exe")
		ext := strings.ToLower(filepath.Ext(base))
		score := -f.depth * 3
		if f.mode.Perm()&0o111 != 0 {
			score += 100
		}
		switch {
		case scriptExts[ext]:
			score -= 80
		case ext == "":
			score += 30
		}
		for _, h := range hints {
			h = strings.TrimSuffix(strings.ToLower(filepath.Base(h)), ".exe")
			switch {
			case h == "":
			case baseNoExe == h:
				score += 45
			case strings.HasPrefix(h, baseNoExe+"_") || strings.HasPrefix(h, baseNoExe+"-") ||
				strings.HasPrefix(baseNoExe, h+"_") || strings.HasPrefix(baseNoExe, h+"-"):
				score += 15
			}
		}
		ranked = append(ranked, scored{fileEntry: f, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > 1 && ranked[0].score == ranked[1].score {
		return "", fmt.Errorf("archive contains several possible binaries (%s); set install.bin in the app config",
			listRel([]fileEntry{ranked[0].fileEntry, ranked[1].fileEntry}))
	}
	return ranked[0].path, nil
}

// executables returns files with any execute bit.
func executables(files []fileEntry) []fileEntry {
	var out []fileEntry
	for _, f := range files {
		if f.mode.Perm()&0o111 != 0 {
			out = append(out, f)
		}
	}
	return out
}

func listRel(files []fileEntry) string {
	names := make([]string, 0, len(files))
	for i, f := range files {
		if i == 8 {
			names = append(names, "...")
			break
		}
		names = append(names, filepath.ToSlash(f.rel))
	}
	return strings.Join(names, ", ")
}
