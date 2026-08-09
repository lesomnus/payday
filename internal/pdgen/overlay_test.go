package pdgen_test

import (
	"strings"
	"testing"

	"github.com/lesomnus/payday/internal/pdgen"
)

// TestASchemaMissingPaydaysOwnIsRefused.
//
// Three emitters find payday's entities by full name and returned quietly when
// they were not there. What EmitGate writes is the whole of the `Add` tenant
// check, so a schema that lost `payday.Tenant` -- by a renamed proto package,
// most likely -- generated an app where **reads stay walled and writes stop
// being**, and nothing failed on the way.
func TestASchemaMissingPaydaysOwnIsRefused(t *testing.T) {
	// A schema of the app's own entities, which is what a renamed package
	// leaves behind: every one of payday's is gone at once.
	s, err := read(t, tenant+entity("Robot", `domain: 7, tenanted: {via: "tenant"}`,
		`Tenant tenant = 2 [(orm.edge) = {}];`))
	if err != nil {
		t.Fatal(err)
	}

	// Read itself is happy: `test.Tenant` says `tenant: {}`, so the wall is
	// found by the option and not by the name. That is the whole reason this
	// went unnoticed.
	if s.Tenant == nil {
		t.Fatal("the tenant was not found by its option")
	}

	err = pdgen.CheckOwn(s)
	if err == nil {
		t.Fatal("a schema with none of payday's own entities generated silently")
	}

	for _, want := range []string{
		"payday.Tenant", "payday.Holder", "payday.Audit", "payday.Outbox",
		"proto package was renamed",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}
