// Package signertpm makes `tpm:` a key `audit.checkpoints.key` may name (#52):
// a key at a persistent handle of the machine's TPM 2.0, or its vTPM, signed
// with through `trail/trailtpm`.
//
// It is a package rather than a line in `config` for the reason `brokerpg` is
// one: importing it links a client in. An app whose deployments keep the key
// in a TPM says so once, where it says what else it is built out of:
//
//	import _ "github.com/lesomnus/payday/config/signertpm"
//
// and then this is a line a deployment can write:
//
//	audit:
//	  checkpoints:
//	    key: tpm:0x81000100
//
// The package `trail/trailtpm` says how to make the key.
package signertpm

import (
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail/trailtpm"
)

func init() {
	config.RegisterSigner(trailtpm.Scheme, trailtpm.Open)
}
