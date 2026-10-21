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
	if v := body("tenantAtFixedTenant"); !strings.Contains(v, `seen.Tenant(s.store.Db.Driver(), "Fixed", "tenant", ref,`) {
		t.Errorf("a walk that cannot move is asked every time:\n%s", v)
	}
}

// TestTheSinkForgetsARowItWrites: what a transaction was told about a row is
// forgotten by a write to that row -- an Erase, a Patch, an Apply -- before
// the write, on the Sink, which both stacks are built on. An entity the Sink
// had nothing else to say about is wrapped for it.
func TestTheSinkForgetsARowItWrites(t *testing.T) {
	const plain = `
message Plain {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 2, tenanted: {via: "tenant"}, erase: {hard: {}}};
}
`
	src := inPkg("test", tenant+plain)

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

	for rpc, ref := range map[string]string{"Erase": "req", "Patch": "req.GetRef()", "Apply": "req.GetRef()"} {
		at := strings.Index(out, "func (s sinkPlain) "+rpc+"(")
		if at < 0 {
			t.Errorf("the sink does not forget a row its %s writes: there is no %s", rpc, rpc)
			continue
		}
		v := out[at:]
		if end := strings.Index(v[1:], "\nfunc "); end >= 0 {
			v = v[:end+1]
		}
		forget := `seen.Forget(s.store.Db.Driver(), "Plain", ` + ref + `)`
		call := "s.PlainServiceServer." + rpc + "(ctx, req)"
		if f, c := strings.Index(v, forget), strings.Index(v, call); f < 0 || c < 0 || f > c {
			t.Errorf("%s does not forget the row before it writes it:\n%s", rpc, v)
		}
	}
}
