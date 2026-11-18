//go:build darwin

package installer

import (
	"errors"

	"golang.org/x/sys/unix"
)

const quarantineSupported = true

// removeQuarantineAttr deletes the Gatekeeper quarantine attribute. It uses
// the l-variant for symlinks so the link itself is inspected rather than its
// target.
func removeQuarantineAttr(path string, symlink bool) error {
	var err error
	if symlink {
		err = unix.Lremovexattr(path, quarantineAttr)
	} else {
		err = unix.Removexattr(path, quarantineAttr)
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOATTR), errors.Is(err, unix.ENOTSUP):
		return errNoQuarantine
	default:
		return err
	}
}
