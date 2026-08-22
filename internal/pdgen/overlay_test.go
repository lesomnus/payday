package pdgen_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
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
		"OWN_TENANT", "OWN_HOLDER", "OWN_AUDIT", "OWN_OUTBOX",
		"reads stay walled and writes stop being",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// TestPaydaysOwnEntitiesAreFoundByMarkerNotName.
//
// This is the whole of what `own:` bought. The four used to be found by full
// name -- `payday.Tenant` and the rest -- which made the proto package payday's
// rather than the app's, and made renaming it silently destructive: the layers
// built out of them were simply not generated.
//
// So the schema below is in a package called something else entirely, and
// everything still finds them.
func TestPaydaysOwnEntitiesAreFoundByMarkerNotName(t *testing.T) {
	s, err := readAs(t, "fleet", ownAll(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		own  pdpb.Own
		name string
	}{
		{pdpb.Own_OWN_TENANT, "fleet.Tenant"},
		{pdpb.Own_OWN_HOLDER, "fleet.Holder"},
		{pdpb.Own_OWN_AUDIT, "fleet.Audit"},
		{pdpb.Own_OWN_OUTBOX, "fleet.Outbox"},
	} {
		v := s.Own(tc.own)
		if v == nil {
			t.Fatalf("%s was not found in a package called something else", tc.own)
		}
		if got := string(v.FullName()); got != tc.name {
			t.Errorf("%s: got %s, want %s", tc.own, got, tc.name)
		}
	}

	// And the refusal is satisfied, so `pd gen` would go on to write the Gate
	// layer, the audit layer and the drain.
	if err := pdgen.CheckOwn(s); err != nil {
		t.Fatalf("a renamed package is refused: %v", err)
	}

	// Nothing is payday's by accident: an app's own entity says no marker.
	if v := s.Own(pdpb.Own_OWN_UNSPECIFIED); v != nil {
		t.Errorf("an entity with no marker answered as payday's: %s", v.FullName())
	}
}

// TestTwoEntitiesCannotClaimTheSameMarker.
//
// Whichever came first would win, which is a schema settling a trust boundary
// by its own declaration order -- the same failure the certificate reader
// refuses for the same reason.
func TestTwoEntitiesCannotClaimTheSameMarker(t *testing.T) {
	_, err := readAs(t, "fleet", ownAll(t)+entity("Impostor",
		`domain: 20, own: OWN_TENANT, global: {}`))
	if err == nil {
		t.Fatal("two entities claimed to be the tenant")
	}
	for _, want := range []string{"Impostor", "OWN_TENANT", "an app writes no `own:`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// ownAll is payday's own four entities, **read from the files payday ships**,
// in a package that is not payday's.
//
// Written out by hand they would be a stub, and a stub does not survive
// [pdgen.CheckOverlay] -- which is itself the point: that check now runs on a
// renamed package, where keyed on the name it matched nothing and quietly did
// not. So the fixture has to be the real thing, and taking it from the source
// of truth is also what keeps it from going stale.
func ownAll(t *testing.T) string {
	t.Helper()

	// Everything the synthetic file in `readAs` already supplies, or that names
	// the package these are being moved out of.
	drop := regexp.MustCompile(`(?m)^(edition|package|import|option (features|go_package))\b.*$`)

	b := &strings.Builder{}
	for _, name := range []string{"tenant", "holder", "audit", "outbox"} {
		v, err := os.ReadFile(filepath.Join("..", "..", "schema", "payday", name+".proto"))
		if err != nil {
			t.Fatal(err)
		}

		// `payday.Tenant` as an edge target becomes the local `Tenant`, since
		// that is what moving them into one package does.
		src := drop.ReplaceAllString(string(v), "")
		b.WriteString(strings.ReplaceAll(src, "payday.Tenant ", "Tenant "))
		b.WriteString("\n")
	}

	return b.String()
}
