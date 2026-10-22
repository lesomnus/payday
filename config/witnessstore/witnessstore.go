// Package witnessstore registers the store witness (trail/witnessstore) with
// config: an app that imports it names one in `audit.checkpoints.witnesses`
// as `store:` and a directory of its own,
//
//	import _ "github.com/lesomnus/payday/config/witnessstore"
//
// best on a disk that refuses deletion, and mounted with credentials neither
// the database's nor the archive's.
package witnessstore

import (
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail/witnessstore"
)

func init() {
	config.RegisterWitness(witnessstore.Scheme, witnessstore.Open)
}
