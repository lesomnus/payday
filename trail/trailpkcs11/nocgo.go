//go:build !cgo

package trailpkcs11

import (
	"crypto"
	"errors"
	"io"
)

var errNoCgo = errors.New("this binary is built without cgo -- CGO_ENABLED=0, or for js/wasm -- and a PKCS #11 module is a C library it cannot load")

// Signer is a private key in a PKCS #11 token. Built without cgo, there is
// none: [Open] refuses.
type Signer struct{}

var (
	_ crypto.Signer = (*Signer)(nil)
	_ io.Closer     = (*Signer)(nil)
)

// Open refuses: see [Signer].
func Open(string) (*Signer, error) { return nil, errNoCgo }

func (*Signer) Public() crypto.PublicKey { return nil }

func (*Signer) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) { return nil, errNoCgo }

func (*Signer) Close() error { return nil }
