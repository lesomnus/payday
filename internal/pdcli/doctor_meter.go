package pdcli

import (
	"fmt"
	"strings"

	"github.com/lesomnus/payday/internal/pdgen"
)

// meterPkg is the meters' runtime.
const meterPkg = "github.com/lesomnus/payday/meter"

// doctorMeters says when what each tenant holds is declared and nothing reads
// it, and when a reader is built and never run (payday#186).
//
// Both are as quiet as anything asked about here. A meter nobody reads fails
// nothing: the app serves, and the billing service is handed no readings -- or
// the ones of the meters the app wrote itself and not the schema's -- which is
// found the day somebody looks at a bill. A reader that is built and not in
// the spin list reads whenever the app asks one tenant to be read, before it
// destroys it, and on no schedule at all.
//
// Each is found by what the app's own code names, as the access record's
// parts are: `pd.Meters`, which is the schema's meters; a `meter.Reader`
// literal, and its `Sink`; and that reader -- or what it was put in, or the
// function that builds it -- handed to an `append` or in a slice, which is how
// a spin list is made. A test does not count: what it wires is not what the
// app serves.
func doctorMeters(l Layout, s *pdgen.Schema) []Finding {
	w := readWiring(l, meterPkg, "Reader", "Meters", "Sink")

	var vs []Finding
	if n := len(s.Meters); n > 0 && !w.called {
		keys := []string{}
		for _, m := range s.Meters {
			keys = append(keys, m.Key)
		}
		vs = append(vs, Finding{
			What: fmt.Sprintf("the schema declares %d meters -- %s -- and nothing reads them: "+
				"`pd.Meters` is called nowhere, so no reading of them reaches a sink", n, strings.Join(keys, ", ")),
			Fix: `s.Meters = &meter.Reader{
	Tenants: pd.MeterTenants(client),
	Meters:  pd.Meters(client), // and the app's own, if it writes any
	Sink:    sink,              // the billing service's client
}
s.Spin = append(s.Spin, s.Meters)

docs/guide/meters.md has the rest: a replica, and reading a tenant before it is destroyed.`,
		})
	}

	for _, r := range w.loops {
		if !w.says(r, "Sink") {
			vs = append(vs, Finding{
				What: fmt.Sprintf("%s builds a meter.Reader with no Sink, and a reader with none gives up as it starts: "+
					"nothing is measured and the process ends", r.at),
				Fix: "Sink: sink, // the billing service's client: anything with Send(ctx, []meter.Reading) error",
			})
		}

		if w.spins(r) {
			continue
		}
		vs = append(vs, Finding{
			What: fmt.Sprintf("%s builds a meter.Reader that is never handed to spin.Run, so no tenant is measured "+
				"on a schedule -- only when the app asks for one", r.at),
			Fix: fmt.Sprintf("s.Spin = append(s.Spin, %s)", r.shownAs("s.Meters")),
		})
	}

	return vs
}
