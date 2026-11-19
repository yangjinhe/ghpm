// Package version carries build information injected at link time.
package version

// Version is overridden with -ldflags "-X ghpm/internal/version.Version=...".
var Version = "0.5.1-dev"

// Commit is the git revision, also set at link time.
var Commit = ""

func String() string {
	if Commit != "" {
		return Version + " (" + Commit + ")"
	}
	return Version
}
