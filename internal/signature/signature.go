// Package signature verifies detached signatures over release checksum
// manifests. Minisign signatures are verified natively (Ed25519, with
// BLAKE2b-512 pre-hashing for the default "Ed" algorithm); OpenPGP
// signatures are handed to the system gpg binary, which keeps key management
// where users already expect it.
package signature

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Mode controls what happens when a signature cannot be checked.
type Mode string

const (
	ModeSkip        Mode = "skip"
	ModeIfAvailable Mode = "if-available"
	ModeRequired    Mode = "required"
)

// Config is the per-app signature policy from config.yaml.
type Config struct {
	Type  string `yaml:"type,omitempty"`  // minisign | gpg | "" (inferred)
	Key   string `yaml:"key,omitempty"`   // minisign public key (inline/file) or gpg fingerprint
	Asset string `yaml:"asset,omitempty"` // explicit signature asset name
	Mode  string `yaml:"mode,omitempty"`  // if-available | required | skip
}

// NormalizedMode returns the effective mode.
func (c Config) NormalizedMode() Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(c.Mode))) {
	case ModeSkip:
		return ModeSkip
	case ModeRequired:
		return ModeRequired
	case ModeIfAvailable, "":
		return ModeIfAvailable
	default:
		return ModeIfAvailable
	}
}

// Configured reports whether the app asked for signature verification at
// all. An absent signature block leaves verification off.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.Type) != "" || strings.TrimSpace(c.Key) != "" ||
		strings.TrimSpace(c.Asset) != "" || strings.TrimSpace(c.Mode) != ""
}

// Enabled reports whether verification should run.
func (c Config) Enabled() bool {
	return c.Configured() && c.NormalizedMode() != ModeSkip
}

// Validate checks configuration before any network work happens.
func (c Config) Validate(appID string) error {
	switch strings.ToLower(strings.TrimSpace(c.Type)) {
	case "", "minisign", "gpg", "pgp", "openpgp":
	default:
		return fmt.Errorf("app %q: signature.type must be minisign or gpg", appID)
	}
	switch c.NormalizedMode() {
	case ModeSkip, ModeIfAvailable, ModeRequired:
	default:
		return fmt.Errorf("app %q: signature.mode must be if-available, required or skip", appID)
	}
	if c.NormalizedMode() == ModeRequired && strings.EqualFold(strings.TrimSpace(c.Type), "minisign") && strings.TrimSpace(c.Key) == "" {
		return fmt.Errorf("app %q: signature.key is required for minisign verification", appID)
	}
	return nil
}

// CandidateNames returns the signature asset names to look for, given the
// signed file's asset name.
func CandidateNames(signed string) []string {
	base := filepath.Base(signed)
	candidates := []string{
		base + ".sig",
		base + ".minisig",
		base + ".asc",
		base + ".gpg",
	}
	// Some projects publish a single detached signature without repeating
	// an extension, e.g. "checksums.txt" + "checksums.sig".
	if ext := filepath.Ext(base); ext != "" {
		candidates = append(candidates, strings.TrimSuffix(base, ext)+".sig",
			strings.TrimSuffix(base, ext)+".minisig")
	}
	return candidates
}

// FindAsset returns the first candidate present in names, or "".
func FindAsset(names []string, signed string) string {
	byLower := make(map[string]string, len(names))
	for _, n := range names {
		byLower[strings.ToLower(n)] = n
	}
	for _, c := range CandidateNames(signed) {
		if n, ok := byLower[strings.ToLower(c)]; ok {
			return n
		}
	}
	return ""
}

// Algorithm returns the configured type, or infers it from the signature
// asset's extension.
func (c Config) Algorithm(sigAsset string) string {
	t := strings.ToLower(strings.TrimSpace(c.Type))
	switch t {
	case "gpg", "pgp", "openpgp":
		return "gpg"
	case "minisign":
		return "minisign"
	}
	switch strings.ToLower(filepath.Ext(sigAsset)) {
	case ".minisig":
		return "minisign"
	case ".asc", ".gpg":
		return "gpg"
	case ".sig":
		// Minisign is the common producer of bare .sig files for release
		// checksums; if the contents are OpenPGP we fall back to gpg on
		// parse failure.
		return "minisign"
	}
	return "minisign"
}

// Verify checks sigPath against dataPath.
func Verify(ctx context.Context, cfg Config, sigAsset, sigPath, dataPath string) error {
	if !cfg.Enabled() {
		return nil
	}
	alg := cfg.Algorithm(sigAsset)
	if alg == "gpg" {
		return VerifyGPG(ctx, cfg.Key, sigPath, dataPath)
	}
	if err := VerifyMinisign(cfg.Key, sigPath, dataPath); err != nil {
		// A .sig file may actually be an ASCII-armoured OpenPGP signature.
		if isPGPSignature(sigPath) {
			return VerifyGPG(ctx, cfg.Key, sigPath, dataPath)
		}
		return err
	}
	return nil
}

func isPGPSignature(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 64)
	n, _ := f.Read(buf)
	return bytes.Contains(buf[:n], []byte("-----BEGIN PGP SIGNATURE-----"))
}

// VerifyMinisign checks a minisign signature file. key is a base64 public
// key, a public key file path, or the whole public key file content; when
// empty, the embedded key id is still checked against the signature but the
// key itself is unknown, so verification fails with a clear error.
func VerifyMinisign(key, sigPath, dataPath string) error {
	pub, keyID, err := loadPublicKey(key)
	if err != nil {
		return err
	}
	sig, sigKeyID, trusted, global, err := readSignature(sigPath)
	if err != nil {
		return err
	}
	if keyID != sigKeyID {
		return fmt.Errorf("signature key id %x does not match configured key %x", sigKeyID, keyID)
	}
	data, err := os.ReadFile(dataPath)
	if err != nil {
		return err
	}
	message := data
	if sig.alg == "Ed" {
		sum := blake2b.Sum512(data)
		message = sum[:]
	} else if sig.alg != "ED" {
		return fmt.Errorf("unsupported minisign algorithm %q", sig.alg)
	}
	if !ed25519.Verify(pub, message, sig.bytes) {
		return errors.New("minisign signature does not match the file")
	}
	if len(global) > 0 && trusted != "" {
		// The trusted comment binds metadata (timestamp, filename) to the
		// signature. minisign signs the raw comment text, but some emitters
		// include the "trusted comment: " prefix, so accept either form.
		if !ed25519.Verify(pub, append(append([]byte{}, sig.bytes...), []byte(trusted)...), global) &&
			!ed25519.Verify(pub, append(append([]byte{}, sig.bytes...), []byte("trusted comment: "+trusted)...), global) {
			return errors.New("minisign trusted comment signature does not match")
		}
	}
	return nil
}

type minisig struct {
	alg   string
	bytes []byte
}

func readSignature(path string) (sig minisig, keyID [8]byte, trusted string, global []byte, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return sig, keyID, "", nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var b64 []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "untrusted comment:"):
		case strings.HasPrefix(line, "trusted comment:"):
			trusted = strings.TrimSpace(strings.TrimPrefix(line, "trusted comment:"))
		default:
			b64 = append(b64, line)
		}
	}
	if len(b64) == 0 {
		return sig, keyID, "", nil, errors.New("not a minisign signature (no base64 payload)")
	}
	blob, err := base64.StdEncoding.DecodeString(b64[0])
	if err != nil {
		return sig, keyID, "", nil, fmt.Errorf("decode minisign signature: %w", err)
	}
	if len(blob) != 2+8+ed25519.SignatureSize {
		return sig, keyID, "", nil, fmt.Errorf("unexpected minisign signature length %d", len(blob))
	}
	sig.alg = string(blob[:2])
	copy(keyID[:], blob[2:10])
	sig.bytes = blob[10:]
	if len(b64) > 1 {
		if g, err := base64.StdEncoding.DecodeString(b64[1]); err == nil && len(g) == ed25519.SignatureSize {
			global = g
		}
	}
	return sig, keyID, trusted, global, nil
}

func loadPublicKey(src string) (ed25519.PublicKey, [8]byte, error) {
	var keyID [8]byte
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, keyID, errors.New("no minisign public key configured; set signature.key")
	}
	content := src
	// A bare path is more likely than a key that happens to be a valid
	// relative path, so only read it when the file exists.
	if !strings.Contains(src, "\n") {
		if info, err := os.Stat(src); err == nil && !info.IsDir() {
			data, err := os.ReadFile(src)
			if err != nil {
				return nil, keyID, fmt.Errorf("read minisign public key: %w", err)
			}
			content = string(data)
		}
	}
	var payload string
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "untrusted comment:") {
			continue
		}
		payload = line
		break
	}
	if payload == "" {
		return nil, keyID, errors.New("empty minisign public key")
	}
	blob, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, keyID, fmt.Errorf("decode minisign public key: %w", err)
	}
	if len(blob) != 2+8+ed25519.PublicKeySize {
		return nil, keyID, fmt.Errorf("unexpected minisign public key length %d", len(blob))
	}
	copy(keyID[:], blob[2:10])
	return ed25519.PublicKey(blob[10:]), keyID, nil
}

// VerifyGPG verifies an OpenPGP signature using the system gpg binary. key,
// when set, is either a fingerprint/key id the signature must be made by, or
// a public key file (or inline "BEGIN PGP PUBLIC KEY BLOCK" text) that is
// imported into a temporary keyring for the check.
func VerifyGPG(ctx context.Context, key, sigPath, dataPath string) error {
	if _, err := exec.LookPath("gpg"); err != nil {
		return errors.New("gpg is not installed; install gnupg or set signature.mode: skip")
	}
	key = strings.TrimSpace(key)
	home := ""
	expected := ""
	if keyMaterial, ok := readKeyMaterial(key); ok {
		dir, err := os.MkdirTemp("", "ghpm-gpg-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		keyFile := filepath.Join(dir, "key.asc")
		if err := os.WriteFile(keyFile, keyMaterial, 0o600); err != nil {
			return err
		}
		importCmd := exec.CommandContext(ctx, "gpg", "--homedir", dir, "--batch", "--quiet", "--import", keyFile)
		var importErr bytes.Buffer
		importCmd.Stderr = &importErr
		if err := importCmd.Run(); err != nil {
			return fmt.Errorf("import gpg key: %s", firstLine(strings.TrimSpace(importErr.String())))
		}
		home = dir
	} else {
		expected = normalizeFingerprint(key)
	}

	args := []string{"--batch", "--status-fd=2"}
	if home != "" {
		args = append(args, "--homedir", home)
	}
	args = append(args, "--verify", sigPath, dataPath)
	cmd := exec.CommandContext(ctx, "gpg", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	status := stderr.String() + stdout.String()
	signer := gpgValidSig(status)
	if err != nil || signer == "" {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		hint := ""
		if strings.Contains(strings.ToLower(msg), "no public key") {
			hint = "; import the maintainer key first (gpg --recv-keys <key id>) or set signature.key to a key file"
		}
		return fmt.Errorf("gpg verification failed: %s%s", firstLine(cleanGPGStatus(msg)), hint)
	}
	if expected != "" && !strings.HasSuffix(normalizeFingerprint(signer), expected) {
		return fmt.Errorf("signature made by %s, expected %s", signer, key)
	}
	return nil
}

// readKeyMaterial reports whether key is public key material (inline or a
// file path) rather than a fingerprint.
func readKeyMaterial(key string) ([]byte, bool) {
	if key == "" {
		return nil, false
	}
	if strings.Contains(key, "BEGIN PGP PUBLIC KEY BLOCK") {
		return []byte(key), true
	}
	if strings.Contains(key, "\n") {
		return nil, false
	}
	if info, err := os.Stat(key); err == nil && !info.IsDir() {
		data, err := os.ReadFile(key)
		if err == nil && bytes.Contains(data, []byte("BEGIN PGP PUBLIC KEY BLOCK")) {
			return data, true
		}
	}
	return nil, false
}

// cleanGPGStatus strips gpg's machine-readable status lines from a message.
func cleanGPGStatus(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[GNUPG:]") {
			continue
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "; ")
}

func gpgValidSig(status string) string {
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "[GNUPG:]" && fields[1] == "VALIDSIG" {
			return fields[2]
		}
	}
	return ""
}

func normalizeFingerprint(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	if strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
