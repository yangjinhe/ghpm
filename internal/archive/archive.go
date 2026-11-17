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
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return out, err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxBytes {
				return out, errors.New("archive exceeds size limit")
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
		case tar.TypeSymlink, tar.TypeLink:
			// Symlinks (including hardlinks) are skipped: release binaries
			// never need them and they are the usual escape vector.
			continue
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
		rel, skip := entryPath(f.Name, strip)
		if skip {
			continue
		}
		dest, err := safeJoin(destDir, rel)
		if err != nil {
			return out, err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return out, err
			}
			continue
		}
		mode := f.Mode()
		if !mode.IsRegular() {
			continue // symlink or device
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

// FindBinary locates the executable a user means. When wanted is set it is
// matched against the file base name (and full relative path suffix). When
// empty, the single executable file is returned; with several candidates a
// helpful error lists them.
func FindBinary(dir, wanted string) (string, error) {
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
			return "", fmt.Errorf("binary %q not found in archive; candidates: %s", wanted, listRel(executables(files)))
		}
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].depth < candidates[j].depth })
		return candidates[0].path, nil
	}

	pool := executables(files)
	if len(pool) == 0 {
		pool = files
	}
	// Ignore obvious documentation files.
	filtered := make([]fileEntry, 0, len(pool))
	for _, f := range pool {
		base := strings.ToLower(filepath.Base(f.rel))
		if strings.HasPrefix(base, "readme") || strings.HasPrefix(base, "license") ||
			strings.HasPrefix(base, "changelog") || strings.HasSuffix(base, ".txt") ||
			strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".1") {
			continue
		}
		filtered = append(filtered, f)
	}
	pool = filtered

	switch len(pool) {
	case 0:
		return "", errors.New("no executable file found in archive")
	case 1:
		return pool[0].path, nil
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].depth != pool[j].depth {
			return pool[i].depth < pool[j].depth
		}
		return pool[i].rel < pool[j].rel
	})
	if pool[0].depth < pool[1].depth {
		return pool[0].path, nil
	}
	return "", fmt.Errorf("archive contains several executables (%s); set install.bin in the app config", listRel(pool))
}

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
