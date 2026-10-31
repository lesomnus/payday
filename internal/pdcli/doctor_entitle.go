package pdcli

import (
	"fmt"

	"github.com/lesomnus/payday/internal/pdgen"
)

// entitlePkg is the copy's runtime.
const entitlePkg = "github.com/lesomnus/payday/entitle"

// doctorEntitlements says when the copy of what each tenant is entitled to is
// declared and nothing keeps it, and when a follower is built that gives up as
// it starts or never runs (payday#185).
//
// Each is quiet in the direction that costs. A copy nothing keeps fails
// nothing: every tenant is entitled to whatever was last applied, which for a
// new deployment is nothing -- every feature refused, every limit none --
// and for an old one is a plan that stops changing, until its grants end. A
// follower with no source, or no copy, ends the process as it starts, which
// is loud but found only by running it; one that is built and never handed to
// a spin list follows nothing at all.
//
// Read off the app's own Go, as the meters' are ([doctorMeters]): an
// `entitle.Follower` literal, with its `Source` and `Copy`, and that follower
// -- or what it was put in, or the function that builds it -- handed to an
// `append` or in a slice.
//
// What it does not ask is whether anything reads the copy, as it asks of
// meters whether anything reads them. Keeping the copy is payday's, and a
// meter is read by `pd.Meters` or not at all; which writes a grant decides
// is the app's own judgement, written where each write is, and no count of
// calls to `pd.Entitlements` says whether the right ones are.
func doctorEntitlements(l Layout, s *pdgen.Schema) []Finding {
	w := readWiring(l, entitlePkg, "Follower", "", "Source", "Copy")

	var vs []Finding
	if s.Entitlements != nil && len(w.loops) == 0 {
		vs = append(vs, Finding{
			What: "the schema declares entitlements and nothing keeps the copy: no entitle.Follower is built, so no " +
				"source of grants is configured, and every tenant is entitled to what the copy last held -- for a " +
				"new deployment, nothing",
			Fix: `s.Spin = append(s.Spin, &entitle.Follower{
	Source: pdpb.NewGrantServiceClient(conn), // the billing service's payday.GrantService
	Copy:   pd.EntitlementCopy(drv, content),
})

docs/guide/entitlements.md has the rest.`,
		})
	}

	for _, r := range w.loops {
		if !w.says(r, "Source") {
			vs = append(vs, Finding{
				What: fmt.Sprintf("%s builds an entitle.Follower with no Source -- no source of grants is configured -- "+
					"and a follower with none gives up as it starts, which ends the process", r.at),
				Fix: "Source: pdpb.NewGrantServiceClient(conn), // the billing service's payday.GrantService",
			})
		}
		if !w.says(r, "Copy") {
			vs = append(vs, Finding{
				What: fmt.Sprintf("%s builds an entitle.Follower with no Copy, and a follower with nothing to keep "+
					"gives up as it starts, which ends the process", r.at),
				Fix: "Copy: pd.EntitlementCopy(drv, content),",
			})
		}

		if w.spins(r) {
			continue
		}
		vs = append(vs, Finding{
			What: fmt.Sprintf("%s builds an entitle.Follower that is never handed to spin.Run, so the copy is never "+
				"kept: each tenant is entitled to what it last held, until its grants end", r.at),
			Fix: fmt.Sprintf("s.Spin = append(s.Spin, %s)", r.shownAs("s.Follower")),
		})
	}

	return vs
}
