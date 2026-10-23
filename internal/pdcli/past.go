package pdcli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

// Past is what an app declared of its past (payday#151): that payday's `Event`
// and `Fact` are among its entities, and the domain each takes.
//
//	option (payday.app) = {past: {event: 17, fact: 16}};
//
// It is read off the schema before anything is compiled, for the reason
// [Layout.ProtoPkg] is: it decides what `pd gen` writes for buf to compile --
// the two copies, numbered -- so it cannot wait for a compile. `pdgen.Read`
// reads the same option again off the compiled schema, and holds the copies to
// it.
type Past struct {
	Event uint32
	Fact  uint32

	// At is the file that declared it, as a person would type it.
	At string
}

// pastFile is one of payday's past as it ships: the name it is copied under,
// the message it declares, and the marker that message carries.
type pastFile struct {
	Name    string
	Message string
	Own     pdpb.Own
}

var pastFiles = []pastFile{
	{"event.proto", "Event", pdpb.Own_OWN_EVENT},
	{"fact.proto", "Fact", pdpb.Own_OWN_FACT},
}

// Domain is the number the declaration gives the entity of that marker.
func (p *Past) Domain(own pdpb.Own) uint32 {
	switch own {
	case pdpb.Own_OWN_EVENT:
		return p.Event
	case pdpb.Own_OWN_FACT:
		return p.Fact
	}

	return 0
}

// PastDir is where payday's past is, for the copy a generation of an app that
// declares one makes: beside [SchemaDir], in the same module.
func PastDir() (string, error) {
	d, err := SchemaDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(filepath.Dir(d), schema.DirPast), nil
}

// readPast is the past this app's own files declare, and nil when none does.
//
// # Why it parses and does not compile
//
// Compiling needs the imports, and the ones that matter are not there yet:
// `payday.proto` comes out of buf's cache, and the app's entities import the
// copies this is about to decide whether to write. So the file is parsed --
// protocompile's parser, which is the compiler's own front half -- and the
// option is taken as written. What it says is read into `payday.App` as text,
// which is the form an option's value is written in, so a misspelt field is
// refused here in the words a compile would use.
//
// Only a file that says `option (payday.app)` is parsed, which is the one
// [readProtoPkg] reads the app's package off; an app that says nothing of
// either has no past.
func readPast(l Layout) (*Past, error) {
	var v *Past

	err := l.eachOwn(func(p string, b []byte) error {
		if !protoAppAt.Match(b) {
			return nil
		}

		app, err := appOf(l.rel(p), b)
		if err != nil {
			return err
		}
		if !app.HasPast() {
			return nil
		}
		if v != nil {
			return fmt.Errorf(
				"%s and %s both declare a past; it is said once, in the file that says the "+
					"package is the app's", v.At, l.rel(p))
		}

		past := app.GetPast()
		if err := pdgen.CheckPastDomains(past.GetEvent(), past.GetFact()); err != nil {
			return fmt.Errorf("%s: %w", l.rel(p), err)
		}

		v = &Past{Event: past.GetEvent(), Fact: past.GetFact(), At: l.rel(p)}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return v, nil
}

// appOf is what one file's `option (payday.app)` lines say, together.
func appOf(name string, b []byte) (*pdpb.App, error) {
	h := reporter.NewHandler(nil)
	f, err := parser.Parse(name, bytes.NewReader(b), h)
	if err != nil {
		return nil, err
	}
	r, err := parser.ResultFromAST(f, false, h)
	if err != nil {
		return nil, err
	}

	app := &pdpb.App{}
	for _, o := range r.FileDescriptorProto().GetOptions().GetUninterpretedOption() {
		parts := o.GetName()
		if len(parts) == 0 || !parts[0].GetIsExtension() ||
			strings.TrimPrefix(parts[0].GetNamePart(), ".") != "payday.app" {
			continue
		}

		text, err := textOf(o)
		if err != nil {
			return nil, fmt.Errorf("%s: option (payday.app): %w", name, err)
		}

		// One option at a time and merged, since `(payday.app).past.event = 17`
		// and `(payday.app).past.fact = 16` are two lines of one value.
		v := &pdpb.App{}
		if err := prototext.Unmarshal([]byte(text), v); err != nil {
			return nil, fmt.Errorf("%s: option (payday.app): %w", name, err)
		}
		proto.Merge(app, v)
	}

	return app, nil
}

// textOf is an option's value as the text format of `payday.App`: what follows
// `(payday.app)` in its name becomes the fields around it, so the dotted form
// and the literal form read the same.
func textOf(o *descriptorpb.UninterpretedOption) (string, error) {
	var v string
	switch {
	case o.AggregateValue != nil:
		v = "{" + o.GetAggregateValue() + "}"
	case o.PositiveIntValue != nil:
		v = strconv.FormatUint(o.GetPositiveIntValue(), 10)
	case o.NegativeIntValue != nil:
		v = strconv.FormatInt(o.GetNegativeIntValue(), 10)
	case o.DoubleValue != nil:
		v = strconv.FormatFloat(o.GetDoubleValue(), 'g', -1, 64)
	case o.IdentifierValue != nil:
		v = o.GetIdentifierValue()
	case o.StringValue != nil:
		v = strconv.Quote(string(o.GetStringValue()))
	}

	parts := o.GetName()[1:]
	if len(parts) == 0 {
		if o.AggregateValue == nil {
			return "", errors.New("is a message, written `{...}`")
		}

		return o.GetAggregateValue(), nil
	}

	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].GetIsExtension() {
			return "", fmt.Errorf("names (%s), which is no field of it", parts[i].GetNamePart())
		}

		v = parts[i].GetNamePart() + ": " + v
		if i > 0 {
			v = "{" + v + "}"
		}
	}

	return v, nil
}

// eachOwn calls fn with every .proto the app wrote itself: what [readPkg] and
// [readProtoPkg] read, and for their reasons -- the copies in
// `proto/<pkg>/payday/` are generated from this rather than a source of it,
// `proto/ext/` holds fragments, and a `.g.proto` is a contract.
func (l Layout) eachOwn(fn func(p string, b []byte) error) error {
	err := filepath.WalkDir(l.Path(DirProto), func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if p == l.Path(l.DirPd()) || p == l.Path(DirExt) {
				return fs.SkipDir
			}

			return nil
		case !strings.HasSuffix(p, ".proto") || strings.HasSuffix(p, ".g.proto"):
			return nil
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		return fn(p, b)
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

// checkPastNames refuses an app whose own schema holds what its past would be
// written over, and says so before buf is run on it.
//
// Two things collide, and buf names neither in words that point here:
//
//   - **A message called `Event` or `Fact`**, in any of the app's packages. In
//     the app's own it is a symbol defined twice; in another it is two Go types
//     of one name in the one Go package everything is generated into, and two
//     ent schemas in one directory. Nested messages are not this: they are
//     named after what they are in.
//   - **A file called `event.proto` or `fact.proto`.** protoc-gen-go names what
//     it writes after the file and not the directory, and every file of an app
//     lands in one Go package, so `proto/app/event.proto` and the copy at
//     `proto/app/payday/event.proto` are both `event.pb.go`.
//
// Only of an app that declares a past: one that does not has neither copy, and
// an `Event` of its own -- a calendar's -- is nobody else's business.
func checkPastNames(l Layout) error {
	if l.Past == nil {
		return nil
	}

	names := map[string]string{}
	for _, v := range pastFiles {
		names[v.Message] = v.Name
	}

	return l.eachOwn(func(p string, b []byte) error {
		base := filepath.Base(p)
		if slices.ContainsFunc(pastFiles, func(v pastFile) bool { return v.Name == base }) {
			return fmt.Errorf(
				"%s is called what payday's copy of its past is, and %s declares one: both would "+
					"be written as %s, since a file of generated Go is named after the .proto "+
					"and every file of an app lands in one Go package.\n\n"+
					"Call it something else -- the messages in it keep their names",
				l.rel(p), l.Past.At, strings.TrimSuffix(base, ".proto")+".pb.go")
		}

		// A file that does not parse is buf's to say, in its own words, with a
		// line and a column.
		f, err := parser.Parse(p, bytes.NewReader(b), reporter.NewHandler(nil))
		if err != nil {
			return nil
		}
		for _, d := range f.Decls {
			m, ok := d.(*ast.MessageNode)
			if !ok {
				continue
			}

			name := string(m.Name.AsIdentifier())
			if _, ok := names[name]; !ok {
				continue
			}

			return fmt.Errorf(
				"%s declares a message %s, and %s declares a past, which is payday's %s written "+
					"into this app beside it: two messages of one name in one Go package.\n\n"+
					"An app's past is kept in payday's entities and no others. Take this one out, "+
					"and add what it has that they do not with an overlay at %s",
				l.rel(p), name, l.Past.At, name,
				filepath.ToSlash(filepath.Join(DirExt, "payday", strings.ToLower(name)+".ext.proto")))
		}

		return nil
	})
}
