package signed

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestASignatureIsCheckedAgainstTheKeyItWasOpenedWith, for each kind of key a
// checkpoint is signed with, and refused when it is another key's.
func TestASignatureIsCheckedAgainstTheKeyItWasOpenedWith(t *testing.T) {
	x := require.New(t)

	msg := []byte("a checkpoint")
	d := sha256.Sum256(msg)

	_, ed, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)
	ec, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	x.NoError(err)
	rs, err := rsa.GenerateKey(rand.Reader, 2048)
	x.NoError(err)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)

	for _, c := range []struct {
		key    crypto.Signer
		digest []byte
		opts   crypto.SignerOpts
	}{
		{ed, msg, crypto.Hash(0)},
		{ec, d[:], crypto.SHA256},
		{rs, d[:], crypto.SHA256},
		{rs, d[:], &rsa.PSSOptions{Hash: crypto.SHA256, SaltLength: rsa.PSSSaltLengthEqualsHash}},
	} {
		sig, err := c.key.Sign(rand.Reader, c.digest, c.opts)
		x.NoError(err)
		x.NoError(Check(c.key.Public(), c.digest, c.opts, sig))

		sig[len(sig)/2] ^= 1
		x.ErrorIs(Check(c.key.Public(), c.digest, c.opts, sig), ErrMismatch)
	}

	sig, err := ed.Sign(rand.Reader, msg, crypto.Hash(0))
	x.NoError(err)
	x.ErrorIs(Check(other.Public(), msg, crypto.Hash(0), sig), ErrMismatch, "another key's")
}

// TestRAndSAreWhatAVerifierReads: the two halves PKCS #11 hands back are the
// ASN.1 crypto/ecdsa verifies.
func TestRAndSAreWhatAVerifierReads(t *testing.T) {
	x := require.New(t)

	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x.NoError(err)
	d := sha256.Sum256([]byte("a checkpoint"))
	r, s, err := ecdsa.Sign(rand.Reader, k, d[:])
	x.NoError(err)

	rs := make([]byte, 64)
	r.FillBytes(rs[:32])
	s.FillBytes(rs[32:])
	sig, err := Halves(rs)
	x.NoError(err)
	x.True(ecdsa.VerifyASN1(&k.PublicKey, d[:], sig))

	_, err = Halves(rs[:63])
	x.Error(err)
	_, err = Halves(nil)
	x.Error(err)

	// And a half with leading zeros is the same integer.
	sig, err = ECDSA([]byte{0, 0, 1}, []byte{2})
	x.NoError(err)
	want, err := ECDSA(big.NewInt(1).Bytes(), big.NewInt(2).Bytes())
	x.NoError(err)
	x.Equal(want, sig)
}

// TestWhatASignerIsHandedIsWhatItSays: a digest the size its hash is, a
// message for Ed25519, and nothing a device here does not sign.
func TestWhatASignerIsHandedIsWhatItSays(t *testing.T) {
	x := require.New(t)

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x.NoError(err)
	ed, _, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)
	d := sha256.Sum256(nil)

	h, err := Hash(ec.Public(), d[:], crypto.SHA256)
	x.NoError(err)
	x.Equal(crypto.SHA256, h)
	_, err = Hash(ec.Public(), d[:31], crypto.SHA256)
	x.ErrorContains(err, "31 bytes")
	_, err = Hash(ec.Public(), d[:20], crypto.SHA1)
	x.ErrorContains(err, "SHA-1")

	h, err = Hash(ed, []byte("anything at all"), crypto.Hash(0))
	x.NoError(err)
	x.Zero(h)
	_, err = Hash(ed, d[:], crypto.SHA256)
	x.ErrorContains(err, "message itself")
	_, err = Hash(ed, d[:], &ed25519.Options{Context: "x"})
	x.ErrorContains(err, "context")
}
