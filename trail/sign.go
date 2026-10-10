package trail

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// What signs a checkpoint is a [crypto.Signer], so that the key can be where
// the database's and the archive's credentials are not: a file read at start
// is [ParseKey], and a TPM, an HSM or a cloud KMS is a signer of its own,
// handed in as [Policy.Signer] -- none of which give the private half out.
//
// Ed25519 signs a checkpoint's bytes themselves. ECDSA and RSA sign their
// SHA-256, which is what such a device is asked for and what most of them do:
// a TPM, for one, signs with ECDSA P-256 or RSA and seldom with Ed25519. A
// signature is verified by the kind of key that made it.

// signable refuses a key a checkpoint cannot be signed or verified with.
func signable(k crypto.PublicKey) error {
	switch k := k.(type) {
	case ed25519.PublicKey:
		return nil
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256(), elliptic.P384(), elliptic.P521():
			return nil
		}

		return fmt.Errorf("an ECDSA key on %s, and checkpoints are signed on P-256, P-384 or P-521", k.Curve.Params().Name)
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return fmt.Errorf("an RSA key of %d bits, and a checkpoint's is 2048 at least", k.N.BitLen())
		}

		return nil
	}

	return fmt.Errorf("a %T, and checkpoints are signed with Ed25519, ECDSA or RSA", k)
}

// sign signs a checkpoint's bytes, and answers the signer's public half as an
// envelope carries it.
func sign(k crypto.Signer, b []byte) ([]byte, []byte, error) {
	pub := k.Public()
	if err := signable(pub); err != nil {
		return nil, nil, err
	}

	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}

	var sig []byte
	if _, ok := pub.(ed25519.PublicKey); ok {
		sig, err = k.Sign(rand.Reader, b, crypto.Hash(0))
	} else {
		d := sha256.Sum256(b)
		sig, err = k.Sign(rand.Reader, d[:], crypto.SHA256)
	}
	if err != nil {
		return nil, nil, err
	}

	return der, sig, nil
}

// verified answers whether `sig` is a signature over `b` by the key whose
// public half is `der`.
func verified(der, b, sig []byte) bool {
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return false
	}

	switch k := k.(type) {
	case ed25519.PublicKey:
		return ed25519.Verify(k, b, sig)
	case *ecdsa.PublicKey:
		d := sha256.Sum256(b)
		return ecdsa.VerifyASN1(k, d[:], sig)
	case *rsa.PublicKey:
		// PKCS #1 v1.5 is what a signer is asked for, and PSS what a device
		// whose key allows nothing else answers with instead.
		d := sha256.Sum256(b)
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, d[:], sig) == nil ||
			rsa.VerifyPSS(k, crypto.SHA256, d[:], sig, nil) == nil
	}

	return false
}

// trusts answers whether `der` is one of these keys.
func trusts(keys []crypto.PublicKey, der []byte) bool {
	for _, k := range keys {
		if v, err := x509.MarshalPKIXPublicKey(k); err == nil && bytes.Equal(v, der) {
			return true
		}
	}

	return false
}

// GenerateKey makes an Ed25519 key a deployment signs its checkpoints with, as
// the PEM document [ParseKey] reads, and its public half as [Policy.Trust]
// lists it.
func GenerateKey() ([]byte, string, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, "", err
	}

	v, err := PublicKeyString(pub)
	if err != nil {
		return nil, "", err
	}

	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), v, nil
}

// ParseKey reads a private key from a PEM document: PKCS #8 of Ed25519, ECDSA
// or RSA, as `openssl genpkey` writes one, or the older `EC PRIVATE KEY` and
// `RSA PRIVATE KEY` documents.
func ParseKey(doc []byte) (crypto.Signer, error) {
	b, _ := pem.Decode(doc)
	if b == nil {
		return nil, errors.New("not a PEM document")
	}

	var v any
	var err error
	switch b.Type {
	case "PRIVATE KEY":
		v, err = x509.ParsePKCS8PrivateKey(b.Bytes)
	case "EC PRIVATE KEY":
		v, err = x509.ParseECPrivateKey(b.Bytes)
	case "RSA PRIVATE KEY":
		v, err = x509.ParsePKCS1PrivateKey(b.Bytes)
	default:
		return nil, fmt.Errorf("a PEM document of %s, and a key is a PRIVATE KEY", b.Type)
	}
	if err != nil {
		return nil, err
	}

	k, ok := v.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("a %T, which signs nothing", v)
	}
	if err := signable(k.Public()); err != nil {
		return nil, err
	}

	return k, nil
}

// PublicKeyString is a public key as a configuration lists one: PKIX, base64
// -- what `openssl pkey -pubout -outform DER | base64 -w0` writes.
func PublicKeyString(k crypto.PublicKey) (string, error) {
	if err := signable(k); err != nil {
		return "", err
	}

	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(der), nil
}

// ParsePublicKey reads one back, or the PEM document of a `PUBLIC KEY`.
func ParsePublicKey(v string) (crypto.PublicKey, error) {
	v = strings.TrimSpace(v)

	var der []byte
	if b, _ := pem.Decode([]byte(v)); b != nil {
		if b.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("a PEM document of %s, and a public key is a PUBLIC KEY", b.Type)
		}

		der = b.Bytes
	} else {
		var err error
		if der, err = base64.StdEncoding.DecodeString(v); err != nil {
			return nil, err
		}
	}

	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	if err := signable(k); err != nil {
		return nil, err
	}

	return k, nil
}
