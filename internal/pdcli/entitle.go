package pdcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

// Entitlements is what an app declared of its copy of its tenants'
// entitlements (payday#185): that payday's `Entitlement` is among its
// entities, and the domain it takes.
//
//	option (payday.app) = {entitlements: {domain: 43}};
//
// Read off the schema before anything is compiled, for the reason [Past] is:
// it decides what `pd gen` writes for buf to compile.
type Entitlements struct {
	Domain uint32

	// At is the file that declared it, as a person would type it.
	At string
}

// entitleFiles is the copy as it ships: one file, one table.
var entitleFiles = []pastFile{
	{"entitlement.proto", "Entitlement", pdpb.Own_OWN_ENTITLEMENT},
}

// entitleNames are the names the copy's file declares at its top, which land
// in the app's Go package beside its own: the row, the enum of its value, and
// the service its contract is generated as.
var entitleNames = []string{"Entitlement", "EntitlementValue", "EntitlementService"}

// EntitleDir is where payday's copy of entitlements is, beside [SchemaDir], in
// the same module.
func EntitleDir() (string, error) {
	d, err := SchemaDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(filepath.Dir(d), schema.DirEntitle), nil
}

// readEntitlements is what this app's own files declare of the copy, and nil
// when none does. Held to the rule `pdgen.Read` holds the compiled schema to,
// `pdgen.CheckEntitlements`, with what the same layout declares of the past,
// the notifications and payday's two numbered entities -- read first -- since
// it may take none of their numbers.
func readEntitlements(l Layout) (*Entitlements, error) {
	held := pdpb.App_builder{}
	if l.Past != nil {
		held.Past = pdpb.Past_builder{Event: l.Past.Event, Fact: l.Past.Fact}.Build()
	}
	held.Notify = l.Notify.proto()
	if l.Domains != nil {
		held.Domains = pdpb.Domains_builder{Archived: l.Domains.Archived, Access: l.Domains.Access}.Build()
	}
	app := held.Build()

	var v *Entitlements
	err := l.eachOwn(func(p string, b []byte) error {
		if !protoAppAt.Match(b) {
			return nil
		}

		a, err := appOf(l.rel(p), b)
		if err != nil {
			return err
		}
		if !a.HasEntitlements() {
			return nil
		}
		if v != nil {
			return fmt.Errorf(
				"%s and %s both declare entitlements; they are said once, in the file that says the package is the app's",
				v.At, l.rel(p))
		}

		e := a.GetEntitlements()
		if err := pdgen.CheckEntitlements(e, app); err != nil {
			return fmt.Errorf("%s: %w", l.rel(p), err)
		}

		v = &Entitlements{Domain: e.GetDomain(), At: l.rel(p)}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return v, nil
}

// entitlements copies payday's `Entitlement` into an app that declares
// entitlements, numbered with the domain it declared, and takes it out of one
// that does not -- every run, as the past's two are; see [Gen.past].
func (g Gen) entitlements(ctx context.Context, dst string) error {
	if g.Layout.Entitlements == nil {
		for _, v := range entitleFiles {
			if err := os.Remove(filepath.Join(dst, v.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}

		return nil
	}

	src, err := EntitleDir()
	if err != nil {
		return err
	}

	for _, v := range entitleFiles {
		n := g.Layout.Entitlements.Domain
		g.say("  entitlements: %s, domain %d", v.Message, n)

		if err := g.copyEntity(ctx, filepath.Join(src, v.Name), dst, func(b []byte) ([]byte, error) {
			return numbered(b, 0, n)
		}); err != nil {
			return err
		}
	}

	return nil
}

// checkEntitleNames refuses an app whose own schema holds what the copy would
// be written over, before buf is run on it: a file called `entitlement.proto`,
// which is generated as the same Go file the copy is, and a message, an enum
// or a service of a name the copy's file declares, which is a symbol twice in
// one Go package. Only of an app that declares entitlements: one that does
// not has no copy, and an `Entitlement` of its own is its own business.
func checkEntitleNames(l Layout) error {
	if l.Entitlements == nil {
		return nil
	}

	return l.eachOwn(func(p string, b []byte) error {
		base := filepath.Base(p)
		if slices.ContainsFunc(entitleFiles, func(v pastFile) bool { return v.Name == base }) {
			return fmt.Errorf(
				"%s is called what payday's copy of entitlements is, and %s declares entitlements: both would "+
					"be written as %s, since a file of generated Go is named after the .proto and every file of "+
					"an app lands in one Go package.\n\n"+
					"Call it something else -- the messages in it keep their names",
				l.rel(p), l.Entitlements.At, strings.TrimSuffix(base, ".proto")+".pb.go")
		}

		// A file that does not parse is buf's to say, with a line and a column.
		f, err := parser.Parse(p, bytes.NewReader(b), reporter.NewHandler(nil))
		if err != nil {
			return nil
		}
		for _, d := range f.Decls {
			var name string
			switch d := d.(type) {
			case *ast.MessageNode:
				name = string(d.Name.AsIdentifier())
			case *ast.EnumNode:
				name = string(d.Name.AsIdentifier())
			case *ast.ServiceNode:
				name = string(d.Name.AsIdentifier())
			}
			if !slices.Contains(entitleNames, name) {
				continue
			}

			return fmt.Errorf(
				"%s declares %s, and %s declares entitlements, which writes payday's %s into this app beside it: "+
					"two of one name in one Go package.\n\n"+
					"Take this one out; what the copy should hold beside payday's fields is an overlay at %s",
				l.rel(p), name, l.Entitlements.At, name,
				filepath.ToSlash(filepath.Join(DirExt, "payday", "entitlement.ext.proto")))
		}

		return nil
	})
}
