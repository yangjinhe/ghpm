package installer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// quarantineAttr is the extended attribute macOS Gatekeeper uses to mark
// downloads. Leaving it on an installed binary makes the binary refuse to
// start on macOS.
const quarantineAttr = "com.apple.quarantine"

// errNoQuarantine means the file simply did not carry the attribute. That is
// the normal case and not a failure.
var errNoQuarantine = errors.New("no quarantine attribute")

// StripQuarantine removes com.apple.quarantine from every file and symlink
// under root. It is a no-op on non-macOS builds, so callers may invoke it
// unconditionally after installing or switching versions.
func StripQuarantine(root string) (removed int, err error) {
	if !quarantineSupported {
		return 0, nil
	}
	return stripQuarantine(root, removeQuarantineAttr)
}

// stripQuarantine walks root and calls remove for every non-directory entry.
// Files without the attribute are ignored; the first real error is returned
// after the walk finishes so one unreadable file cannot abort the rest.
func stripQuarantine(root string, remove func(path string, symlink bool) error) (int, error) {
	removed := 0
	var firstErr error
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if firstErr == nil {
				firstErr = walkErr
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if err := remove(p, d.Type()&os.ModeSymlink != 0); err != nil {
			if !errors.Is(err, errNoQuarantine) && firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", p, err)
			}
			return nil
		}
		removed++
		return nil
	})
	if err != nil && firstErr == nil {
		firstErr = err
	}
	return removed, firstErr
}
