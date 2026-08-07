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
	t.Helper()

	const name = "test/schema.proto"
	whole := "edition = \"2023\";\npackage test;\n" +
		"import \"orm.proto\";\nimport \"payday.proto\";\n" +
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
  option (payday.entity) = {%s};
}
`, name, body, opts)
}

const tenant = `
message Tenant {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  string alias = 4 [(orm.field) = {unique: true}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 1, tenant: {}};
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
		what: "an entity that never said whether it is behind the wall",
		src:  tenant + entity("Robot", `domain: 7`),
		says: "no tenancy",
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
