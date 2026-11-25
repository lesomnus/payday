package pdgen

import (
	"github.com/lesomnus/payday/pdpb"
)

// paydayCalls is each verb of payday's Holder and Tenant that payday's own code
// calls, with what calls it: what an app's overlay may not switch off
// ([CheckOverlay]), since what calls it would then call nothing, and would
// fail where it was written rather than where the overlay was.
//
// It is read from the code, and kept here rather than beside each caller
// because the callers are in three places that share no Go -- what this
// package writes, what `pd new` writes, and what `pd sandbox init` writes --
// and an overlay is checked against all of them at once.
// TestEveryVerbPaydayCallsOfAHolderOrATenantIsListed reads each of them, and
// fails on a verb called that is not here, or one here that nothing calls.
//
// `Patch` and `Erase` are not here: nothing of payday's calls either. They are
// what an app serves, so payday's entities declare them, and an app that does
// not may switch them off. `Apply` is not declared by payday at all, and an
// overlay may switch it on.
var paydayCalls = map[pdpb.Own]map[string]string{
	pdpb.Own_OWN_TENANT: {
		"add": "`init` puts up the first tenant, and the sandbox seeds one, through it",
		"get": "the gate reads, through it, the tenant a row is put into or an edge is moved onto, and a page made by `pd new` reads its tenant",
	},
	pdpb.Own_OWN_HOLDER: {
		"add": "`init` makes the first holder of the first tenant, and the sandbox seeds one, through it",
		"get": "signing in reads, through it, the holder a credential names (`cmd/auth.go`), and the Watch reads the row it sends",
	},
}

// switchable is whether an overlay may switch payday's verbs of `own`; see
// [CheckOverlay].
func switchable(own pdpb.Own) bool {
	_, ok := paydayCalls[own]
	return ok
}
