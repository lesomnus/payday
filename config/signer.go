package config

import (
	"crypto"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/lesomnus/payday/trail"
)

// signers holds what each registered scheme opens.
var signers = map[string]func(key string) (crypto.Signer, error){}

// shipped is the adapters payday has (#52), named here only so that a
// configuration naming one this binary does not link is told which package
// would.
var shipped = map[string]string{
	"awskms":     "github.com/lesomnus/payday/trail/trailawskms -- a module of its own, registered by the app",
	"hashivault": "github.com/lesomnus/payday/config/signervault",
	"pkcs11":     "github.com/lesomnus/payday/config/signerpkcs11",
	"tpm":        "github.com/lesomnus/payday/config/signertpm",
}

// RegisterSigner records how to open a key that `audit.checkpoints.key` names
// by its scheme -- `pkcs11:…`, `tpm:…` -- so that a key in a device is a line
// of a deployment's configuration rather than code an app writes (#52).
//
// It is [RegisterLease]'s shape, and for its reason: what talks to a device is
// a client that is linked in whether or not anything signs with it -- a C
// library loaded at run time for PKCS #11, a cloud's SDK for its KMS -- so
// each is a package of its own that an app imports:
//
//	import _ "github.com/lesomnus/payday/config/signerpkcs11"
//
// `open` is handed the whole of what was written, scheme and all, once, where
// the process comes up, and what it answers is made to sign once
// ([trail.TrySigner]) before anything is built on it. A signer that is also an
// [io.Closer] is closed when it fails to.
//
// The scheme is a URI's (RFC 3986), compared without case: a letter, then
// letters, digits, `+`, `-` or `.`. It is two characters at least, since `C:`
// begins a path.
func RegisterSigner[S crypto.Signer](scheme string, open func(key string) (S, error)) {
	if !isScheme(scheme) {
		panic(fmt.Sprintf("config.RegisterSigner: %q is not a scheme of two characters or more", scheme))
	}

	signers[strings.ToLower(scheme)] = func(key string) (crypto.Signer, error) {
		s, err := open(key)
		if err != nil {
			return nil, err
		}
		// A nil pointer is a signer to the interface, and one that would
		// panic at its first call rather than say it opened nothing.
		if v := reflect.ValueOf(s); !v.IsValid() || v.Kind() == reflect.Pointer && v.IsNil() {
			return nil, nil
		}

		return s, nil
	}
}

// Signers lists every scheme a deployment may write in
// `audit.checkpoints.key`, in lexical order. A file and a PEM document are not
// schemes and are always read.
func Signers() []string {
	return slices.Sorted(maps.Keys(signers))
}

// signerOf reads what `audit.checkpoints.key` says: the PEM document itself, a
// key in a device by its scheme, or the file a key is in.
func signerOf(v string) (crypto.Signer, error) {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return nil, nil
	case strings.HasPrefix(v, "-----BEGIN"):
		return trail.ParseKey([]byte(v))
	}

	scheme, named := schemeOf(v)
	if open, ok := signers[scheme]; named && ok {
		k, err := open(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", scheme, err)
		}
		if k == nil {
			return nil, fmt.Errorf("%s: opened nothing", scheme)
		}
		if err := trail.TrySigner(k); err != nil {
			if c, ok := k.(io.Closer); ok {
				_ = c.Close()
			}

			return nil, fmt.Errorf("%s: %w", scheme, err)
		}

		return k, nil
	}

	b, err := os.ReadFile(v)
	if err != nil {
		if named && errors.Is(err, fs.ErrNotExist) {
			return nil, unlinked(scheme)
		}

		return nil, err
	}

	return trail.ParseKey(b)
}

// unlinked refuses a scheme nothing in this binary opens. It names the scheme
// and never the value, since what follows a scheme can be a PIN.
func unlinked(scheme string) error {
	has := "none"
	if len(signers) > 0 {
		has = strings.Join(Signers(), ", ")
	}
	how := "import the package that registers it, or see config.RegisterSigner to write one"
	if pkg, ok := shipped[scheme]; ok {
		how = "import " + pkg
	}

	return fmt.Errorf(
		"`%s:` is not a scheme this binary opens (it opens %s), and there is no file of that name either.\n\n"+
			"    a key in a device is linked in, like a database driver: %s",
		scheme, has, how)
}

// schemeOf answers with the scheme `v` begins with, when it begins with one.
func schemeOf(v string) (string, bool) {
	i := strings.IndexByte(v, ':')
	if i < 0 || !isScheme(v[:i]) {
		return "", false
	}

	return strings.ToLower(v[:i]), true
}

func isScheme(v string) bool {
	if len(v) < 2 {
		return false
	}
	for i, r := range v {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z':
		case i > 0 && ('0' <= r && r <= '9' || r == '+' || r == '-' || r == '.'):
		default:
			return false
		}
	}

	return true
}
