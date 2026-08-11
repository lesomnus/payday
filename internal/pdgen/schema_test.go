package pdgen_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/protobuf-orm/protobuf-orm/graph"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/lesomnus/payday/internal/pdgen"

	// Linked so that their descriptors are in the global registry, which is
	// what the compiler below resolves imports against. Nothing here calls
	// them.
	_ "github.com/lesomnus/payday/pdpb"
	_ "github.com/protobuf-orm/protobuf-orm/ormpb"
)

// read compiles a schema written inline and hands it to the reader, which is
// what the plugin does with what buf gives it.
//
// It is done in memory rather than by running buf so that a test that asks
// "what does it say when this is wrong" costs nothing to write and nothing to
// run. The imports resolve out of the global registry -- `orm.proto` and
// `payday.proto` are linked into this binary -- so there is no import path to
// keep in step with anything.
func read(t *testing.T, src string) (*pdgen.Schema, error) {
	return readAs(t, "test", src)
}

// readAs is [read] in a named package, for the cases that are about payday's
// own entities and so have to be declared where payday declares them.
func readAs(t *testing.T, pkg string, src string) (*pdgen.Schema, error) {
	t.Helper()

	const name = "test/schema.proto"
	whole := "edition = \"2023\";\npackage " + pkg + ";\n" +
		"import \"orm.proto\";\nimport \"payday.proto\";\n" +
		"import \"google/protobuf/timestamp.proto\";\n" +
		"option features.field_presence = IMPLICIT;\n" +
		"option go_package = \"example.com/app\";\n" + src

	c := protocompile.Compiler{
		Resolver: protocompile.CompositeResolver{
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{name: whole}),
			},
			protocompile.ResolverFunc(func(p string) (protocompile.SearchResult, error) {
				fd, err := protoregistry.GlobalFiles.FindFileByPath(p)
				if err != nil {
					return protocompile.SearchResult{}, err
				}

				return protocompile.SearchResult{Desc: fd}, nil
			}),
		},
		SourceInfoMode: protocompile.SourceInfoStandard,
	}

	fs, err := c.Compile(context.Background(), name)
	if err != nil {
		t.Fatalf("compile the schema this test is about: %v", err)
	}

	// protogen is what the plugin sees, so the test sees it too.
	req := &pluginpb.CodeGeneratorRequest{FileToGenerate: []string{name}}
	seen := map[string]bool{}
	var add func(fd protoreflect.FileDescriptor)
	add = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true

		// Dependencies first: protogen reads them in order and a file whose
		// imports have not been seen yet is a file it cannot resolve.
		is := fd.Imports()
		for i := range is.Len() {
			add(is.Get(i).FileDescriptor)
		}
		req.ProtoFile = append(req.ProtoFile, rebuild(t, fd))
	}
	add(fs[0])

	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("build the plugin request: %v", err)
	}

	g := graph.NewGraph()
	if err := graph.ParseFiles(context.Background(), g, p.Files); err != nil {
		return nil, fmt.Errorf("orm: %w", err)
	}

	return pdgen.Read(g, p.Files)
}

// rebuild round-trips a descriptor through its wire form so that the custom
// options in it come back as the concrete types this binary has linked.
//
// The compiler above resolves `orm.proto` and `payday.proto` out of the global
// registry, and what it makes of an option written against them is a dynamic
// message. `proto.GetExtension` is handed the concrete type and panics on the
// dynamic one, so the two are reconciled here rather than everywhere the
// generator reads an option.
func rebuild(t *testing.T, fd protoreflect.FileDescriptor) *descriptorpb.FileDescriptorProto {
	t.Helper()

	b, err := proto.Marshal(protodesc.ToFileDescriptorProto(fd))
	if err != nil {
		t.Fatalf("hold %s: %v", fd.Path(), err)
	}

	v := &descriptorpb.FileDescriptorProto{}
	// The default resolver is the global type registry, which is where the
	// linked extensions are.
	if err := proto.Unmarshal(b, v); err != nil {
		t.Fatalf("read %s back: %v", fd.Path(), err)
	}

	return v
}

// entity is the boilerplate every case below would otherwise repeat: a keyed
// message with whatever payday option the case is about.
func entity(name, opts string, extra ...string) string {
	body := ""
	for _, v := range extra {
		body += v + "\n"
	}

	return fmt.Sprintf(`
message %s {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  string alias = 4;
%s
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {%s, erase: {hard: {}}};
}
`, name, body, opts)
}

const tenant = `
message Tenant {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  string alias = 4 [(orm.field) = {unique: true}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 1, tenant: {}, erase: {hard: {}}};
}
`

func TestReads(t *testing.T) {
	s, err := read(t, tenant+entity("Robot", `domain: 7, tenanted: {via: "tenant"}`,
		`Tenant tenant = 2 [(orm.edge) = {}];`))
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]*pdgen.Entity{}
	for _, v := range s.Entities {
		byName[v.GoName()] = v
	}

	if s.Tenant == nil || s.Tenant.GoName() != "Tenant" {
		t.Fatalf("the tenant was not found: %v", s.Tenant)
	}
	if got := byName["Robot"].Domain; got != 7 {
		t.Errorf("Robot domain: %d", got)
	}
	if got := byName["Robot"].Name; got != "robot" {
		t.Errorf("Robot name: %q, and nothing declared one so it should be the message folded down", got)
	}
	if got := byName["Robot"].Via; len(got) != 1 || got[0] != "tenant" {
		t.Errorf("Robot via: %v", got)
	}
}

// TestRefuses is the whole reason the option exists. Each of these is
// something that compiles, runs, and is wrong in a way nothing would report.
func TestRefuses(t *testing.T) {
	for _, tt := range []struct {
		what string
		src  string
		says string
	}{{
		what: "an entity that says nothing at all",
		src: tenant + `
message Robot {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  option (orm.message) = {rpc: {crud: true}};
}`,
		says: "(payday.entity)",
	}, {
		// Saying nothing is behind the wall by the edge called `tenant`, so what
		// is refused is not the silence -- it is an entity with no such edge.
		// See TestSayingNothingIsBehindTheWall for the other half.
		what: "an entity that said nothing and has no tenant to be behind",
		src:  tenant + entity("Robot", `domain: 7`),
		says: `has no edge "tenant"`,
	}, {
		what: "a domain of zero, which is what an unregistered identifier reads as",
		src:  tenant + entity("Robot", `domain: 0, global: {}`),
		says: "domain",
	}, {
		what: "a domain that does not fit in the byte an identifier carries",
		src:  tenant + entity("Robot", `domain: 256, global: {}`),
		says: "one byte",
	}, {
		what: "two entities sharing a domain, which makes an old identifier lie",
		src:  tenant + entity("Robot", `domain: 1, global: {}`),
		says: "both declare domain 1",
	}, {
		what: "two entities written the same way",
		src:  tenant + entity("Robot", `domain: 7, name: "tenant", global: {}`),
		says: "both written",
	}, {
		what: "two entities claiming to be the tenant",
		src:  tenant + entity("Robot", `domain: 7, tenant: {}`),
		says: "a wall is made of one thing",
	}, {
		what: "a tenanted entity in a schema with no tenant",
		src:  entity("Robot", `domain: 7, tenanted: {via: "tenant"}`),
		says: "nothing in this schema says it is the tenant",
	}, {
		what: "a via that names an edge the entity does not have",
		src: tenant + entity("Robot", `domain: 7, tenanted: {via: "owner"}`,
			`Tenant tenant = 2 [(orm.edge) = {}];`),
		says: `no edge "owner"`,
	}, {
		what: "a via that arrives somewhere that is not the tenant",
		src: tenant +
			entity("Robot", `domain: 7, tenanted: {via: "tenant"}`, `Tenant tenant = 2 [(orm.edge) = {}];`) +
			entity("Joint", `domain: 8, tenanted: {via: "robot"}`, `Robot robot = 2 [(orm.edge) = {}];`),
		says: "arrives at test.Robot",
	}} {
		t.Run(tt.what, func(t *testing.T) {
			_, err := read(t, tt.src)
			if err == nil {
				t.Fatal("generated anyway")
			}
			if !strings.Contains(err.Error(), tt.says) {
				t.Fatalf("the refusal does not say %q:\n%s", tt.says, err)
			}
			t.Log(err)
		})
	}
}

// TestNames is what a message is written as when the schema did not say.
func TestNames(t *testing.T) {
	s, err := read(t, tenant+
		entity("WorkCell", `domain: 7, global: {}`)+
		entity("Robot", `domain: 8, name: "bot", global: {}`))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"Tenant": "tenant", "WorkCell": "work-cell", "Robot": "bot"}
	for _, v := range s.Entities {
		if got := v.Name; got != want[v.GoName()] {
			t.Errorf("%s: %q, want %q", v.GoName(), got, want[v.GoName()])
		}
	}
}

// TestTenantedByField is the form an audit trail needs.
//
// A trail row names its tenant with a column and holds no edge to it, and that
// is deliberate: what it records happened, and it goes on being true after the
// tenant it happened in is gone. There is no foreign key to walk, so `via`
// cannot say it.
func TestTenantedByField(t *testing.T) {
	t.Run("reads a column instead of an edge", func(t *testing.T) {
		s, err := read(t, tenant+`
message Audit {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  bytes tenant_id = 2 [(orm.field) = {type: TYPE_UUID}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 3, tenanted: {field: "tenant_id"}, erase: {hard: {}}};
}`)
		if err != nil {
			t.Fatal(err)
		}

		for _, v := range s.Entities {
			if v.GoName() != "Audit" {
				continue
			}
			if len(v.Columns) != 1 || v.Columns[0] != "tenant_id" {
				t.Errorf("field: %q", v.Columns)
			}
			if len(v.Via) != 0 {
				t.Errorf("via: %v, and a column is not an edge", v.Via)
			}
		}
	})

	for _, tt := range []struct {
		what string
		src  string
		says string
	}{{
		what: "a column that is not there",
		src:  tenant + entity("Audit", `domain: 3, tenanted: {field: "tenant_id"}`),
		says: `no field "tenant_id"`,
	}, {
		what: "a column that could not hold an identifier",
		src:  tenant + entity("Audit", `domain: 3, tenanted: {field: "alias"}`),
		says: "a tenant is named by a uuid",
	}, {
		what: "an edge named where a column was asked for",
		src: tenant + entity("Audit", `domain: 3, tenanted: {field: "tenant"}`,
			`Tenant tenant = 2 [(orm.edge) = {}];`),
		says: "say `via` for those",
	}, {
		what: "both at once, which are not two ways of writing one thing",
		src: tenant + entity("Audit", `domain: 3, tenanted: {via: "tenant", field: "tenant_id"}`,
			`Tenant tenant = 2 [(orm.edge) = {}];`),
		says: "not two ways of writing one thing",
	}} {
		t.Run(tt.what+" is refused", func(t *testing.T) {
			_, err := read(t, tt.src)
			if err == nil {
				t.Fatal("generated anyway")
			}
			if !strings.Contains(err.Error(), tt.says) {
				t.Fatalf("the refusal does not say %q:\n%s", tt.says, err)
			}
			t.Log(err)
		})
	}
}

// TestOverlayMayAddAndMayNotChange is the guard on the one thing merging does
// silently.
//
// protobuf-merge takes the overlay's word for a number that is already there.
// So an overlay that redeclares payday's `alias` wins, and the app compiles: the
// wall goes on reading a tenant and `auth` goes on looking a holder up, both
// against a column that is no longer what they were written for.
func TestOverlayMayAddAndMayNotChange(t *testing.T) {
	// payday's Holder as it ships, with the numbers this test is about spelled
	// out so that a reader does not have to open another file.
	holder := func(extra string) string {
		return `
message Holder {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias = 4;
  string name = 5;
  string desc = 6;
  map<string, string> labels = 7;
  google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];
  google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];
` + extra + `
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 2, tenanted: {via: "tenant"}, own: OWN_HOLDER};
}`
	}

	t.Run("adding in the app's own numbers is fine", func(t *testing.T) {
		if _, err := readAs(t, "payday", tenantOf("payday")+holder(`
  string email = 8;
  bytes badge = 16;
`)); err != nil {
			t.Fatal(err)
		}
	})

	for _, tt := range []struct {
		what string
		body string
		says string
	}{{
		what: "redeclaring one of payday's with another type",
		body: `  int64 alias = 4;`,
		says: `4 is payday's "alias"`,
	}, {
		what: "renaming one of payday's",
		body: `  string handle = 4;`,
		says: `redeclared as "handle"`,
	}} {
		t.Run(tt.what+" is refused", func(t *testing.T) {
			// The overlay has already been merged by the time a generator sees
			// it, so what is written here is the merged file: payday's Holder
			// with that number replaced.
			src := tenantOf("payday") + strings.Replace(holder(""), "  string alias = 4;", tt.body, 1)

			_, err := readAs(t, "payday", src)
			if err == nil {
				t.Fatal("generated anyway")
			}
			if !strings.Contains(err.Error(), tt.says) {
				t.Fatalf("the refusal does not say %q:\n%s", tt.says, err)
			}
			t.Log(err)
		})
	}

	t.Run("an app's own entity is the app's entirely", func(t *testing.T) {
		// Nothing here is payday's, so nothing is checked against it.
		if _, err := read(t, tenant+entity("Robot", `domain: 7, global: {}`)); err != nil {
			t.Fatal(err)
		}
	})
}

// tenantOf is payday's Tenant in the given package, for a test that has to
// compile one.
func tenantOf(pkg string) string {
	return `
message Tenant {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  string alias = 4 [(orm.field) = {unique: true}];
  string name = 5;
  string desc = 6;
  map<string, string> labels = 7;
  google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 1, tenant: {}, own: OWN_TENANT, erase: {hard: {}}};
}`
}

// TestListIsRefusedWhenItWouldBeWrong is the half of a List worth generating:
// the parts that are the same for every entity and that people get wrong.
func TestListIsRefusedWhenItWouldBeWrong(t *testing.T) {
	robot := func(list string) string {
		return tenant + `
message Robot {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  string alias = 4;
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 7, tenanted: {via: "tenant"}, list: {` + list + `}, erase: {hard: {}}};
}`
	}

	t.Run("a well-formed list reads", func(t *testing.T) {
		s, err := read(t, robot(`order: [{field: {name: "date_created"}}, {field: {name: "id"}}], max: 100, by: [{name: "ref"}]`))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range s.Entities {
			if v.GoName() != "Robot" {
				continue
			}
			if v.List == nil || len(v.List.Order) != 2 || v.List.Max != 100 {
				t.Fatalf("list: %+v", v.List)
			}
			if v.List.Size != 50 {
				t.Errorf("size: %d, and nothing said one", v.List.Size)
			}
		}
	})

	for _, tt := range []struct{ what, list, says string }{{
		what: "an order that does not end in the key",
		list: `order: [{field: {name: "date_created"}}], max: 100`,
		says: "has to end in the key",
	}, {
		what: "no cap on the page",
		list: `order: [{field: {name: "id"}}]`,
		says: "`max` is required",
	}, {
		what: "a cap below what nothing-said gets",
		list: `order: [{field: {name: "id"}}], size: 100, max: 10`,
		says: "more than it may have",
	}, {
		what: "an order on a column that is not there",
		list: `order: [{field: {name: "nope"}}, {field: {name: "id"}}], max: 100`,
		says: `no field "nope"`,
	}, {
		what: "a filter on a column that is not there",
		list: `order: [{field: {name: "id"}}], max: 100, by: [{name: "nope"}]`,
		says: `no field or edge "nope"`,
	}, {
		what: "an edge to read along that is not there",
		list: `order: [{field: {name: "id"}}], max: 100, with: [{name: "nope"}]`,
		says: `no edge "nope"`,
	}} {
		t.Run(tt.what+" is refused", func(t *testing.T) {
			_, err := read(t, robot(tt.list))
			if err == nil {
				t.Fatal("generated anyway")
			}
			if !strings.Contains(err.Error(), tt.says) {
				t.Fatalf("the refusal does not say %q:\n%s", tt.says, err)
			}
			t.Log(err)
		})
	}

	t.Run("an order no index covers is a warning and not a refusal", func(t *testing.T) {
		// A warning because a small table is a real thing: a hundred rows of
		// configuration do not need an index, and refusing to generate for
		// them would be insisting on a cost nobody is paying.
		s, err := read(t, robot(`order: [{field: {name: "date_created"}}, {field: {name: "id"}}], max: 100`))
		if err != nil {
			t.Fatal(err)
		}

		ws := pdgen.Warnings(s)
		if len(ws) != 1 {
			t.Fatalf("warnings: %v", ws)
		}
		if !strings.Contains(ws[0], "no index begins with (date_created, id)") {
			t.Errorf("the warning does not say what is missing:\n%s", ws[0])
		}
		// And it says what to write, since a warning somebody has to go and
		// research is a warning somebody ignores.
		if !strings.Contains(ws[0], "indexes:") {
			t.Errorf("the warning does not say what to write:\n%s", ws[0])
		}
		t.Log(ws[0])
	})

	t.Run("and is silent once the index is there", func(t *testing.T) {
		s, err := read(t, tenant+`
message Robot {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];
  option (orm.message) = {
    rpc: {crud: true}
    indexes: [{name: "page", refs: [{name: "date_created", number: 15}, {name: "id", number: 1}]}]
  };
  option (payday.entity) = {
    domain: 7
    tenanted: {via: "tenant"}
    list: {order: [{field: {name: "date_created"}}, {field: {name: "id"}}], max: 100}
    erase: {hard: {}}
  };
}`)
		if err != nil {
			t.Fatal(err)
		}
		if ws := pdgen.Warnings(s); len(ws) != 0 {
			t.Fatalf("warnings: %v", ws)
		}
	})
}

// TestAWatchWithoutAVersionIsRefused is the one refusal that is about what
// happens on the **client**.
//
// A watch sends state rather than deltas, so a subscriber keeps what it was
// last told about a row and replaces it. Two answers about one row arrive out
// of order often enough to be ordinary -- a snapshot racing an event, a
// reconnection, an outbox draining late -- and without a version the
// replacement is unconditional: a stale answer overwrites a fresh one, and
// nothing anywhere fails.
//
// It is refused at generation because it cannot be worked around later. Nothing
// outside the row says which of two copies of it is newer.
func TestAWatchWithoutAVersionIsRefused(t *testing.T) {
	_, err := read(t, `
		message Tenant {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			string alias = 4 [(orm.field) = {unique: true}];
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {domain: 1, tenant: {}, erase: {hard: {}}};
		}
		message Robot {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			Tenant tenant = 2 [(orm.edge) = {immutable: true}];
			string alias = 4;
			google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {
				domain: 7
				tenanted: {via: "tenant"}
				list: {order: [{field: {name: "date_created"}}, {field: {name: "id"}}], by: [{name: "ref"}], size: 20, max: 100}
				watch: {}
			};
		}
	`)
	if err == nil {
		t.Fatal("a watch with nothing to order two answers by was generated")
	}
	if !strings.Contains(err.Error(), "watch: needs a version field") {
		t.Fatalf("the refusal does not say why: %s", err)
	}

	// And it says what to write, at a number that is free.
	if !strings.Contains(err.Error(), "date_updated = 13 [(orm.field) = {version: {}}]") {
		t.Fatalf("the refusal does not say what to write: %s", err)
	}
}

// TestAListWithoutAWatchNeedsNoVersion, because a list is answered once: there
// are no two answers to put in order.
func TestAListWithoutAWatchNeedsNoVersion(t *testing.T) {
	_, err := read(t, `
		message Tenant {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			string alias = 4 [(orm.field) = {unique: true}];
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {domain: 1, tenant: {}, erase: {hard: {}}};
		}
		message Robot {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			Tenant tenant = 2 [(orm.edge) = {immutable: true}];
			string alias = 4;
			google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {
				domain: 7
				tenanted: {via: "tenant"}
				list: {order: [{field: {name: "date_created"}}, {field: {name: "id"}}], size: 20, max: 100}
				erase: {hard: {}}
			};
		}
	`)
	if err != nil {
		t.Fatalf("a list needs no version and was refused: %s", err)
	}
}

// TestAWatchWithNothingToNameARowByIsRefused is the other half of what a watch
// needs, and it was found by scaffolding one.
//
// `pd entity add --watch` wrote an entity whose list declared no filters, and
// what came out was a generated stream referring to a field the filter message
// does not have -- a compile error in the app, in generated code somebody then
// has to read. A watch says which rows it is about, so a filter that cannot
// name one by reference gives it nothing to say.
//
// It is refused rather than added silently: `by:` is the list of things a
// caller may filter on, and quietly putting one more in it is the generator
// deciding what an API offers.
func TestAWatchWithNothingToNameARowByIsRefused(t *testing.T) {
	_, err := read(t, `
		message Tenant {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			string alias = 4 [(orm.field) = {unique: true}];
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {domain: 1, tenant: {}, erase: {hard: {}}};
		}
		message Robot {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			Tenant tenant = 2 [(orm.edge) = {immutable: true}];
			string alias = 4;
			google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];
			google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {
				domain: 7
				tenanted: {via: "tenant"}
				list: {order: [{field: {name: "date_created"}}, {field: {name: "id"}}], by: [{name: "alias"}], size: 20, max: 100}
				watch: {}
			};
		}
	`)
	if err == nil {
		t.Fatal("a watch with no way to name a row was generated")
	}
	if !strings.Contains(err.Error(), "needs `ref` among the list's `by:`") {
		t.Fatalf("the refusal does not say why: %s", err)
	}

	// And the suggestion keeps what the schema already had rather than
	// replacing it.
	if !strings.Contains(err.Error(), `list: {by: [{name: "ref"}, {name: "alias"}]}`) {
		t.Fatalf("the refusal throws away the filters that were declared: %s", err)
	}
}

// TestAHeaderFieldOfTheWrongKindIsRefused is the check that makes a reflective
// read safe.
//
// `payday/header` matches on the name and then on the kind, so a `name` that is
// an int is a field that **silently reads as absent**: a page falls back to the
// alias, or to the identifier, and nothing anywhere says why. That is the class
// worth refusing, and it is the only thing about the header that is.
func TestAHeaderFieldOfTheWrongKindIsRefused(t *testing.T) {
	_, err := read(t, `
		message Tenant {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			string alias = 4 [(orm.field) = {unique: true}];
			int64 name = 5;
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {domain: 1, tenant: {}, erase: {hard: {}}};
		}
	`)
	if err == nil {
		t.Fatal("a name nothing generic can read was generated")
	}
	if !strings.Contains(err.Error(), "expects a TYPE_STRING") {
		t.Fatalf("the refusal does not say what is expected: %s", err)
	}
	if !strings.Contains(err.Error(), "Call it something else if it is not that") {
		t.Fatalf("the refusal does not say the way out: %s", err)
	}
}

// TestAnEntityWithNoHeaderIsFine, which is payday's own Audit and Outbox.
//
// Their early fields are structural rather than descriptive -- a trail row's
// 4..7 are the trace, the action, the object and the patch. A rule that made
// every entity carry a name would be one payday exempts itself from twice on
// the first day, and that is the shape of a rule that is wrong.
func TestAnEntityWithNoHeaderIsFine(t *testing.T) {
	_, err := read(t, `
		message Tenant {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			string alias = 4 [(orm.field) = {unique: true}];
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {domain: 1, tenant: {}, erase: {hard: {}}};
		}
		message Trail {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			bytes tenant_id = 2 [(orm.field) = {type: TYPE_UUID}];
			string action = 5;
			bytes object_id = 6 [(orm.field) = {type: TYPE_UUID}];
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {domain: 8, tenanted: {field: "tenant_id"}, erase: {hard: {}}};
		}
	`)
	if err != nil {
		t.Fatalf("an entity whose early fields are structural was refused: %s", err)
	}
}

// TestAHeaderFieldSomewhereElseIsRefused, because the numbers **are** the rule.
//
// 1 is the key, 2 is the tenant, 4..7 are the alias, the name, the description
// and the labels. An entity that does not want one of them leaves the number
// empty -- which is what keeps payday able to add a header field later without
// every app having spent that number on something else.
func TestAHeaderFieldSomewhereElseIsRefused(t *testing.T) {
	_, err := read(t, `
		message Tenant {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			string alias = 4 [(orm.field) = {unique: true}];
			string name = 9;
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {domain: 1, tenant: {}, erase: {hard: {}}};
		}
	`)
	if err == nil {
		t.Fatal("a header field somewhere else was generated")
	}
	if !strings.Contains(err.Error(), "the header is at 5 in every entity payday ships") {
		t.Fatalf("the refusal does not say the rule: %s", err)
	}
}

// TestAnEntityNobodyNamesIsOrdinary.
//
// A resource with no name a person writes -- a measurement, a sample, a row of
// a log -- simply does not declare the field. There is no switch to turn off:
// `alias` is found by name, so an entity without one gets no naming, no folding
// and no slug, and still gets the wall.
func TestAnEntityNobodyNamesIsOrdinary(t *testing.T) {
	s, err := read(t, `
		message Tenant {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			string alias = 4 [(orm.field) = {unique: true}];
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {domain: 1, tenant: {}, erase: {hard: {}}};
		}
		message Reading {
			bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
			Tenant tenant = 2 [(orm.edge) = {immutable: true}];
			double celsius = 8;
			option (orm.message) = {rpc: {crud: true}};
			option (payday.entity) = {domain: 7, tenanted: {via: "tenant"}, erase: {hard: {}}};
		}
	`)
	if err != nil {
		t.Fatalf("an entity with no alias was refused: %s", err)
	}

	for _, v := range s.Entities {
		if v.GoName() == "Reading" && v.Alias {
			t.Fatal("an entity with no alias was given naming anyway")
		}
	}
}

// TestSayingNothingIsBehindTheWall is the default, and the reason it is this one
// rather than the other.
//
// Getting tenancy wrong fails in two directions and they are not alike: assuming
// a wall hides every row, and assuming none shows every row to everybody. The
// first is noticed within minutes because the screen is empty; the second is
// noticed by whoever it happens to. So the safe thing to assume is the loud one.
//
// And it cannot be wrong quietly. An entity with a `tenant` edge to the tenant
// gets the right predicate; one whose edge goes elsewhere is refused; one with no
// such edge is refused. Never a wall that narrows to the wrong rows.
//
// What it buys is the shape a rule should have: the **dangerous** case is the one
// written down. `global: {}` can be searched for; the ordinary case says nothing.
func TestSayingNothingIsBehindTheWall(t *testing.T) {
	s, err := read(t, tenant+entity("Robot", `domain: 7`, `Tenant tenant = 2 [(orm.edge) = {}];`))
	if err != nil {
		t.Fatalf("an entity with a tenant edge and no declaration was refused: %s", err)
	}

	for _, v := range s.Entities {
		if v.GoName() != "Robot" {
			continue
		}
		if v.IsGlobal || v.IsTenant {
			t.Fatal("saying nothing was read as not being behind the wall")
		}
		if len(v.Via) != 1 || v.Via[0] != "tenant" {
			t.Fatalf("the assumed path is %v and should be [tenant]", v.Via)
		}
	}
}

// TestSayingNothingWithNoTenantEdgeSaysItWasAssumed.
//
// A refusal about a path nobody wrote reads as nonsense unless it says the path
// was assumed, and says which of the two things to write instead.
func TestSayingNothingWithNoTenantEdgeSaysItWasAssumed(t *testing.T) {
	_, err := read(t, tenant+entity("Robot", `domain: 7`))
	if err == nil {
		t.Fatal("an entity with nothing to be behind the wall of was generated")
	}
	for _, want := range []string{
		"Nothing here declared tenancy",
		"fails loudly rather than the one that leaks",
		"global: {}",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q:\n%s", want, err)
		}
	}
}

// TestNotBehindTheWallIsStillSaidOutLoud, which is the half that does not get a
// default and never will.
func TestNotBehindTheWallIsStillSaidOutLoud(t *testing.T) {
	s, err := read(t, tenant+entity("Fleet", `domain: 9, global: {}`))
	if err != nil {
		t.Fatalf("a global entity was refused: %s", err)
	}

	for _, v := range s.Entities {
		if v.GoName() == "Fleet" && !v.IsGlobal {
			t.Fatal("global: {} was not read as being outside the wall")
		}
	}
}

// TestTheSecondAxisIsFieldThree.
//
// Field 3 is the app's, and the number is the whole declaration -- there is no
// option to write. What payday fixes is the shape, because a slot whose shape
// is not fixed is one nothing generic can be taught to read, and then it may as
// well have been 8.
func TestTheSecondAxisIsFieldThree(t *testing.T) {
	app := func(vs ...string) string {
		return tenant + `
message Site {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  string alias = 4;
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 8, tenanted: {via: "tenant"}, erase: {hard: {}}};
}
` + strings.Join(vs, "\n")
	}

	asset := func(at string) string {
		return `
message Asset {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  ` + at + `
  string alias = 4;
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 9, tenanted: {via: "tenant"}, erase: {hard: {}}};
}`
	}

	t.Run("an edge at 3 is the set, and payday does not name it", func(t *testing.T) {
		s, err := read(t, app(asset(`Site site = 3 [(orm.edge) = {}];`)))
		if err != nil {
			t.Fatal(err)
		}
		if s.Set == nil {
			t.Fatal("nothing was read as the set")
		}
		if got := string(s.Set.FullName()); got != "test.Site" {
			t.Fatalf("the set is %s", got)
		}
		if !s.Set.IsSet {
			t.Fatal("the set does not know it is one")
		}

		for _, v := range s.Entities {
			if v.GoName() == "Asset" && v.Set != "site" {
				t.Fatalf("Asset's set edge is %q", v.Set)
			}
			if v.GoName() == "Tenant" && v.Set != "" {
				t.Fatal("the tenant was read as being in a set")
			}
		}
	})

	t.Run("an app that declares none has none", func(t *testing.T) {
		s, err := read(t, app(asset(``)))
		if err != nil {
			t.Fatal(err)
		}
		if s.Set != nil {
			t.Fatalf("a set was invented: %s", s.Set.FullName())
		}
	})

	for _, tc := range []struct {
		what string
		at   string
		says string
	}{
		{
			// The one that compiles and means nothing. A predicate cannot be
			// built from it, and the day something generic asks "what set is
			// this row in" it reads a name as a set.
			"a scalar at 3", `string region = 3;`, "an edge to it",
		},
		{
			"a list at 3", `repeated Site site = 3 [(orm.edge) = {}];`, "is one set",
		},
	} {
		t.Run(tc.what+" is refused", func(t *testing.T) {
			_, err := read(t, app(asset(tc.at)))
			if err == nil {
				t.Fatal("it was taken")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("said: %s", err)
			}
		})
	}

	t.Run("two entities meaning two different sets is refused", func(t *testing.T) {
		second := `
message Cell {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  string alias = 4;
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 10, tenanted: {via: "tenant"}, erase: {hard: {}}};
}
message Reading {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  Cell cell = 3 [(orm.edge) = {}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 11, tenanted: {via: "tenant"}, erase: {hard: {}}};
}`
		_, err := read(t, app(asset(`Site site = 3 [(orm.edge) = {}];`), second))
		if err == nil {
			t.Fatal("two axes wearing one number were taken")
		}
		if !strings.Contains(err.Error(), "field 3 is one axis") {
			t.Fatalf("said: %s", err)
		}
	})

	t.Run("a set inside a set is refused", func(t *testing.T) {
		// The hierarchy the slot exists to avoid: put Area above Site and next
		// there is a Region above Area. That is what labels are for.
		src := tenant + `
message Area {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 8, tenanted: {via: "tenant"}, erase: {hard: {}}};
}
message Site {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  Area area = 3 [(orm.edge) = {}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 9, tenanted: {via: "tenant"}, erase: {hard: {}}};
}
message Asset {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  Site site = 3 [(orm.edge) = {}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 10, tenanted: {via: "tenant"}, erase: {hard: {}}};
}`
		_, err := read(t, src)
		if err == nil {
			t.Fatal("a hierarchy was taken")
		}
		if !strings.Contains(err.Error(), "field 3 is one axis") &&
			!strings.Contains(err.Error(), "a set inside a set") {
			t.Fatalf("said: %s", err)
		}
	})

	t.Run("a set outside the wall is refused", func(t *testing.T) {
		// Two tenants can name it, and then narrowing to it crosses the wall
		// with nothing failing.
		src := tenant + `
message Site {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 8, global: {}, erase: {hard: {}}};
}
message Asset {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  Site site = 3 [(orm.edge) = {}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 9, tenanted: {via: "tenant"}, erase: {hard: {}}};
}`
		_, err := read(t, src)
		if err == nil {
			t.Fatal("a set outside the wall was taken")
		}
		if !strings.Contains(err.Error(), "crosses the wall") {
			t.Fatalf("said: %s", err)
		}
	})
}

// TestSeveralTenantColumnsAreOr.
//
// The one construct here that makes a row *more* visible, and it exists for the
// trail: a write has a tenant whose row changed and a tenant whose actor made
// it, and both are parties to the record. Neither should have to hold a scope
// wide enough to see the other in order to read what happened.
func TestSeveralTenantColumnsAreOr(t *testing.T) {
	s, err := read(t, tenant+`
message Note {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  bytes about_id = 8 [(orm.field) = {type: TYPE_UUID}];
  bytes by_id = 9 [(orm.field) = {type: TYPE_UUID}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {
    domain: 7
    tenanted: {field: "about_id", field: "by_id"}
  , erase: {hard: {}}};
}`)
	if err != nil {
		t.Fatal(err)
	}

	for _, v := range s.Entities {
		if v.GoName() != "Note" {
			continue
		}
		if len(v.Columns) != 2 || v.Columns[0] != "about_id" || v.Columns[1] != "by_id" {
			t.Fatalf("columns: %q", v.Columns)
		}
	}

	t.Run("and a column that is not there is still refused", func(t *testing.T) {
		_, err := read(t, tenant+`
message Note {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  bytes about_id = 8 [(orm.field) = {type: TYPE_UUID}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {
    domain: 7
    tenanted: {field: "about_id", field: "nowhere"}
  , erase: {hard: {}}};
}`)
		if err == nil {
			t.Fatal("a column nothing declares was taken")
		}
		if !strings.Contains(err.Error(), "nowhere") {
			t.Fatalf("said: %s", err)
		}
	})
}

// TestAnEntityHasToSayWhatEraseDoes.
//
// The two failures are not alike, which is why this is a refusal rather than a
// default. Assuming soft wrongly leaves rows somebody meant to be gone, and
// that is noticed by looking at them; assuming hard wrongly destroys them, and
// that is noticed by somebody asking for one back.
//
// payday cannot default it either way, because saying it softly means carrying
// a field and payday does not add fields to an app's schema. So the only thing
// left is to refuse and say what to write.
func TestAnEntityHasToSayWhatEraseDoes(t *testing.T) {
	robot := func(field, opt string) string {
		return tenant + `
message Robot {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {}];
  ` + field + `
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 7, tenanted: {via: "tenant"}` + opt + `};
}`
	}

	const erased = `google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];`

	t.Run("a field marked erased is soft, and says nothing else", func(t *testing.T) {
		s, err := read(t, robot(erased, ``))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range s.Entities {
			if v.GoName() == "Robot" && v.IsHard {
				t.Fatal("an entity with an erased field was read as hard")
			}
		}
	})

	t.Run("no field and no word is refused", func(t *testing.T) {
		_, err := read(t, robot(``, ``))
		if err == nil {
			t.Fatal("an entity that destroys rows without saying so was taken")
		}
		if !strings.Contains(err.Error(), "erase") || !strings.Contains(err.Error(), "date_erased") {
			t.Fatalf("said: %s", err)
		}
	})

	t.Run("no field and the word is hard", func(t *testing.T) {
		s, err := read(t, robot(``, `, erase: {hard: {}}`))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range s.Entities {
			if v.GoName() == "Robot" && !v.IsHard {
				t.Fatal("it was not read as hard")
			}
		}
	})

	t.Run("both is refused, because they are two different answers", func(t *testing.T) {
		// The field is what makes an Erase a stamp, so the row would be softly
		// erased while the schema says it is destroyed.
		_, err := read(t, robot(erased, `, erase: {hard: {}}`))
		if err == nil {
			t.Fatal("a contradiction was taken")
		}
		if !strings.Contains(err.Error(), "two different answers") {
			t.Fatalf("said: %s", err)
		}
	})
}

// A field that has presence in the API and nowhere to keep it.
//
// A message field with no `nullable`, no `default` and no marker generates a
// NOT NULL column while the API beside it still has `Has…`, because a message
// field has presence in proto whatever the column does. So a caller asks
// whether a value is set, is told yes, and reads a zero somebody wrote because
// the column would not take null -- a row saying a thing happened at the
// beginning of the epoch.
//
// The first version of this test asserted nothing. Its schemas were malformed,
// so `Read` found no entities and returned without checking any, and four
// subtests passed by never reaching the code they were about. What caught it
// was that the one expected to fail did not.
func TestAFieldThatLiesAboutPresence(t *testing.T) {
	// The entity a subtest is about, built the way every other test here builds
	// one -- so that a mistake in the schema is a mistake in the schema rather
	// than a test that quietly checks nothing.
	of := func(t *testing.T, field string) error {
		t.Helper()

		_, err := read(t, tenant+entity("Thing", `domain: 9, global: {}`, field))

		return err
	}

	// The whole point: this is the only shape refused.
	t.Run("refused", func(t *testing.T) {
		err := of(t, `google.protobuf.Timestamp date_seen = 8;`)
		if err == nil {
			t.Fatal("a field that lies about presence was accepted")
		}
		for _, want := range []string{"date_seen", "presence"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal does not say %q: %v", want, err)
			}
		}
	})

	// Both fixes are the schema's to choose and they mean different things, so
	// the generator names them rather than picking one.
	for _, tt := range []struct {
		what string
		decl string
	}{
		{"nullable", `[(orm.field) = {nullable: true}]`},
		{"a default", `[(orm.field) = {default: ""}]`},
	} {
		t.Run("said with "+tt.what, func(t *testing.T) {
			if err := of(t, `google.protobuf.Timestamp date_seen = 8 `+tt.decl+`;`); err != nil {
				t.Fatalf("saying %s was not enough: %v", tt.what, err)
			}
		})
	}

	// The stamps payday writes itself are exempt, and the rule is stated as
	// their declarations rather than their names -- an app whose version field
	// is called something else is not caught by a rule about spelling.
	t.Run("a version is not a claim about a caller", func(t *testing.T) {
		if err := of(t, `google.protobuf.Timestamp changed_at = 8 [(orm.field) = {version: {}}];`); err != nil {
			t.Fatalf("a server stamp was read as a claim: %v", err)
		}
	})

	// Written out rather than through the helper, because an erased marker and
	// the helper's `erase: {hard: {}}` are two different answers about the same
	// thing and the schema is refused for saying both.
	t.Run("an erased marker is not a claim about a caller", func(t *testing.T) {
		_, err := read(t, tenant+`
message Thing {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  string alias = 4;
  google.protobuf.Timestamp gone_at = 8 [(orm.field) = {erased: {}}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 9, global: {}};
}`)
		if err != nil {
			t.Fatalf("a server stamp was read as a claim: %v", err)
		}
	})

	// A map has no presence either, and its descriptor says `MessageKind`
	// because its entries are a synthetic message. That is what this check
	// caught the first time it was run against apptest.
	t.Run("a map is not a message field", func(t *testing.T) {
		if err := of(t, `map<string, string> labels = 8;`); err != nil {
			t.Fatalf("a map was read as a message field: %v", err)
		}
	})

	// And an edge is a message field too. Its presence is the foreign key being
	// there rather than a claim about what somebody sent -- which is why this
	// walks `Fields()` and not `Props()`.
	t.Run("an edge is not a field", func(t *testing.T) {
		_, err := read(t, tenant+entity("Thing", `domain: 9, tenanted: {via: "tenant"}`,
			`Tenant tenant = 2 [(orm.edge) = {}];`))
		if err != nil {
			t.Fatalf("an edge was read as a field: %v", err)
		}
	})
}

// A filter may be about an edge, which is a column.
//
// "Everyone in this tenant", "every team in this site" -- the common filters,
// and each is `WHERE <fk>_id = ?` against an index rather than a join. That it
// was refused made those a hand-written RPC apiece, for what a WHERE clause
// does.
//
// What the filter carries is the target's **ref**, so a caller names it the way
// they name it everywhere else: by identifier, or by the alias they typed.
func TestAFilterMayBeAboutAnEdge(t *testing.T) {
	s, err := read(t, tenant+entity("Robot", `domain: 7, tenanted: {via: "tenant"}, list: {order: [{field: {name: "id"}}], max: 100, by: [{name: "ref"}, {name: "tenant"}]}`,
		`Tenant tenant = 2 [(orm.edge) = {}];`))
	if err != nil {
		t.Fatalf("an edge was refused: %v", err)
	}

	var by []pdgen.By
	for _, v := range s.Entities {
		if v.GoName() == "Robot" {
			by = v.List.By
		}
	}

	if len(by) != 2 {
		t.Fatalf("by: %d of them", len(by))
	}
	if !by[0].Ref {
		t.Fatal("the first is not the ref")
	}
	if by[1].Edge != "tenant" || by[1].Target != "Tenant" {
		t.Fatalf("the second is %+v", by[1])
	}
}

// TestAOneToManyEdgeIsRefused, because there is no column on this row to
// compare -- which is a join, and a join wants a reason beside the code that
// acts on it.
func TestAOneToManyEdgeIsRefused(t *testing.T) {
	_, err := read(t, tenant+
		entity("Robot", `domain: 7, tenanted: {via: "tenant"}, list: {order: [{field: {name: "id"}}], max: 100, by: [{name: "parts"}]}`,
			`Tenant tenant = 2 [(orm.edge) = {}];`,
			`repeated Part parts = 8 [(orm.edge) = {}];`)+
		entity("Part", `domain: 8, global: {}`))
	if err == nil {
		t.Fatal("a one-to-many edge was accepted as a filter")
	}
	if !strings.Contains(err.Error(), "join") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
}
