package pdcli

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/lesomnus/payday/internal/pdgen"
)

// Reading the app's own schema, which is what `doctor` said it did and did not.
//
// The sentence was *reads the schema the way the generator does, so that
// everything `pd gen` refuses is refused here too*, and what stood under it
// globbed payday's shipped entity files and checked that overlay **filenames**
// matched. It never opened the app's protos. So an entity `pd gen` refuses
// outright -- a domain already taken, a `watch:` with no `list:`, a tenant path
// through a mutable edge -- got *looks like an app that generates* and exit 0.
//
// That is worse than not checking. Somebody reads the sentence, trusts the exit
// code, and finds out from `pd gen` in CI or from a generated file that will
// not compile.
//
// # Why it costs a buf
//
// Because reading a schema the way the generator does means reading what the
// generator is handed, and that is a `CodeGeneratorRequest` -- descriptors with
// every import resolved and every custom option in its concrete type. Parsing
// the files here would be a second, worse compiler, and the findings it
// invented would be about its own gaps.
//
// So this shells out for a descriptor set and walks the same path the plugin
// does: descriptors, `protogen`, the orm graph, `pdgen.Read`. It is one `buf
// build`, which is the cheapest thing `pd gen` already does on every run.

// doctorEntities reads the app's schema and answers with what `pd gen` would
// refuse.
//
// One finding, not many: `pdgen.Read` stops at the first thing it cannot
// accept, so a list here would be a list of one wearing a plural.
func doctorEntities(ctx context.Context, l Layout) []Finding {
	// An app that has never generated has no `buf.lock`, so its imports do not
	// resolve and there is no schema here to read -- only the same list of
	// unresolved imports for every app in that state.
	//
	// Silent rather than reported, and the reason is what the next thing they
	// type is: `pd gen` writes the lock and fetches. A finding here would be a
	// fatal about a state that fixes itself, on the one run where somebody is
	// least able to tell a real problem from a first-time one.
	if _, err := os.Stat(filepath.Join(l.Work, "buf.lock")); err != nil {
		return nil
	}

	fs, err := describe(ctx, l)
	if err != nil {
		// A schema that does not **compile** is not this check's finding to
		// report: buf says so itself, in its own words, with a line and a
		// column. Repeating it here in worse words would bury the one thing
		// this function knows that buf does not.
		return []Finding{{
			What:  "the schema does not compile, so nothing further can be said about it",
			Fix:   strings.TrimRight(err.Error(), "\n"),
			Fatal: true,
		}}
	}

	generate, err := mine(l, fs)
	if err != nil {
		return []Finding{{What: fmt.Sprintf("cannot read %s/: %s", DirProto, err), Fatal: true}}
	}

	if _, err := read(fs, generate); err != nil {
		return []Finding{{
			What:  "pd gen would refuse this schema: " + err.Error(),
			Fatal: true,
		}}
	}

	return nil
}

// describe is the app's schema as descriptors, from buf.
func describe(ctx context.Context, l Layout) (*descriptorpb.FileDescriptorSet, error) {
	name, err := Buf(ctx)
	if err != nil {
		return nil, err
	}

	// To a file rather than to a pipe: `buf build -o -` writes an image, and an
	// image is a superset that carries more than a descriptor set does. Named
	// with the extension, because that is how buf decides the format.
	out, err := os.CreateTemp("", "*.binpb")
	if err != nil {
		return nil, err
	}
	out.Close()
	defer os.Remove(out.Name())

	cmd := exec.CommandContext(ctx, name, "build", "-o", out.Name())
	cmd.Dir = l.Work

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w\n%s%s", err, stderr.String(), whyStale(stderr.Bytes()))
	}

	b, err := os.ReadFile(out.Name())
	if err != nil {
		return nil, err
	}

	v := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(b, v); err != nil {
		return nil, err
	}

	return v, nil
}

// mine is which of those files are the app's own.
//
// # Why it walks the disk rather than filtering the set
//
// Because buf names a file relative to the **module** root, which is
// `proto/` -- so the app's `app/holder.proto` and the dependency's
// `payday/entity.proto` are two paths of the same shape and there is nothing in
// either to tell them apart. A prefix rule over the set would have to know
// which top-level names are somebody else's, which is a list that goes stale
// the first time payday publishes a second module.
//
// What is on disk under `proto/` **and** in the image is the app's by
// definition, generated files included -- and those are its own too: an app that
// regenerated into a state `pd gen` would refuse should hear about it here.
func mine(l Layout, fs0 *descriptorpb.FileDescriptorSet) ([]string, error) {
	root := l.Path(DirProto)

	// What buf compiled, which is not everything under `proto/`: an overlay in
	// `ext/` is **merged** into a generated file rather than compiled beside it,
	// so it has no descriptor and naming one as a generation target is an error
	// about a file that is doing exactly what it should.
	built := map[string]bool{}
	for _, f := range fs0.GetFile() {
		built[f.GetName()] = true
	}

	var vs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".proto") {
			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}

		if v := filepath.ToSlash(rel); built[v] {
			vs = append(vs, v)
		}

		return nil
	})

	return vs, err
}

// read walks the path the plugin walks: descriptors, protogen, the orm graph,
// and the generator's own reader.
func read(fs *descriptorpb.FileDescriptorSet, generate []string) (*pdgen.Schema, error) {
	if len(generate) == 0 {
		return nil, fmt.Errorf("no schema found under %s/", DirProto)
	}

	p, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: generate,
		ProtoFile:      fs.GetFile(),
	})
	if err != nil {
		return nil, err
	}

	g := graph.NewGraph()
	if err := graph.ParseFiles(context.Background(), g, p.Files); err != nil {
		return nil, fmt.Errorf("orm: %w", err)
	}

	return pdgen.Read(g, p.Files)
}
