package pdgen_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
)

// TestTheSinkRemembersOnlyAWalkThatCannotMove: the sink asks which tenant a
// row's path arrives at once a transaction, and that is only right when the
// answer cannot change -- when no step past the first can be repointed. The
// first step is the edge of the row being written, and is not part of what is
// remembered.
//
// `pd gen` already refuses a stamp or an `agrees:` path with a step that can
// move; `via` beside an `agrees:` and no stamp is the one it lets through, and
// so the one the generated sink has to tell apart for itself.
func TestTheSinkRemembersOnlyAWalkThatCannotMove(t *testing.T) {
	const loose = `
message Loose {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 2, tenanted: {via: "tenant"}, erase: {hard: {}}};
}
`
	const fixed = `
message Fixed {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 3, tenanted: {via: "tenant"}, erase: {hard: {}}};
}
`
	src := inPkg("test", tenant+loose+fixed+entity("Grant",
		`domain: 9, tenanted: {via: "loose.tenant", agrees: ["fixed.tenant"]}`,
		`Loose loose = 2 [(orm.edge) = {immutable: true}];`,
		`Fixed fixed = 8 [(orm.edge) = {immutable: true}];`))

	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": src})
	if err != nil {
		t.Fatal(err)
	}

	g := p.NewGeneratedFile("sink.go", "example.com/app/server/pd")
	g.P("package pd")
	g.P()
	pdgen.EmitSink(g, s, pdgen.Paths{
		Ent:  "example.com/app/internal/ent",
		Bare: "example.com/app/server/bare",
	}, protogen.GoImportPath("example.com/app"))

	b, err := g.Content()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)

	body := func(name string) string {
		at := strings.Index(out, "func (s sinkGrant) "+name+"(")
		if at < 0 {
			t.Fatalf("no %s in what the sink wrote", name)
		}
		v := out[at:]
		if end := strings.Index(v[1:], "\nfunc "); end >= 0 {
			v = v[:end+1]
		}
		return v
	}

	if v := body("tenantAtLooseTenant"); strings.Contains(v, "seen.Tenant") {
		t.Errorf("a walk through an edge that can be repointed is remembered:\n%s", v)
	}
	if v := body("tenantAtFixedTenant"); !strings.Contains(v, `seen.Tenant(s.store.Db.Driver(), "Fixed.tenant", ref,`) {
		t.Errorf("a walk that cannot move is asked every time:\n%s", v)
	}
}
