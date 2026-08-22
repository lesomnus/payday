package pdcli

import (
	"strings"
	"testing"
)

// TestAStalePinIsExplained.
//
// The failure is real and it happened: an app's buf.lock was pinned before
// `erase:` and `own:` were pushed, so `pd gen` died on a file the app never
// wrote, naming fields it had never heard of. Nothing in buf's message says the
// word "pin", so the obvious readings -- a corrupt copy, a bad merge, a broken
// payday -- are all wrong.
func TestAStalePinIsExplained(t *testing.T) {
	const said = `proto/payday/audit.proto:209:5:message app.Audit: option (payday.entity): field own not found
proto/payday/audit.proto:215:5:message app.Audit: option (payday.entity): field erase not found
proto/payday/tenant.proto:65:5:message app.Tenant: option (payday.entity): field own not found`

	v := whyStale([]byte(said))
	for _, want := range []string{"`own:`", "`erase:`", "buf dep update", "a pin is yours to move"} {
		if !strings.Contains(v, want) {
			t.Errorf("does not say %q:\n%s", want, v)
		}
	}
	if n := strings.Count(v, "`own:`"); n != 1 {
		t.Errorf("names the same field %d times", n)
	}

	// And it says nothing about anything else, so an ordinary compile error is
	// not decorated with advice that does not apply.
	if v := whyStale([]byte(`proto/app/thing.proto:12:3: field "alias" already defined`)); v != "" {
		t.Errorf("explained an unrelated failure:\n%s", v)
	}
}
