// Package witnessrekor registers the Rekor witness (trail/witnessrekor) with
// config: an app that imports it names a log in `audit.checkpoints.witnesses`
// as `rekor:`, its URL, `key=` the PEM file of the log's key and `v=2` for a
// log on tiles,
//
//	import _ "github.com/lesomnus/payday/config/witnessrekor"
//
// a log of the deployment's own, or the public instance by the operator's
// choice; see payday's runtime docs first.
package witnessrekor

import (
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail/witnessrekor"
)

func init() {
	config.RegisterWitness(witnessrekor.Scheme, witnessrekor.Open)
}
