package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"golang.org/x/crypto/nacl/box"
)

var testKey *signingKey

// generateTestKey generates one 4096-bit key for all the signing tests, since that's slow
func generateTestKey(t *testing.T) *signingKey {
	t.Helper()
	if testKey == nil {
		key, err := generateKey("proj release signing", "o/proj", 4096, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		testKey = key
	}
	return testKey
}

func TestGenerateKey(t *testing.T) {
	key := generateTestKey(t)
	entity := readPrivateKey(t, key.ArmoredPrivate, key.Passphrase)

	if key.UserID != "proj release signing (o/proj)" || entity.PrimaryIdentity().UserId.Email != "" {
		t.Errorf("user ID %q, email %q", key.UserID, entity.PrimaryIdentity().UserId.Email)
	}
	if bits, _ := entity.PrimaryKey.BitLength(); entity.PrimaryKey.PubKeyAlgo != packet.PubKeyAlgoRSA || bits != 4096 {
		t.Errorf("key is algorithm %v with %d bits", entity.PrimaryKey.PubKeyAlgo, bits)
	}
	if len(entity.Subkeys) != 0 || len(entity.Revocations) != 0 {
		t.Errorf("%d subkeys, %d revocations", len(entity.Subkeys), len(entity.Revocations))
	}
	if expired := entity.PrimaryKey.KeyExpired(entity.PrimaryIdentity().SelfSignature, time.Now().AddDate(50, 0, 0)); expired {
		t.Error("the key expires")
	}
	if len(key.Fingerprint) != 40 || !strings.HasSuffix(key.Fingerprint, key.keyID()) {
		t.Errorf("fingerprint %q, key ID %q", key.Fingerprint, key.keyID())
	}

	// The key can sign
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, entity, strings.NewReader("hello"), nil); err != nil {
		t.Fatal(err)
	}
	public, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.ArmoredPublic))
	if err != nil {
		t.Fatal(err)
	}
	if public[0].PrivateKey != nil {
		t.Error("the public key contains the private key")
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(public, strings.NewReader("hello"), &sig, nil); err != nil {
		t.Errorf("signature doesn't verify: %v", err)
	}
}

func TestRevocationCertificate(t *testing.T) {
	key := generateTestKey(t)
	if !strings.Contains(key.Revocation, key.Fingerprint) || !strings.Contains(key.Revocation, "\n:-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Fatalf("revocation certificate:\n%s", key.Revocation)
	}
	_, armored, _ := strings.Cut(key.Revocation, "\n:")
	block, err := armor.Decode(strings.NewReader(armored))
	if err != nil {
		t.Fatal(err)
	}
	p, err := packet.Read(block.Body)
	if err != nil {
		t.Fatal(err)
	}
	sig, ok := p.(*packet.Signature)
	if !ok || sig.SigType != packet.SigTypeKeyRevocation {
		t.Fatalf("packet is %T, not a key revocation signature", p)
	}
	public, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.ArmoredPublic))
	if err != nil {
		t.Fatal(err)
	}
	if err := public[0].PrimaryKey.VerifyRevocationSignature(sig); err != nil {
		t.Errorf("revocation signature doesn't verify: %v", err)
	}
}

// TestGPGCompatibility checks that gpg, which actions/setup-java uses to import the key, can
// import and sign with it, and can revoke it with the revocation certificate
func TestGPGCompatibility(t *testing.T) {
	if testing.Short() {
		t.Skip("gpg is slow to start on Windows")
	}
	gpg, err := exec.LookPath("gpg")
	if err != nil {
		t.Skip("gpg is not installed")
	}
	key := generateTestKey(t)
	dir := t.TempDir()
	// The gpg in Git for Windows is an MSYS program, whose agent can't use a Windows path
	gpgPath := filepath.ToSlash
	if cygpath, err := exec.LookPath("cygpath"); err == nil {
		gpgPath = func(path string) string {
			out, err := exec.Command(cygpath, "-u", path).Output()
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSpace(string(out))
		}
	}
	home := gpgPath(dir)
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gpg, append([]string{"--homedir", home, "--batch", "--no-tty"}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("gpg %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	t.Cleanup(func() {
		_ = exec.Command(filepath.Join(filepath.Dir(gpg), "gpgconf"), "--homedir", home, "--kill", "gpg-agent").Run()
	})

	run(key.ArmoredPrivate, "--import")
	data := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(data, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(key.Passphrase, "--pinentry-mode", "loopback", "--passphrase-fd", "0",
		"--armor", "--detach-sign", gpgPath(data))
	if out := run("", "--verify", gpgPath(data+".asc"), gpgPath(data)); !strings.Contains(out, "Good signature") {
		t.Errorf("gpg --verify:\n%s", out)
	}

	revocation := strings.Replace(key.Revocation, "\n:-----BEGIN", "\n-----BEGIN", 1)
	run(revocation, "--import")
	if out := run("", "--with-colons", "--list-keys", key.Fingerprint); !strings.Contains(out, "\npub:r:") {
		t.Errorf("key isn't revoked after importing the revocation certificate:\n%s", out)
	}
}

func TestSealSecret(t *testing.T) {
	public, private, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSecret(base64.StdEncoding.EncodeToString(public[:]), "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(sealed)
	if value, ok := box.OpenAnonymous(nil, raw, public, private); !ok || string(value) != "s3cret" {
		t.Errorf("opened %q, %v", value, ok)
	}
	if _, err := sealSecret(base64.StdEncoding.EncodeToString([]byte("short")), "x"); err == nil {
		t.Error("a short public key was accepted")
	}
}
