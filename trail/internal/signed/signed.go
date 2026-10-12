// Package signed is what the adapters beside it share (#52): a device's
// signature put in the form a checkpoint's verifier reads, and checked against
// the key's public half before it is handed over.
//
// The check is the adapters' whole defence against a device that signs with
// something other than the key it said it was: a token with two objects of one
// label, a persistent handle made a different key under a running process, a
// key deleted and made again under the same name. Each of those signs a
// checkpoint that [trail.Policy.Verify] calls forged, a day after it was
// written; checked here, it is a checkpoint that fails to be signed, now, with
// a reason. It costs a verification per signature, which is little beside
// asking a device for one.
package signed

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
)

// ErrMismatch is a device's signature that its key's public half does not
// verify.
var ErrMismatch = errors.New("the device signed with a key other than the one it answered for: its signature does not verify against the public half it was opened with")

// Hash answers what `digest` is, as [crypto.Signer] is handed it: the message
// itself for Ed25519, and otherwise a digest of the size `opts` says.
func Hash(pub crypto.PublicKey, digest []byte, opts crypto.SignerOpts) (crypto.Hash, error) {
	h := opts.HashFunc()
	if _, ok := pub.(ed25519.PublicKey); ok {
		if h != 0 {
			return 0, fmt.Errorf("an Ed25519 key signs a message itself, and was handed a %s digest", h)
		}
		if o, ok := opts.(*ed25519.Options); ok && o.Context != "" {
			return 0, errors.New("an Ed25519 key here signs with no context")
		}

		return 0, nil
	}

	switch h {
	case crypto.SHA256, crypto.SHA384, crypto.SHA512:
	default:
		return 0, fmt.Errorf("a %s digest, and a device here signs SHA-256, SHA-384 or SHA-512", h)
	}
	if len(digest) != h.Size() {
		return 0, fmt.Errorf("a digest of %d bytes, and %s is %d", len(digest), h, h.Size())
	}

	return h, nil
}

// Check verifies `sig` over `digest` by `pub`, as [crypto.Signer] promises it
// and as a checkpoint is verified: for RSA, PKCS #1 v1.5 or PSS, since a device
// whose key allows only PSS answers with that when asked for the other.
func Check(pub crypto.PublicKey, digest []byte, opts crypto.SignerOpts, sig []byte) error {
	ok := false
	switch k := pub.(type) {
	case ed25519.PublicKey:
		ok = ed25519.Verify(k, digest, sig)
	case *ecdsa.PublicKey:
		ok = ecdsa.VerifyASN1(k, digest, sig)
	case *rsa.PublicKey:
		h := opts.HashFunc()
		ok = rsa.VerifyPKCS1v15(k, h, digest, sig) == nil || rsa.VerifyPSS(k, h, digest, sig, nil) == nil
	default:
		return fmt.Errorf("a %T, which nothing here verifies", pub)
	}
	if !ok {
		return ErrMismatch
	}

	return nil
}

// ECDSA is a signature of r and s, as PKCS #11 and a TPM hand one back, in
// the ASN.1 form crypto/ecdsa writes and a checkpoint's verifier reads.
func ECDSA(r, s []byte) ([]byte, error) {
	return asn1.Marshal(struct{ R, S *big.Int }{
		R: new(big.Int).SetBytes(r),
		S: new(big.Int).SetBytes(s),
	})
}

// Halves is ECDSA for a device that hands back r and s as one string, each
// half as long as the other: PKCS #11's CKM_ECDSA.
func Halves(rs []byte) ([]byte, error) {
	if len(rs) == 0 || len(rs)%2 != 0 {
		return nil, fmt.Errorf("an ECDSA signature of %d bytes, which is not r and s of one length", len(rs))
	}

	return ECDSA(rs[:len(rs)/2], rs[len(rs)/2:])
}
