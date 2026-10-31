package pdgen_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
)

// entitlementCopy is payday's Entitlement as `pd gen` writes it into an app
// declaring entitlements at `n`.
func entitlementCopy(t *testing.T, n int) string {
	t.Helper()

	drop := regexp.MustCompile(`(?m)^(edition|package|import|option (features|go_package))\b.*$`)
	zero := regexp.MustCompile(`(?m)^(\s*)domain: 0$`)

	b, err := os.ReadFile(filepath.Join("..", "..", "schema", "entitle", "entitlement.proto"))
	require.NoError(t, err)
	src := drop.ReplaceAllString(string(b), "")
	require.Len(t, zero.FindAllString(src, -1), 1, "entitlement.proto has one placeholder")

	return zero.ReplaceAllString(src, fmt.Sprintf("${1}domain: %d", n)) + "\n"
}

// TestTheCopyIsWhereTheAppSaysItIs: declared, it is payday's Entitlement at
// the app's number, behind the wall by its edge, and kept on the trail as a
// kind written beneath the servers.
func TestTheCopyIsWhereTheAppSaysItIs(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "fleet", "option (payday.app) = {entitlements: {domain: 43}};\n"+ownAll(t)+entitlementCopy(t, 43))
	x.NoError(err)
	x.NoError(pdgen.CheckOwn(s))
	x.NotNil(s.Entitlements)
	x.Equal(uint8(43), s.Entitlements.Domain)

	v := s.Own(pdpb.Own_OWN_ENTITLEMENT)
	x.NotNil(v)
	x.Equal("fleet.Entitlement", string(v.FullName()))
	x.Equal(uint8(43), v.Domain)
	x.False(v.IsGlobal, "a tenant's copy is behind the wall")

	// And an app that declares none has none.
	s, err = readAs(t, "fleet", ownAll(t))
	x.NoError(err)
	x.NoError(pdgen.CheckOwn(s))
	x.Nil(s.Entitlements)
	x.False(s.Has(pdpb.Own_OWN_ENTITLEMENT))
}

// TestTheCopyIsHeldToItsDeclaration: declared with no copy, a copy with no
// declaration, a copy at another number, and a number another entity holds --
// none of which `pd gen` writes, and each a table numbered by nothing the
// schema says.
func TestTheCopyIsHeldToItsDeclaration(t *testing.T) {
	x := require.New(t)
	declared := "option (payday.app) = {entitlements: {domain: 43}};\n"

	s, err := readAs(t, "fleet", declared+ownAll(t))
	x.NoError(err)
	x.ErrorContains(pdgen.CheckOwn(s), "declares entitlements")

	_, err = readAs(t, "fleet", ownAll(t)+entitlementCopy(t, 43))
	x.ErrorContains(err, "declares none")

	_, err = readAs(t, "fleet", declared+ownAll(t)+entitlementCopy(t, 44))
	x.ErrorContains(err, "declares 43 for it")

	other := `
message Other {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  option (orm.message) = {rpc: {get: {}}};
  option (payday.entity) = {domain: 43, global: {}, erase: {hard: {}}};
}
`
	_, err = readAs(t, "fleet", declared+ownAll(t)+entitlementCopy(t, 43)+other)
	x.ErrorContains(err, "gives it to the copy of entitlements")
}

// TestTheCopysNumberIsOneNothingElseHolds, refused before anything is copied.
func TestTheCopysNumberIsOneNothingElseHolds(t *testing.T) {
	app := pdpb.App_builder{
		Past:    pdpb.Past_builder{Event: 17, Fact: 16}.Build(),
		Notify:  pdpb.Notify_builder{Notification: 40, Receipt: 41}.Build(),
		Domains: pdpb.Domains_builder{Archived: 240, Access: 241}.Build(),
	}.Build()

	for name, v := range map[string]struct {
		n    uint32
		says string
	}{
		"none":            {0, "says no `domain:`"},
		"too large":       {256, "does not fit"},
		"payday's below":  {6, "is payday's"},
		"payday's above":  {248, "is payday's"},
		"the past's":      {16, "past.fact"},
		"a notification":  {40, "notify.notification"},
		"payday's access": {241, "domains.access"},
	} {
		t.Run(name, func(t *testing.T) {
			err := pdgen.CheckEntitlements(pdpb.Entitlements_builder{Domain: v.n}.Build(), app)
			require.ErrorContains(t, err, v.says)
		})
	}

	require.NoError(t, pdgen.CheckEntitlements(pdpb.Entitlements_builder{Domain: 43}.Build(), app))
	require.NoError(t, pdgen.CheckEntitlements(nil, app))
}

// TestTheCopyIsWrittenAsTheEntitlePackageReadsIt: what `pd gen` writes for an
// app that declares entitlements, and nothing for one that does not.
func TestTheCopyIsWrittenAsTheEntitlePackageReadsIt(t *testing.T) {
	x := require.New(t)

	emit := func(src string) string {
		s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", src)})
		x.NoError(err)

		g := p.NewGeneratedFile("entitle.go", "example.com/app/server/pd")
		g.P("package pd")
		g.P()
		pdgen.EmitEntitlements(g, s, pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"}, "example.com/app")
		b, err := g.Content()
		x.NoError(err)

		return string(b)
	}

	got := emit("option (payday.app) = {entitlements: {domain: 43}};\n" + ownAll(t) + entitlementCopy(t, 43))
	for _, v := range []string{
		"func Entitlements(drv dialect.Driver) entitle.Reader",
		"func EntitlementCopy(drv dialect.Driver, content audit.Contents) entitle.Copy",
		// A limit's read locks, outside the database that writes one at a time.
		"if lock && r.drv.Dialect() != dialect.SQLite {",
		"s.ForUpdate()",
		// Each write on the trail, in its transaction.
		"Record(ctx, s.db, s.content, entitle.Applied, []pdid.Id{id})",
		"RecordErased(ctx, s.db, s.content, entitle.Forgot, ids)",
		"pdid.New(EntitlementDomain)",
		// As the deployment's work, in the tenant alone.
		"frame.New(pdid.Nil, tenant, frame.Grant{}).WithScope(frame.Only(tenant))",
		// Values by the enum's names.
		"case app.EntitlementValue_ENTITLEMENT_VALUE_UNLIMITED:",
	} {
		x.Contains(got, v)
	}

	x.NotContains(emit(ownAll(t)), "Entitlement", "an app that declares none is written nothing")
}
