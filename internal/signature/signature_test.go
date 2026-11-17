package signature

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// minisignTestKey builds the on-disk representations of a minisign key pair.
func minisignTestKey(t *testing.T) (pub ed25519.PublicKey, priv ed25519.PrivateKey, keyID []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID = make([]byte, 8)
	if _, err := rand.Read(keyID); err != nil {
		t.Fatal(err)
	}
	return pub, priv, keyID
}

func minisignPublicFile(pub ed25519.PublicKey, keyID []byte) []byte {
	blob := append(append([]byte("Ed"), keyID...), pub...)
	return []byte("untrusted comment: minisign public key\n" + b64(blob) + "\n")
}

func minisignSignatureFile(t *testing.T, priv ed25519.PrivateKey, keyID, data []byte, prehashed bool, trusted string) []byte {
	t.Helper()
	alg := []byte("ED")
	msg := data
	if prehashed {
		alg = []byte("Ed")
		sum := blake2b.Sum512(data)
		msg = sum[:]
	}
	sig := ed25519.Sign(priv, msg)
	blob := append(append(append([]byte{}, alg...), keyID...), sig...)
	out := "untrusted comment: signature from minisign secret key\n" + b64(blob) + "\n"
	if trusted != "" {
		out += "trusted comment: " + trusted + "\n"
		global := ed25519.Sign(priv, append(append([]byte{}, sig...), []byte(trusted)...))
		out += b64(global) + "\n"
	}
	return []byte(out)
}

func TestVerifyMinisignPrehashedAndLegacy(t *testing.T) {
	pub, priv, keyID := minisignTestKey(t)
	dir := t.TempDir()
	data := []byte("abc123  ghpm.tar.gz\n")
	dataPath := writeFile(t, dir, "SHA256SUMS", data)
	pubPath := writeFile(t, dir, "minisign.pub", minisignPublicFile(pub, keyID))

	for _, prehashed := range []bool{true, false} {
		sigPath := writeFile(t, dir, "sig", minisignSignatureFile(t, priv, keyID, data, prehashed, ""))
		if err := VerifyMinisign(pubPath, sigPath, dataPath); err != nil {
			t.Fatalf("prehashed=%v: %v", prehashed, err)
		}
		if err := VerifyMinisign(string(minisignPublicFile(pub, keyID)), sigPath, dataPath); err != nil {
			t.Fatalf("inline key prehashed=%v: %v", prehashed, err)
		}
	}
}

func TestVerifyMinisignTrustedComment(t *testing.T) {
	pub, priv, keyID := minisignTestKey(t)
	dir := t.TempDir()
	data := []byte("payload")
	dataPath := writeFile(t, dir, "checksums.txt", data)
	sigPath := writeFile(t, dir, "sig", minisignSignatureFile(t, priv, keyID, data, true, "timestamp:1700000000\tfile:checksums.txt"))
	if err := VerifyMinisign(b64(append(append([]byte("Ed"), keyID...), pub...)), sigPath, dataPath); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyMinisignRejectsTamperedFile(t *testing.T) {
	pub, priv, keyID := minisignTestKey(t)
	dir := t.TempDir()
	sigPath := writeFile(t, dir, "sig", minisignSignatureFile(t, priv, keyID, []byte("original"), true, ""))
	dataPath := writeFile(t, dir, "checksums.txt", []byte("original"))
	if err := VerifyMinisign(b64(append(append([]byte("Ed"), keyID...), pub...)), sigPath, dataPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyMinisign(b64(append(append([]byte("Ed"), keyID...), pub...)), sigPath, dataPath); err == nil {
		t.Fatal("expected verification to fail for tampered file")
	}
}

func TestVerifyMinisignKeyIDMismatch(t *testing.T) {
	_, priv, keyID := minisignTestKey(t)
	otherPub, _, otherID := minisignTestKey(t)
	dir := t.TempDir()
	dataPath := writeFile(t, dir, "checksums.txt", []byte("data"))
	sigPath := writeFile(t, dir, "sig", minisignSignatureFile(t, priv, keyID, []byte("data"), true, ""))
	err := VerifyMinisign(b64(append(append([]byte("Ed"), otherID...), otherPub...)), sigPath, dataPath)
	if err == nil || !strings.Contains(err.Error(), "key id") {
		t.Fatalf("want key id mismatch, got %v", err)
	}
}

func TestFindAsset(t *testing.T) {
	names := []string{"foo.tar.gz", "SHA256SUMS", "SHA256SUMS.sig", "SHA256SUMS.minisig", "other.asc"}
	cases := map[string]string{
		"SHA256SUMS":          "SHA256SUMS.sig",
		"checksums.txt":       "",
		"foo.tar.gz.sha256":   "",
		"FOO.TAR.GZ":          "",
		"sha256sums":          "SHA256SUMS.sig",
		"something-else":      "",
		"nested/SHA256SUMS":   "SHA256SUMS.sig",
		"other.txt":           "",
		"release/SHA256SUMS":  "SHA256SUMS.sig",
		"not-in-the-list.txt": "",
	}
	for signed, want := range cases {
		if got := FindAsset(names, signed); got != want {
			t.Errorf("FindAsset(%q) = %q, want %q", signed, got, want)
		}
	}
}

func TestAlgorithmInference(t *testing.T) {
	cases := []struct {
		cfg   Config
		asset string
		want  string
	}{
		{Config{Type: "gpg"}, "x.sig", "gpg"},
		{Config{}, "x.minisig", "minisign"},
		{Config{}, "x.asc", "gpg"},
		{Config{}, "x.sig", "minisign"},
		{Config{Type: "minisign"}, "x.asc", "minisign"},
	}
	for _, c := range cases {
		if got := c.cfg.Algorithm(c.asset); got != c.want {
			t.Errorf("Algorithm(%+v, %q) = %q, want %q", c.cfg, c.asset, got, c.want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	if err := (Config{Type: "minisign", Mode: "required"}).Validate("app"); err == nil {
		t.Fatal("expected minisign required without key to fail validation")
	}
	if err := (Config{Type: "minisign", Mode: "required", Key: "RW..."}).Validate("app"); err != nil {
		t.Fatal(err)
	}
	if err := (Config{Type: "cosign"}).Validate("app"); err == nil {
		t.Fatal("expected unknown type to fail")
	}
	if (Config{}).NormalizedMode() != ModeIfAvailable {
		t.Fatal("default mode must be if-available")
	}
	if (Config{Mode: "skip"}).Enabled() {
		t.Fatal("skip must disable verification")
	}
	if (Config{}).Enabled() {
		t.Fatal("an absent signature block must leave verification off")
	}
	if !(Config{Mode: "required"}).Enabled() {
		t.Fatal("an explicit mode must enable verification")
	}
}

func TestVerifyGPGWithEphemeralKeyring(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "gnupg")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command("gpg", append([]string{"--homedir", home, "--batch", "--pinentry-mode", "loopback", "--passphrase", ""}, args...)...)
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	if _, err := run("--quick-generate-key", "ghpm test <test@example.com>", "rsa2048", "sign", "0"); err != nil {
		t.Skipf("cannot generate test key: %v", err)
	}
	dataPath := writeFile(t, dir, "SHA256SUMS", []byte("abc  file\n"))
	sigPath := filepath.Join(dir, "SHA256SUMS.asc")
	if out, err := run("--armor", "--detach-sign", "-o", sigPath, dataPath); err != nil {
		t.Skipf("cannot sign: %v %s", err, out)
	}
	t.Setenv("GNUPGHOME", home)
	if err := VerifyGPG(t.Context(), "", sigPath, dataPath); err != nil {
		t.Fatal(err)
	}

	// The public key can also be supplied as a file, which is imported into
	// a throwaway keyring; the original keyring is then irrelevant.
	keyPath := filepath.Join(dir, "maintainer.asc")
	exportCmd := exec.Command("gpg", "--homedir", home, "--batch", "--armor", "--export", "test@example.com")
	keyData, err := exportCmd.Output()
	if err != nil || !bytes.Contains(keyData, []byte("BEGIN PGP PUBLIC KEY BLOCK")) {
		t.Fatalf("export key: %v (%d bytes)", err, len(keyData))
	}
	if err := os.WriteFile(keyPath, keyData, 0o600); err != nil {
		t.Fatal(err)
	}
	emptyHome := filepath.Join(dir, "empty")
	if err := os.MkdirAll(emptyHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GNUPGHOME", emptyHome)
	if err := VerifyGPG(t.Context(), keyPath, sigPath, dataPath); err != nil {
		t.Fatalf("key-file verification: %v", err)
	}

	if err := os.WriteFile(dataPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyGPG(t.Context(), keyPath, sigPath, dataPath); err == nil {
		t.Fatal("expected gpg verification to reject tampered data")
	}
}
