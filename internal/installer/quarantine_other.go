//go:build !darwin

package installer

// quarantineSupported is false everywhere except macOS; StripQuarantine is a
// no-op so call sites stay platform-neutral.
const quarantineSupported = false

func removeQuarantineAttr(string, bool) error { return errNoQuarantine }
