// protoc-gen-pd writes what a payday schema declared into the code that
// enforces it.
//
// It reads the `(payday.entity)` option beside what `orm` already says, and
// emits one file holding the domain of each entity, the minter that stamps
// identifiers with it, and the wall that narrows every read to the tenants a
// caller may see.
//
// It also **refuses to generate** a schema that left any of those unsaid. That
// is the half worth having: an entity with no domain hands out identifiers that
// say nothing, and an entity that never said whether it is behind the wall sits
// outside it while everything goes on compiling.
//
//	plugins:
//	  - local: [go, tool, github.com/lesomnus/payday/cmd/protoc-gen-pd]
//	    out: .
//	    opt:
//	      - module=github.com/acme/app
//	      - ent=internal/ent
//	      - bare=server/bare
//	      - out=server/pd
//	    strategy: all
//
// `strategy: all` is not optional. The default runs a plugin once per
// directory, and an entity whose tenant is declared in another one is then not
// a generation target when its own file is read.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/lesomnus/payday/internal/pdgen"
)

type opts struct {
	// Ent and Bare are where the other generators were told to put the ent
	// runtime and the generated servers, relative to the module root.
	Ent  string
	Bare string

	// Out is where this plugin's own file goes.
	Out  string
	Name string

	// Stage says which pass this is. A List is an RPC, so it has to be in the
	// contract before the messages and the servers are generated from it --
	// which means this plugin runs twice: once beside protoc-gen-orm-service
	// writing proto, and once with the rest writing Go.
	Stage string

	// Payday is the payday this generation was run from, as a version, and it
	// is written into the one Go file this plugin owns.
	//
	// It is handed over rather than read here: this runs as a plugin under
	// `buf`, in whatever directory buf chose, and the question is about the
	// **app's** module graph. `pdcli.PaydayVersion` asks it where it can be
	// answered.
	Payday string
}

func main() {
	o := opts{
		Ent:    "internal/ent",
		Bare:   "server/bare",
		Out:    "server/pd",
		Name:   "pd.g.go",
		Payday: "(devel)",
	}

	var fs flag.FlagSet
	fs.StringVar(&o.Ent, "ent", o.Ent, "directory of the ent runtime, relative to the module root")
	fs.StringVar(&o.Bare, "bare", o.Bare, "directory of the generated servers")
	fs.StringVar(&o.Out, "out", o.Out, "directory this plugin writes into")
	fs.StringVar(&o.Name, "name", o.Name, "name of the file this plugin writes")
	fs.StringVar(&o.Stage, "stage", "go", "which pass: `proto` writes the List into the contract, `ts` the domain table, `go` everything else")
	fs.StringVar(&o.Payday, "version", o.Payday, "the payday this generation is from, stamped into the generated package")

	protogen.Options{ParamFunc: fs.Set}.Run(func(p *protogen.Plugin) error {
		return run(context.Background(), p, o)
	})
}

func run(ctx context.Context, p *protogen.Plugin, o opts) error {
	p.SupportedEditionsMinimum = descriptorpb.Edition_EDITION_PROTO2
	p.SupportedEditionsMaximum = descriptorpb.Edition_EDITION_MAX
	p.SupportedFeatures = uint64(0 |
		pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL |
		pluginpb.CodeGeneratorResponse_FEATURE_SUPPORTS_EDITIONS,
	)

	g := graph.NewGraph()
	if err := graph.ParseFiles(ctx, g, p.Files); err != nil {
		return fmt.Errorf("read the schema: %w", err)
	}

	s, err := pdgen.Read(g, p.Files)
	if err != nil {
		return err
	}
	if len(s.Entities) == 0 {
		return nil
	}

	// Here rather than in `pdgen.Read`, because what makes this true is `pd
	// gen` having copied payday's entities in and a library caller has not.
	// See [pdgen.CheckOwn] for what goes missing quietly without it.
	if err := pdgen.CheckOwn(s); err != nil {
		return err
	}

	// The module root is where the message package lives, and everything else
	// is named relative to it -- the same way the orm generators are told
	// about it.
	src := sourceOf(p, s)
	if src == nil {
		return fmt.Errorf("no file to take the module path from")
	}

	switch o.Stage {
	case "proto":
		return writeProto(p, s, src, o)

	case "ts":
		// One file for the whole schema rather than one per source, because
		// what it holds is a table: an app registers every domain there is or
		// registers none, and splitting it would make importing half of it a
		// thing that compiles.
		out := p.NewGeneratedFile("domains.ts", "")
		out.P(pdgen.Ts(s))

		// Where each entity's messages landed, which only the plugin knows:
		// protoc-gen-es keeps the directory a .proto was in and renames the
		// file, and the store's declarations import from there.
		at := map[protoreflect.FullName]string{}
		for _, f := range p.Files {
			if !f.Generate {
				continue
			}
			for _, m := range f.Messages {
				at[m.Desc.FullName()] = f.Desc.Path()
			}
		}

		ent := p.NewGeneratedFile("entities.ts", "")
		ent.P(pdgen.TsEntities(s, func(v *pdgen.Entity) string {
			return pdgen.TsImport(at[v.FullName()])
		}))

		return nil
	}

	// Where the messages are, which is not always where the module root is: an
	// app that puts its generated Go in `api/` still wants the ent runtime and
	// the servers beside `go.mod`, and says so with a `../` in the option. So
	// these are joined rather than concatenated -- `<pkg>` + `/../internal/ent`
	// is not a package, and is not an error either.
	root := src.GoImportPath
	paths := pdgen.Paths{
		Ent:  at(root, o.Ent),
		Bare: at(root, o.Bare),
	}

	// The file is named by its full import path: `module=` in the plugin
	// options is what strips the prefix back off, the same way the orm
	// generators are told where the module root is.
	dir := at(root, o.Out)
	out := p.NewGeneratedFile(path.Join(string(dir), o.Name), dir)
	out.P("// Code generated by protoc-gen-pd. DO NOT EDIT.")
	out.P("// payday: ", o.Payday)
	out.P("")
	out.P("// Package ", pkgName(o.Out), " is what the schema said, as code.")
	out.P("//")
	out.P("// Nothing here is a decision. Every line follows from a `(payday.entity)`")
	out.P("// option, and the reason it is generated rather than written is that the")
	out.P("// line nobody writes is the one that matters -- an entity added to the")
	out.P("// schema arrives here with its wall already on it.")
	out.P("package ", pkgName(o.Out))
	out.P("")

	pdgen.EmitPayday(out, o.Payday)
	pdgen.EmitDomains(out, s)
	pdgen.EmitMinter(out, s, paths)
	pdgen.EmitWall(out, s, paths)
	pdgen.EmitGrouped(out, s, paths)
	for _, w := range pdgen.Warnings(s) {
		fmt.Fprintln(os.Stderr, "protoc-gen-pd: "+w)
	}

	pdgen.EmitSink(out, s, paths, root)
	pdgen.EmitGate(out, s, paths, root)
	pdgen.EmitAudit(out, s, paths, root)
	pdgen.EmitWatchRecorder(out, s, paths)
	pdgen.EmitOutbox(out, s, paths)
	pdgen.EmitBatch(out, s, paths, root, p.Files)

	return nil
}

// sourceOf answers with a file that holds an entity, which is what the module
// path is read from.
func sourceOf(p *protogen.Plugin, s *pdgen.Schema) *protogen.File {
	want := map[string]bool{}
	for _, v := range s.Entities {
		want[string(v.FullName())] = true
	}

	for _, f := range p.Files {
		if !f.Generate {
			continue
		}
		for _, m := range f.Messages {
			if want[string(m.Desc.FullName())] {
				return f
			}
		}
	}

	return nil
}

// writeProto is the first pass: the List of each entity that declared one, as
// an overlay onto the contract.
//
// One file out per file in, and beside it: that is what the merge step pairs
// them by, and it is also what keeps an entity's List where a reader of that
// entity would look for it.
func writeProto(p *protogen.Plugin, s *pdgen.Schema, _ *protogen.File, o opts) error {
	at := map[protoreflect.FullName]*pdgen.Entity{}
	for _, v := range s.Entities {
		at[v.FullName()] = v
	}

	for _, f := range p.Files {
		if !f.Generate {
			continue
		}

		vs := []*pdgen.Entity{}
		for _, m := range f.Messages {
			if v, ok := at[m.Desc.FullName()]; ok {
				vs = append(vs, v)
			}
		}

		body := pdgen.Proto(vs, string(f.Desc.Package()))
		if body == "" {
			continue
		}

		name := strings.TrimSuffix(path.Base(f.Desc.Path()), ".proto")
		out := p.NewGeneratedFile(path.Join(path.Dir(f.Desc.Path()), name+"_svc.pd.proto"), "")
		out.P(body)
	}

	return nil
}

// at is the import path `rel` names, read from `base`.
func at(base protogen.GoImportPath, rel string) protogen.GoImportPath {
	return protogen.GoImportPath(path.Join(string(base), rel))
}

func pkgName(dir string) string {
	vs := strings.Split(strings.Trim(dir, "/"), "/")
	return vs[len(vs)-1]
}
