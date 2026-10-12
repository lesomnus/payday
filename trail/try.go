package trail

import (
	"crypto"
	"crypto/rand"
	"errors"
)

// TrySigner signs the way a checkpoint is signed and verifies the way
// [Policy.Verify] does, so that a signer which cannot is found where the
// process comes up rather than at the first checkpoint, a day later, as one
// nobody can verify.
//
// It is for a key in a device above all (#52), which answers for more than the
// key: a session that logs in, a mechanism the token has, a signature encoded
// the way a verifier reads it -- ECDSA's is ASN.1, where PKCS #11 and a TPM
// hand back r and s -- and a public half that is the one the private half
// signs for. Each of those is a checkpoint [Policy.Verify] calls `forged`, and
// none of them shows until something is signed.
//
// What it signs is random, and nothing a checkpoint could be mistaken for.
func TrySigner(k crypto.Signer) error {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	b = append([]byte("payday/trail: a signer, tried\n"), b...)

	der, sig, err := sign(k, b)
	if err != nil {
		return err
	}
	if !verified(der, b, sig) {
		return errors.New("what it signs does not verify against its own public half")
	}

	return nil
}
