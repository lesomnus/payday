// Package witnesslog registers the log witness (trail/witnesslog) with
// config: an app that imports it names one in `audit.checkpoints.witnesses`
// as `log:`,
//
//	import _ "github.com/lesomnus/payday/config/witnesslog"
//
// and alerts on what it tells; see payday's runtime docs.
package witnesslog

import (
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/trail/witnesslog"
)

func init() {
	config.RegisterWitness(witnesslog.Scheme, witnesslog.Open)
}
