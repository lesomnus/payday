package pdgen_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
)

// TestTheSinkKeepsANameOnlyWhereAServerWritesOne: an entity its servers only
// read -- written by the app's own code, and served by a Get, a List and a
// Watch -- has an alias the app wrote, and no Add or Patch for the Sink to keep
// it by. What the Sink writes about a name is written for the servers there
// are: an Add retries a name it made up, an AddMany its items', a Patch folds
// one it was given; and none of it, nor the rule an Add is admitted by, for a
// server that has none of them. Written anyway, it called methods the server
// does not have, and the app did not compile.
func TestTheSinkKeepsANameOnlyWhereAServerWritesOne(t *testing.T) {
	const read = `
message Reading {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias = 4;
  option (orm.message) = {rpc: {get: {}}};
  option (payday.entity) = {domain: 7, tenanted: {via: "tenant"}, erase: {hard: {}}, trail: {recorded: {}}};
}
`
	const renamed = `
message Label {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias = 4;
  option (orm.message) = {rpc: {get: {}, patch: {}}};
  option (payday.entity) = {domain: 8, tenanted: {via: "tenant"}, erase: {hard: {}}};
}
`
	const written = `
message Robot {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias = 4;
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 9, tenanted: {via: "tenant"}, erase: {hard: {}}};
}
`
	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("test", tenant+read+renamed+written)})
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

	has := func(v string) bool { return strings.Contains(out, v) }
	for _, v := range []string{
		"func (s sinkReading) Add(",
		"func (s sinkReading) Patch(",
		"func (s sinkReading) admit(",
		"ReadingWithAddRule",
		"ReadingAddRequest",
		"func (s sinkLabel) Add(",
		"func (s sinkLabel) admit(",
		"LabelWithAddRule",
		"[sinkLabel.Add]",
	} {
		if has(v) {
			t.Errorf("the sink wrote %q, for a server that has no such write", v)
		}
	}
	for _, v := range []string{
		"func (s sinkLabel) Patch(",
		"func (s sinkRobot) Add(",
		"func (s sinkRobot) Patch(",
		"func (s sinkRobot) admit(",
		"RobotWithAddRule",
	} {
		if !has(v) {
			t.Errorf("the sink did not write %q, for a server that has that write", v)
		}
	}
}
