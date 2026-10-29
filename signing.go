package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// signingKey is a new OpenPGP signing key, ready to be stored as secrets
type signingKey struct {
	Fingerprint string
	UserID      string
	// ArmoredPrivate is the private key, encrypted with Passphrase
	ArmoredPrivate string
	ArmoredPublic  string
	Passphrase     string
	// Revocation is a revocation certificate in the format gpg writes, which must be edited
	// before it can be imported, so it isn't imported by accident
	Revocation string
}

// keyID is the long key ID: the last 16 hex digits of the fingerprint
func (k *signingKey) keyID() string {
	return k.Fingerprint[len(k.Fingerprint)-16:]
}

// generateKey generates an RSA signing key with no expiry, no subkeys, and the user ID
// "name (comment)", with no email address
func generateKey(name, comment string, bits int, now time.Time) (*signingKey, error) {
	config := &packet.Config{
		Time:          func() time.Time { return now },
		Algorithm:     packet.PubKeyAlgoRSA,
		RSABits:       bits,
		DefaultHash:   crypto.SHA256,
		DefaultCipher: packet.CipherAES256,
	}
	entity, err := openpgp.NewEntity(name, comment, "", config)
	if err != nil {
		return nil, fmt.Errorf("can't generate the key: %w", err)
	}
	// NewEntity adds an encryption subkey, which a signing key doesn't need
	entity.Subkeys = nil

	key := &signingKey{
		Fingerprint: strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint)),
		UserID:      entity.PrimaryIdentity().Name,
	}
	if key.ArmoredPublic, err = armorEncode(openpgp.PublicKeyType, nil, entity.Serialize); err != nil {
		return nil, err
	}

	// Make the revocation certificate while the private key can still sign, and don't keep the
	// revocation in the key itself
	if err := entity.RevokeKey(packet.NoReason, "", config); err != nil {
		return nil, fmt.Errorf("can't make the revocation certificate: %w", err)
	}
	revocation := entity.Revocations[0]
	entity.Revocations = nil
	if key.Revocation, err = formatRevocation(revocation, key, now); err != nil {
		return nil, err
	}

	passphrase := make([]byte, 32)
	rand.Read(passphrase)
	key.Passphrase = base64.StdEncoding.EncodeToString(passphrase)
	if err := entity.EncryptPrivateKeys([]byte(key.Passphrase), config); err != nil {
		return nil, fmt.Errorf("can't encrypt the private key: %w", err)
	}
	key.ArmoredPrivate, err = armorEncode(openpgp.PrivateKeyType, nil, func(w io.Writer) error {
		return entity.SerializePrivateWithoutSigning(w, config)
	})
	if err != nil {
		return nil, err
	}
	return key, nil
}

func armorEncode(blockType string, headers map[string]string, serialize func(io.Writer) error) (string, error) {
	var b bytes.Buffer
	w, err := armor.Encode(&b, blockType, headers)
	if err != nil {
		return "", err
	}
	if err := serialize(w); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	b.WriteString("\n")
	return b.String(), nil
}

func formatRevocation(sig *packet.Signature, key *signingKey, created time.Time) (string, error) {
	armored, err := armorEncode(openpgp.PublicKeyType,
		map[string]string{"Comment": "This is a revocation certificate"}, sig.Serialize)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`This is a revocation certificate for the OpenPGP key:

pub   rsa %s [S]
      %s
uid           %s

Importing it into gpg, along with the public key, revokes the key. Uploading the revoked public
key to a keyserver then publishes the revocation, so that signatures made with the key, including
earlier ones, are reported as made by a revoked key. Use it if the private key leaks.

So that it isn't imported by accident, delete the ':' at the start of the line below first.

:%s`, created.UTC().Format("2006-01-02"), key.Fingerprint, key.UserID, armored), nil
}
