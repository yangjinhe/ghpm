// Package platform describes the host platform and normalises the many
// spellings projects use for operating systems and CPU architectures.
package platform

import (
	"regexp"
	"runtime"
	"strings"
)

// Platform is an OS/architecture pair using Go's canonical names
// (linux, darwin, windows; amd64, arm64, 386, arm, ...).
type Platform struct {
	OS   string
	Arch string
}

func (p Platform) String() string { return p.OS + "/" + p.Arch }

// Current returns the platform this process is running on.
func Current() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// Parse builds a Platform from user supplied values, normalising aliases.
func Parse(osName, arch string) Platform {
	return Platform{OS: NormalizeOS(osName), Arch: NormalizeArch(arch)}
}

// NormalizeOS maps common spellings onto Go's GOOS values.
func NormalizeOS(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "linux", "gnu/linux":
		return "linux"
	case "darwin", "macos", "mac", "osx", "apple-darwin":
		return "darwin"
	case "windows", "win", "win32", "win64":
		return "windows"
	case "freebsd":
		return "freebsd"
	case "":
		return runtime.GOOS
	}
	return s
}

// NormalizeArch maps common spellings onto Go's GOARCH values.
func NormalizeArch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "x86_64", "x86-64", "x64", "amd64", "64bit", "64-bit":
		return "amd64"
	case "aarch64", "arm64", "armv8", "armv8l", "arm64-v8a":
		return "arm64"
	case "armv7", "armv7l", "armhf", "armv7-a", "arm":
		return "arm"
	case "armv6", "armv6l":
		return "arm"
	case "i386", "i686", "x86", "386", "32bit", "32-bit":
		return "386"
	case "ppc64le", "powerpc64le":
		return "ppc64le"
	case "s390x":
		return "s390x"
	case "riscv64":
		return "riscv64"
	case "":
		return runtime.GOARCH
	}
	return s
}

// OSTokens lists the substrings projects put in asset names for an OS.
var OSTokens = map[string][]string{
	"linux":     {"linux"},
	"darwin":    {"darwin", "macos", "osx", "apple-darwin", "mac"},
	"windows":   {"windows", "win32", "win64", "pc-windows", "win"},
	"freebsd":   {"freebsd"},
	"netbsd":    {"netbsd"},
	"openbsd":   {"openbsd"},
	"dragonfly": {"dragonfly"},
	"solaris":   {"solaris", "illumos"},
	"android":   {"android"},
	"ios":       {"ios"},
}

// ArchTokens lists the substrings projects put in asset names for an arch.
// Order matters: longer, more specific tokens are listed first.
var ArchTokens = map[string][]string{
	"amd64":    {"x86_64", "x86-64", "amd64", "x64"},
	"arm64":    {"aarch64", "arm64", "armv8", "arm64-v8a", "arm64e"},
	"arm":      {"armv7l", "armv7", "armhf", "armv6l", "armv6", "armv5l", "armv5", "arm"},
	"386":      {"i686", "i386", "x86", "386"},
	"ppc64le":  {"powerpc64le", "ppc64le", "ppc64"},
	"s390x":    {"s390x"},
	"riscv64":  {"riscv64gc", "riscv64"},
	"loong64":  {"loongarch64", "loong64"},
	"mips64le": {"mips64le"},
	"mips64":   {"mips64"},
	"mipsle":   {"mipsle"},
	"mips":     {"mips"},
}

// ArchAlias returns the token most commonly used in release asset names for
// an architecture (e.g. amd64 -> x86_64), for use in {arch_alias}.
func ArchAlias(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i686"
	case "arm":
		return "armv7l"
	}
	return goarch
}

// ExecutableSuffix is appended to binaries on the platform.
func ExecutableSuffix(osName string) string {
	if osName == "windows" {
		return ".exe"
	}
	return ""
}

// DetectOS returns the OS named in name, or "" when none is mentioned.
// It uses token boundaries so "win" does not match inside "darwin".
func DetectOS(name string) string {
	lower := strings.ToLower(name)
	for _, osName := range []string{"linux", "darwin", "windows", "freebsd", "netbsd", "openbsd", "dragonfly", "solaris", "android", "ios"} {
		for _, tok := range OSTokens[osName] {
			if tokenMatch(lower, tok) {
				return osName
			}
		}
	}
	return ""
}

// DetectArches returns every architecture named in name. Matched spans are
// masked before checking the next architecture so that "x86_64" is not also
// reported as 386 through its "x86" prefix.
func DetectArches(name string) []string {
	lower := strings.ToLower(name)
	var out []string
	for _, arch := range []string{"loong64", "mips64le", "mips64", "mipsle", "mips", "ppc64le", "riscv64", "s390x", "amd64", "arm64", "arm", "386"} {
		for _, tok := range ArchTokens[arch] {
			if tokenMatch(lower, tok) {
				out = append(out, arch)
				lower = maskToken(lower, tok)
				break
			}
		}
	}
	return out
}

func maskToken(s, tok string) string {
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(tok))
	return re.ReplaceAllString(s, strings.Repeat(" ", len(tok)))
}

var tokenBoundary = map[string]*regexp.Regexp{}

func tokenMatch(s, tok string) bool {
	re, ok := tokenBoundary[tok]
	if !ok {
		// A token matches when it is not glued to other alphanumerics.
		// Underscore and hyphen count as separators so x86_64 and x86-64
		// both match their own token and not a shorter prefix.
		pat := `(?:^|[^a-z0-9])` + regexp.QuoteMeta(tok) + `(?:$|[^a-z0-9])`
		re = regexp.MustCompile(pat)
		tokenBoundary[tok] = re
	}
	return re.MatchString(s)
}

// IsArchiveExt reports whether name looks like a supported archive.
func IsArchiveExt(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range []string{".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.zst", ".tzst", ".tar.bz2", ".zip", ".tar"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
