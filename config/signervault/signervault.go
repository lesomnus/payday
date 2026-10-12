// Package signervault makes `hashivault://` a key `audit.checkpoints.key` may
// name (#52): a key in Vault's Transit secrets engine, or OpenBao's, signed
// with through `trail/trailvault`.
//
// It is a package rather than a line in `config` for the reason `brokerpg` is
// one, though what it links in is small -- it speaks Transit over HTTP and
// needs no SDK: an app whose deployments keep the key in Vault says so once,
// where it says what else it is built out of:
//
//	import _ "github.com/lesomnus/payday/config/signervault"
//
// and then this is a line a deployment can write, with `VAULT_ADDR` and a
// token in the environment as Vault's own tools read them:
//
//	audit:
//	  checkpoints:
//	    key: hashivault://checkpoints
//
// The package `trail/trailvault` says what else the URI may say, and which
// token to give it.
package signervault

import (
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail/trailvault"
)

func init() {
	config.RegisterSigner(trailvault.Scheme, trailvault.Open)
}
