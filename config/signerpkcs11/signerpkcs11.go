// Package signerpkcs11 makes `pkcs11:` a key `audit.checkpoints.key` may name
// (#52): a key in an HSM, a cloud's HSM, a smart card or SoftHSM, by its RFC
// 7512 URI, signed with through `trail/trailpkcs11`.
//
// It is a package rather than a line in `config` for the reason `brokerpg` is
// one: importing it links a client in, here one that loads a C library at run
// time and so needs cgo. An app whose deployments keep the key in a token says
// so once, where it says what else it is built out of:
//
//	import _ "github.com/lesomnus/payday/config/signerpkcs11"
//
// and then this is a line a deployment can write:
//
//	audit:
//	  checkpoints:
//	    key: pkcs11:token=payday;object=checkpoints?module-path=/usr/lib/softhsm/libsofthsm2.so&pin-source=/run/secrets/hsm-pin
//
// The URI `pkcs11-tool --list-objects` prints for the key is the start of one:
// it names the token and the object, and this adds the library and where the
// PIN is.
package signerpkcs11

import (
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail/trailpkcs11"
)

func init() {
	config.RegisterSigner(trailpkcs11.Scheme, trailpkcs11.Open)
}
