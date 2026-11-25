package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
)

// switching is payday's own entities, merged with an overlay that says `rpc`
// on `entity` -- what a generator is handed once the merge has run: payday's
// verbs, and what the overlay added beside them.
func switching(t *testing.T, entity, rpc string) string {
	t.Helper()

	src := ownAll(t)
	at := strings.Index(src, "message "+entity+" {")
	require.GreaterOrEqual(t, at, 0, "payday has no %s", entity)
	end := at + strings.Index(src[at:], "\n}\n")
	body := src[at:end]

	// What the merge makes of it: a verb payday names takes what the overlay
	// adds inside it, and one payday does not name is added beside them.
	verb := strings.TrimSpace(rpc[:strings.Index(rpc, ":")])
	from := strings.Index(body, "    rpc: {\n")
	require.GreaterOrEqual(t, from, 0, "payday's %s names no verbs", entity)
	to := from + strings.Index(body[from:], "\n    }")
	block := body[from:to]
	if line := "\n      " + verb + ": {}"; strings.Contains(block, line) {
		block = strings.Replace(block, line, "\n      "+rpc, 1)
	} else {
		block += "\n      " + rpc
	}
	body = body[:from] + block + body[to:]

	return src[:at] + body + src[end:]
}

// TestAnOverlayMaySwitchAVerbOfPaydaysWithinWhatPaydayNeeds: an app that never
// erases a tenant should not have `TenantService/Erase` in every client and
// refuse it at every port (lesomnus/roster#531). So an overlay of payday's
// Holder or Tenant may switch off a verb payday's entity has, unless payday's
// own code calls it -- then it is refused, naming the entity, the verb and
// what calls it -- and may ask for an Apply, which payday's has none of.
// Nothing else is switched on, and nothing at all on payday's other entities,
// whose verbs are the ones payday itself calls.
func TestAnOverlayMaySwitchAVerbOfPaydaysWithinWhatPaydayNeeds(t *testing.T) {
	read := func(t *testing.T, entity, rpc string) (*pdgen.Schema, error) {
		t.Helper()
		return readAs(t, "fleet", switching(t, entity, rpc)+tenantedRobot)
	}
	own := func(s *pdgen.Schema, own pdpb.Own) *pdgen.Entity {
		v := s.Own(own)
		require.NotNil(t, v)
		return v
	}

	for _, tt := range []struct {
		entity string
		own    pdpb.Own
		rpc    string
		has    func(*pdgen.Entity) bool
	}{
		{"Tenant", pdpb.Own_OWN_TENANT, "erase: {disabled: true}", func(v *pdgen.Entity) bool { return !v.Rpcs().HasErase() }},
		{"Tenant", pdpb.Own_OWN_TENANT, "patch: {disabled: true}", func(v *pdgen.Entity) bool { return !v.Rpcs().HasPatch() }},
		{"Holder", pdpb.Own_OWN_HOLDER, "erase: {disabled: true}", func(v *pdgen.Entity) bool { return !v.Rpcs().HasErase() }},
		{"Holder", pdpb.Own_OWN_HOLDER, "patch: {disabled: true}", func(v *pdgen.Entity) bool { return !v.Rpcs().HasPatch() }},
		{"Tenant", pdpb.Own_OWN_TENANT, "apply: {}", func(v *pdgen.Entity) bool { return v.Rpcs().HasApply() }},
		{"Holder", pdpb.Own_OWN_HOLDER, "apply: {}", func(v *pdgen.Entity) bool { return v.Rpcs().HasApply() }},
	} {
		t.Run(tt.entity+" "+tt.rpc+" is the app's to say", func(t *testing.T) {
			s, err := read(t, tt.entity, tt.rpc)
			require.NoError(t, err)
			require.True(t, tt.has(own(s, tt.own)), "the merged option is not what the generators read")
		})
	}

	for _, tt := range []struct {
		entity string
		rpc    string
		says   string
	}{
		{"Tenant", "add: {disabled: true}", "fleet.Tenant: (orm.message) rpc.add.disabled: payday calls this Tenant's Add -- `init` puts up the first tenant"},
		{"Tenant", "get: {disabled: true}", "fleet.Tenant: (orm.message) rpc.get.disabled: payday calls this Tenant's Get -- the gate reads"},
		{"Holder", "add: {disabled: true}", "fleet.Holder: (orm.message) rpc.add.disabled: payday calls this Holder's Add -- `init` makes the first holder"},
		{"Holder", "get: {disabled: true}", "fleet.Holder: (orm.message) rpc.get.disabled: payday calls this Holder's Get -- signing in reads"},

		// Nothing switched on but an Apply, and that only as asked for.
		// Not an AddMany, whose rule stands (TestPaydaysOwnHaveNoAddMany), and
		// is said before an overlay is.
		{"Holder", "add: {many: {}}", "fleet.Holder asks for AddMany, and it is payday's"},
		{"Tenant", "apply: {disabled: true}", "fleet.Tenant: (orm.message) rpc.apply is not payday's"},

		// And not a verb of any other entity of payday's.
		{"Audit", "apply: {}", "fleet.Audit: (orm.message) rpc.apply is not payday's; an overlay may add an index there and nothing else -- a verb is switched only on payday's Holder and Tenant"},
		{"Audit", "erase: {}", "fleet.Audit: (orm.message) rpc.erase is not payday's"},
		{"Access", "get: {disabled: true}", "fleet.Access: (orm.message) rpc.get.disabled is not payday's; an overlay may add an index there and nothing else -- a verb is switched only on payday's Holder and Tenant"},
	} {
		t.Run(tt.entity+" "+tt.rpc+" is refused", func(t *testing.T) {
			var src string
			if tt.entity == "Access" {
				src = strings.Replace(ownAll(t), "rpc: {get: {}}", "rpc: {"+tt.rpc+"}", 1)
				require.Contains(t, src, tt.rpc)
				_, err := readAs(t, "fleet", src+tenantedRobot)
				require.ErrorContains(t, err, tt.says)
				return
			}

			_, err := read(t, tt.entity, tt.rpc)
			require.ErrorContains(t, err, tt.says)
			if !strings.Contains(tt.rpc, "many") {
				require.ErrorContains(t, err, "of Holder and Tenant switch a verb\noff that payday does not call")
			}
		})
	}
}

// TestAVerbSwitchedIsWhatPaydayWritesFor: the merged entity is what every
// generator reads, payday's included. A Tenant whose Erase is switched off has
// no gate refusing an Erase -- written anyway, it named a request the contract
// no longer has -- and a Holder whose Apply is switched on has the gate's
// question about what a document moves, the Sink's forgetting and the Secret
// layer for it, as payday's would.
func TestAVerbSwitchedIsWhatPaydayWritesFor(t *testing.T) {
	x := require.New(t)

	src := switching(t, "Tenant", "erase: {disabled: true}")
	{
		holder := switching(t, "Holder", "apply: {}")
		at, end := strings.Index(holder, "message Holder {"), strings.Index(src, "message Holder {")
		src = src[:end] + holder[at:at+strings.Index(holder[at:], "\n}\n")] + src[end+strings.Index(src[end:], "\n}\n"):]
	}
	src = strings.Replace(src, "message Holder {", "message Holder {\n  bytes secret = 16 [(payday.field) = {secret: true}];\n  Robot robot = 17 [(orm.edge) = {nullable: true}];", 1)

	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", src+tenantedRobot)})
	x.NoError(err)

	g := p.NewGeneratedFile("pd.go", "example.com/app/server/pd")
	g.P("package pd")
	g.P()
	paths := pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"}
	pdgen.EmitGate(g, s, paths, protogen.GoImportPath("example.com/app"))
	pdgen.EmitSink(g, s, paths, protogen.GoImportPath("example.com/app"))
	pdgen.EmitSecret(g, s, paths, protogen.GoImportPath("example.com/app"))
	b, err := g.Content()
	x.NoError(err)
	out := string(b)

	x.NotContains(out, "TenantEraseResponse", "an Erase switched off was written for")
	x.NotContains(out, "func (s gateTenant) Erase(")
	x.NotContains(out, "func (s sinkTenant) Erase(")
	for _, v := range []string{
		"func (s gateHolder) Apply(",
		"func (s sinkHolder) Apply(",
		"func (s secretHolder) Apply(",
	} {
		x.Contains(out, v, "an Apply switched on was not written for")
	}
}
