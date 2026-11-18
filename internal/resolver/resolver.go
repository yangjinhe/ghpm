// Package resolver picks the right release asset for a platform. It powers
// both automatic selection at install time and `ghpm add` config generation.
package resolver

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"ghpm/internal/config"
	"ghpm/internal/platform"
)

// Format is the kind of artifact an asset represents.
type Format string

const (
	FormatArchive   Format = "archive"
	FormatBinary    Format = "binary"
	FormatDeb       Format = "deb"
	FormatRPM       Format = "rpm"
	FormatPkg       Format = "pkg"
	FormatDmg       Format = "dmg"
	FormatAppImage  Format = "appimage"
	FormatChecksum  Format = "checksum"
	FormatSignature Format = "signature"
	FormatOther     Format = "other"
)

// Candidate is one asset with its classification and score for a platform.
type Candidate struct {
	Name   string
	Score  int
	OS     string
	Arches []string
	Format Format
	Notes  []string
}

// Options tunes scoring.
type Options struct {
	Prefer      string // "gnu" or "musl"
	InstallType string // borrow preference from the configured install type
}

// Classify inspects an asset name without scoring it.
func Classify(name string) Candidate {
	c := Candidate{Name: name, Format: classifyFormat(name)}
	if IsChecksumAsset(name) {
		c.Format = FormatChecksum
		return c
	}
	if IsSignatureAsset(name) {
		c.Format = FormatSignature
		return c
	}
	c.OS = platform.DetectOS(name)
	c.Arches = platform.DetectArches(name)
	if c.OS == "" {
		// Package formats and .exe files imply their platform.
		switch c.Format {
		case FormatDeb, FormatRPM, FormatAppImage:
			c.OS = "linux"
		case FormatPkg, FormatDmg:
			c.OS = "darwin"
		case FormatBinary:
			if strings.HasSuffix(strings.ToLower(name), ".exe") {
				c.OS = "windows"
			}
		}
	}
	if strings.Contains(strings.ToLower(name), "universal") {
		// "universal" (universal2) is Apple's fat-binary naming; treat it as
		// a macOS asset that matches both architectures.
		if c.OS == "" {
			c.OS = "darwin"
		}
		if c.OS == "darwin" {
			c.Arches = []string{"amd64", "arm64"}
		}
	}
	return c
}

var checksumName = regexp.MustCompile(`(?i)(^|[^a-z])(checksums?|sha-?256(sums)?|hashes)([^a-z]|$)`)

// IsChecksumAsset reports whether name is a checksum manifest.
func IsChecksumAsset(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".sha256") || strings.HasSuffix(lower, ".sha256sum") ||
		strings.HasSuffix(lower, ".sha512") || strings.HasSuffix(lower, ".md5") {
		return true
	}
	base := strings.TrimSuffix(lower, ".txt")
	if checksumName.MatchString(base) {
		// Avoid treating the main program named "checksum" as metadata.
		switch classifyFormat(name) {
		case FormatArchive, FormatDeb, FormatRPM, FormatAppImage, FormatBinary:
			if !strings.Contains(base, "sum") && !strings.Contains(base, "sha") &&
				!strings.Contains(base, "checksum") {
				return false
			}
		}
		return true
	}
	return false
}

// IsSignatureAsset reports whether name is a signature or provenance file.
func IsSignatureAsset(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range []string{".sig", ".asc", ".pem", ".minisig", ".sbom", ".spdx", ".intoto.jsonl", ".attestation", ".cert", ".crt"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

var sourceName = regexp.MustCompile(`(?i)(^|[^a-z])(source|src|sources)([^a-z]|$)`)

// IsSourceAsset reports whether name is a source tarball.
func IsSourceAsset(name string) bool {
	return sourceName.MatchString(strings.TrimSuffix(strings.ToLower(name), ".txt"))
}

func classifyFormat(name string) Format {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".deb"):
		return FormatDeb
	case strings.HasSuffix(lower, ".rpm"):
		return FormatRPM
	case strings.HasSuffix(lower, ".pkg"):
		return FormatPkg
	case strings.HasSuffix(lower, ".dmg"):
		return FormatDmg
	case strings.HasSuffix(lower, ".appimage"):
		return FormatAppImage
	case platform.IsArchiveExt(lower):
		return FormatArchive
	case strings.HasSuffix(lower, ".apk"), strings.HasSuffix(lower, ".snap"),
		strings.HasSuffix(lower, ".flatpak"), strings.HasSuffix(lower, ".nupkg"),
		strings.HasSuffix(lower, ".json"), strings.HasSuffix(lower, ".txt"),
		strings.HasSuffix(lower, ".yml"), strings.HasSuffix(lower, ".yaml"),
		strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".html"):
		return FormatOther
	case strings.HasSuffix(lower, ".exe"):
		return FormatBinary
	default:
		return FormatBinary
	}
}

// Score rates an asset for the target platform. ok is false when the asset
// clearly belongs to a different OS or architecture.
func Score(name string, target platform.Platform, opts Options) (Candidate, bool) {
	c := Classify(name)
	if c.Format == FormatChecksum || c.Format == FormatSignature || c.Format == FormatOther {
		return c, false
	}
	if c.Format == FormatDmg {
		// Mounting and copying app bundles is not supported; expose the asset
		// in `ghpm assets` but never select it for install.
		c.Notes = append(c.Notes, "excluded:disk-image")
		return c, false
	}
	if IsSourceAsset(name) {
		return c, false
	}
	lower := strings.ToLower(name)
	for _, bad := range []string{"debug", "symbols", "sbom", "notarization", "dsym"} {
		if strings.Contains(lower, bad) {
			c.Notes = append(c.Notes, "excluded:"+bad)
			return c, false
		}
	}

	score := 0
	if c.OS != "" {
		if target.OS != "" && c.OS != target.OS && !(target.OS == "darwin" && c.OS == "darwin") {
			return c, false
		}
		score += 50
	} else {
		score += 15
	}
	if len(c.Arches) > 0 {
		matched := false
		for _, a := range c.Arches {
			if a == target.Arch {
				matched = true
				break
			}
		}
		if !matched {
			return c, false
		}
		score += 50
	} else {
		score += 15
		c.Notes = append(c.Notes, "no-arch-token")
	}

	switch c.Format {
	case FormatArchive:
		score += 6
	case FormatBinary:
		score += 4
	case FormatPkg:
		// Installer packages need sudo; prefer archives unless the app asks
		// for the pkg type explicitly.
		score += 2
	case FormatAppImage:
		score += 5
	}
	if opts.InstallType != "" && string(c.Format) == opts.InstallType {
		score += 12
	}
	if opts.Prefer != "" && (c.OS == "linux" || c.OS == "") {
		isMusl := strings.Contains(lower, "musl")
		isGNU := strings.Contains(lower, "gnu")
		switch {
		case opts.Prefer == "musl" && isMusl:
			score += 4
		case opts.Prefer == "musl" && isGNU:
			score -= 4
		case opts.Prefer == "gnu" && isGNU:
			score += 4
		case opts.Prefer == "gnu" && isMusl:
			score -= 4
		}
	}
	if strings.Contains(lower, "universal") {
		score += 3
	}
	c.Score = score
	return c, true
}

// ErrAmbiguous means several assets fit equally well.
var ErrAmbiguous = errors.New("ambiguous asset match")

// ErrNoMatch means nothing fits the target platform.
var ErrNoMatch = errors.New("no matching asset")

// Best picks the highest scoring asset. When the top two are equally good it
// returns ErrAmbiguous together with the ranked candidates so callers can show
// a chooser.
func Best(names []string, target platform.Platform, opts Options) (Candidate, []Candidate, error) {
	var ranked []Candidate
	for _, n := range names {
		if c, ok := Score(n, target, opts); ok {
			ranked = append(ranked, c)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	if len(ranked) == 0 {
		return Candidate{}, ranked, ErrNoMatch
	}
	if best, ok := pickTop(ranked); ok {
		return best, ranked, nil
	}
	return ranked[0], ranked, fmt.Errorf("%w: %s and %s score %d", ErrAmbiguous, ranked[0].Name, ranked[1].Name, ranked[0].Score)
}

// pickTop resolves ties among the highest scoring candidates. It prefers the
// plain build when the only difference is a numeric compatibility suffix
// (e.g. bottom's "-2-17" glibc variant). Genuinely different variants such as
// "static" remain ambiguous so the user can choose.
func pickTop(ranked []Candidate) (Candidate, bool) {
	top := ranked[0].Score
	var tied []Candidate
	for _, c := range ranked {
		if c.Score == top {
			tied = append(tied, c)
		}
	}
	if len(tied) == 1 {
		return tied[0], true
	}
	for _, cand := range tied {
		stem := assetStem(cand.Name)
		canonical := true
		for _, other := range tied {
			if other.Name == cand.Name {
				continue
			}
			otherStem := assetStem(other.Name)
			if !strings.HasPrefix(otherStem, stem) {
				canonical = false
				break
			}
			rest := otherStem[len(stem):]
			if len(rest) < 2 || !strings.ContainsRune("-_.", rune(rest[0])) || rest[1] < '0' || rest[1] > '9' {
				canonical = false
				break
			}
		}
		if canonical {
			return cand, true
		}
	}
	return Candidate{}, false
}

func assetStem(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range []string{".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.zst", ".tzst", ".tar.bz2", ".zip", ".tar", ".deb", ".rpm", ".pkg", ".dmg", ".appimage"} {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}

// Expand replaces placeholders in a configured asset pattern.
func Expand(pattern string, p platform.Platform, tag, version string) string {
	r := strings.NewReplacer(
		"{tag}", tag,
		"{version}", version,
		"{os}", p.OS,
		"{arch}", p.Arch,
		"{arch_alias}", platform.ArchAlias(p.Arch),
		"{os_arch}", p.OS+"-"+p.Arch,
	)
	return r.Replace(pattern)
}

// MatchPattern finds asset names equal to or glob-matching pattern.
func MatchPattern(pattern string, names []string) []string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	var out []string
	glob := strings.ContainsAny(pattern, "*?[")
	for _, n := range names {
		if strings.EqualFold(n, pattern) {
			out = append(out, n)
			continue
		}
		if glob {
			if ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(n)); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

// Resolve returns the asset name configured for the target platform.
func Resolve(app config.App, names []string, target platform.Platform, tag, version string) (string, error) {
	opts := Options{Prefer: "", InstallType: app.Install.Type}
	if app.Match.Regex != "" {
		re, err := regexp.Compile(app.Match.Regex)
		if err != nil {
			return "", fmt.Errorf("app %q: bad regex: %w", app.ID, err)
		}
		var matches []string
		for _, n := range names {
			if re.MatchString(n) {
				matches = append(matches, n)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) == 0 {
			return "", fmt.Errorf("%w: regex %q matched nothing for %s", ErrNoMatch, app.Match.Regex, target)
		}
		if best, _, err := Best(matches, target, opts); err == nil {
			return best.Name, nil
		}
		return "", fmt.Errorf("%w: regex matched %s", ErrAmbiguous, strings.Join(matches, ", "))
	}

	// Explicit platform map: exact, then OS wildcard, arch wildcard, any.
	if pattern, ok := lookupPlatform(app.Match.Platforms, target); ok {
		expanded := Expand(pattern, target, tag, version)
		matches := MatchPattern(expanded, names)
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
			return "", fmt.Errorf("%w: configured asset %q not found in release %s (assets: %s)",
				ErrNoMatch, expanded, tag, strings.Join(names, ", "))
		default:
			if best, _, err := Best(matches, target, opts); err == nil {
				return best.Name, nil
			}
			return "", fmt.Errorf("%w: %q matched %s", ErrAmbiguous, expanded, strings.Join(matches, ", "))
		}
	}
	if app.Match.Template != "" {
		expanded := Expand(app.Match.Template, target, tag, version)
		matches := MatchPattern(expanded, names)
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) == 0 {
			return "", fmt.Errorf("%w: template %q matched nothing for %s", ErrNoMatch, expanded, target)
		}
		if best, _, err := Best(matches, target, opts); err == nil {
			return best.Name, nil
		}
	}

	// No match section (or a map without this platform): score everything.
	best, ranked, err := Best(names, target, opts)
	if err != nil {
		return "", err
	}
	_ = ranked
	return best.Name, nil
}

func lookupPlatform(m map[string]string, p platform.Platform) (string, bool) {
	if len(m) == 0 {
		return "", false
	}
	candidates := []string{
		p.OS + "/" + p.Arch,
		p.OS + "/*",
		"*/" + p.Arch,
		"*/*",
		"any",
		"default",
	}
	for _, k := range candidates {
		if v, ok := m[k]; ok && v != "" {
			return v, true
		}
	}
	// Tolerate case differences and aliases in user written keys.
	for k, v := range m {
		parts := strings.SplitN(k, "/", 2)
		if len(parts) != 2 {
			continue
		}
		if platform.NormalizeOS(parts[0]) == p.OS && platform.NormalizeArch(parts[1]) == p.Arch {
			return v, true
		}
	}
	return "", false
}

// Generated is the config fragment produced by `ghpm add`.
type Generated struct {
	Match     config.Match
	Install   config.Install
	Chosen    Candidate
	Platforms map[string]Candidate
}

// Generate inspects a release's assets and builds a match section covering
// every platform it can identify confidently.
func Generate(names []string, target platform.Platform, tag, version, preferred string, prefer string) (Generated, error) {
	opts := Options{Prefer: prefer}
	var chosen Candidate
	if preferred != "" {
		for _, n := range names {
			if strings.EqualFold(n, preferred) {
				c := Classify(n)
				if c.Format == FormatChecksum || c.Format == FormatSignature || c.Format == FormatOther || c.Format == FormatDmg {
					return Generated{}, fmt.Errorf("asset %q is not installable", preferred)
				}
				chosen = c
				break
			}
		}
		if chosen.Name == "" {
			return Generated{}, fmt.Errorf("asset %q not found in release %s", preferred, tag)
		}
	} else {
		var err error
		chosen, _, err = Best(names, target, opts)
		if err != nil {
			return Generated{}, err
		}
	}

	// Per-platform winners. Ambiguous platforms are left out rather than
	// guessed; the platform being configured is always written below.
	perPlatform := map[string][]Candidate{}
	for _, n := range names {
		c := Classify(n)
		if c.Format == FormatChecksum || c.Format == FormatSignature || c.Format == FormatOther || c.Format == FormatDmg || IsSourceAsset(n) {
			continue
		}
		if c.OS == "" || len(c.Arches) != 1 {
			continue
		}
		scored, ok := Score(n, platform.Platform{OS: c.OS, Arch: c.Arches[0]}, opts)
		if !ok {
			continue
		}
		key := c.OS + "/" + c.Arches[0]
		perPlatform[key] = append(perPlatform[key], scored)
	}
	platforms := map[string]Candidate{}
	for key, list := range perPlatform {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Score > list[j].Score })
		if best, ok := pickTop(list); ok {
			platforms[key] = best
		}
	}

	match := config.Match{Platforms: map[string]string{}}
	for key, c := range platforms {
		match.Platforms[key] = Templatize(c.Name, tag, version)
	}
	if len(match.Platforms) > 0 {
		// Make sure the chosen asset has an entry even if its name lacks an
		// OS token (common for single-platform projects).
		key := target.OS + "/" + target.Arch
		if _, ok := match.Platforms[key]; !ok {
			match.Platforms[key] = Templatize(chosen.Name, tag, version)
		}
	}
	if len(match.Platforms) == 0 {
		match.Platforms = map[string]string{target.String(): Templatize(chosen.Name, tag, version)}
	}

	inst := config.Install{Type: string(chosen.Format)}
	return Generated{Match: match, Install: inst, Chosen: chosen, Platforms: platforms}, nil
}

// Templatize replaces the release tag and version in an asset name with
// placeholders so the config keeps working after upgrades.
func Templatize(name, tag, version string) string {
	out := name
	if tag != "" {
		out = strings.ReplaceAll(out, tag, "{tag}")
	}
	if version != "" && version != tag {
		out = strings.ReplaceAll(out, version, "{version}")
	}
	return out
}

// SortedPlatforms returns map keys in stable order for display and YAML.
func SortedPlatforms(m map[string]Candidate) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ChecksumAsset picks the best checksum manifest for an asset. It prefers a
// per-asset digest file, then a global manifest.
func ChecksumAsset(names []string, assetName string) string {
	base := strings.ToLower(assetName)
	best, bestScore := "", 0
	for _, n := range names {
		if !IsChecksumAsset(n) || IsSignatureAsset(n) {
			continue
		}
		lower := strings.ToLower(n)
		score := 0
		switch {
		case lower == base+".sha256", lower == base+".sha256sum", lower == base+".sha512":
			score = 100
		case strings.Contains(lower, "checksum"):
			score = 80
		case strings.Contains(lower, "sha256sum"), strings.Contains(lower, "sha256sums"):
			score = 70
		case strings.HasPrefix(lower, "sha256"):
			score = 60
		case strings.HasSuffix(lower, ".sha256"), strings.HasSuffix(lower, ".sha256sum"):
			score = 40
		default:
			score = 10
		}
		if score > bestScore {
			best, bestScore = n, score
		}
	}
	return best
}
