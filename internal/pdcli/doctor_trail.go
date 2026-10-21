package pdcli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/payday/internal/pdgen"
)

// A kind kept off the trail that holds a secret.
//
// `trail: {off: {why: ...}}` is the one place a schema says a check of the rows
// does not read a kind (payday#110), and it is right for what changes too often
// and means too little to keep a line of each write about: a session's idle
// clock, a lock, a queue. What it also keeps out of the check is any row
// somebody adds past the servers -- and a kind with a `secret` is one whose
// rows let somebody in: a session's key, a token, a verifier. A row of that
// added in the database is a way in nobody wrote, and a check that says it
// found nothing did not look (payday#115).
//
// Advice and not a refusal, as `personal` is: the decision is the schema's,
// and generation does what it always did. What the doctor does is ask whether
// the reason written down was written knowing that.
func doctorTrailOff(s *pdgen.Schema) []Finding {
	var vs []Finding
	for _, e := range s.Sorted() {
		if !e.Opts.GetTrail().HasOff() || len(e.Secrets) == 0 {
			continue
		}

		fix := &strings.Builder{}
		fmt.Fprintf(fix, "A check of the rows (`pd.Digests`) does not read %s, so a row of it added in the database past the servers is not found:\n\n", e.Name)
		fmt.Fprintf(fix, "    off: {why: %q}\n\n", e.Opts.GetTrail().GetOff().GetWhy())
		fix.WriteString("If it is a way in, record its writes instead, and each is checked as any kind is:\n\n")
		fix.WriteString("    option (payday.entity) = {... trail: {recorded: {}}};   // and pd.Record each write, in its transaction\n")
		for p := range e.Props() {
			if slices.Contains(e.Secrets, p.Name()) {
				fmt.Fprintf(fix, "\n%s  %s", where(p), p.Name())
			}
		}

		vs = append(vs, Finding{
			What: fmt.Sprintf("%s is kept off the trail and holds a secret (%s), so nothing finds one of its rows added past the servers",
				e.FullName(), strings.Join(e.Secrets, ", ")),
			Fix: fix.String(),
		})
	}

	return vs
}
