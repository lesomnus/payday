package pdcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Gen is one run of the generator.
type Gen struct {
	Layout Layout

	// Log is where the steps are said, and may be nil for a silent run.
	Log io.Writer

	// Out is where the generated Go lands, and is [Layout.Root] for a real
	// generation. `--check` points it at a directory nobody is looking at, and
	// that is the whole of how the check differs from the thing it checks --
	// there is no second implementation to drift.
	Out string
}

// Run generates everything an app has, in the order the pieces depend on one
// another.
//
// The order is the part worth reading and the reason this is not four commands
// in a README. A `List` is an RPC: it has to be in the service contract before
// the messages, the stubs, the ent schema and the servers are generated from
// that contract. So the declarations are read twice -- once to write the
// contract, once to write the code -- and a generation that ran the second pass
// without the first would produce a stack that compiles and has no List in it.
func (g Gen) Run(ctx context.Context) error {
	if g.Out == "" {
		g.Out = g.Layout.Root
	}

	// The two halves of a contract before they are merged: what the service
	// generator wrote, and what payday adds to it. Nothing but the merge reads
	// either, so they are temporary -- the same way the generated Go is staged.
	// They used to be `proto.svc/` and `proto.pd/` in the app, and a directory
	// nobody reads is one somebody eventually edits.
	r := run{Gen: g}
	for _, v := range []*string{&r.svc, &r.pd} {
		d, err := os.MkdirTemp("", "pd-gen-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(d)

		*v = d
	}

	type step struct {
		what string
		do   func(context.Context) error
	}

	steps := []step{
		{"payday's entities, and what this app added to them", r.entities},
		{"the buf dependencies, if nothing has pinned them yet", r.deps},
		{"service contracts from the entities", r.contracts},
		{"merging what payday and the app add to them", r.merge},
	}

	// Only of an app that keeps a past, which is the one that has a lock: one
	// that does not has no Event to hold an event message, and `pdgen.Read`
	// refuses one in it. See [run.lock] for why here, between the merge and the
	// code.
	if g.Layout.Past != nil {
		steps = append(steps, step{"the event messages, held to the past's lock", r.lock})
	}

	steps = append(steps,
		step{"messages, servers, ent schema", r.code},
		step{"the ent runtime", r.entRuntime},
	)

	for _, step := range steps {
		g.say("pd: %s", step.what)
		if err := step.do(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.what, err)
		}
	}

	return nil
}

// run is one [Gen.Run], with the scratch space its steps hand to one another.
type run struct {
	Gen

	// svc and pd are the two halves of every contract, before they are merged.
	svc string
	pd  string
}

func (g Gen) say(format string, vs ...any) {
	if g.Log == nil {
		return
	}

	fmt.Fprintf(g.Log, format+"\n", vs...)
}

// goPackage is the line every copied entity has rewritten.
var goPackage = regexp.MustCompile(`(?m)^option go_package = .*$`)

// protoPackage is the `package` line of a copied entity, which is rewritten to
// the app's for the reason in [Layout.ProtoPkg]: a Tenant and a Holder are the
// app's own concepts, and a caller should not have to learn the name of the
// framework to say who they are.
var protoPackage = regexp.MustCompile(`(?m)^package .*$`)

// pdImport is one entity importing another, which moves with them. It does not
// match `payday.proto`, the options file, which does not move.
var pdImport = regexp.MustCompile(`(?m)^import "payday/([^"]+)";$`)

// entities copies payday's own entities into the app, merged with whatever the
// app added to them.
//
// They are copied rather than imported because everything generated from them
// has to be one set of types in one module: an ent schema in another package
// cannot have an edge to one here, and the wall is an edge. `go_package` is
// rewritten for the same reason.
//
// They keep their names -- `payday/tenant.proto` and not `.g.proto` -- because
// an app's own entity imports one by name, and a file a person writes should
// not have to spell a generated suffix. What says they are generated is the
// directory they are in, and the README written beside them.
//
// The past is copied the same way, into an app that declares one, and taken
// out of one that does not; see [Gen.past]. And Archived and Access are copied
// at the numbers an app gives them, when it gives them any (payday#163); see
// [Gen.numbering].
func (g Gen) entities(ctx context.Context) error {
	src, err := SchemaDir()
	if err != nil {
		return err
	}

	vs, err := filepath.Glob(filepath.Join(src, "*.proto"))
	if err != nil {
		return err
	}
	if len(vs) == 0 {
		return fmt.Errorf("%s: payday ships no entities here", src)
	}

	// Before anything is written, so that a refusal leaves the tree as it was.
	if err := checkPastNames(g.Layout); err != nil {
		return err
	}

	dst := filepath.Join(g.Out, g.Layout.DirPd())
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}

	for _, v := range vs {
		edit, err := g.numbering(filepath.Base(v))
		if err != nil {
			return err
		}
		if err := g.copyEntity(ctx, v, dst, edit); err != nil {
			return err
		}
	}
	if err := g.past(ctx, dst); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dst, "README.md"), []byte(copied), 0o644)
}

// copyEntity copies one of payday's entities into `dst`, merged with the app's
// overlay of it when there is one. `edit`, when it is given, is what is done to
// payday's file before the overlay meets it.
func (g Gen) copyEntity(ctx context.Context, v string, dst string, edit func([]byte) ([]byte, error)) error {
	name := filepath.Base(v)
	ext := g.Layout.Path(DirExt, "payday", strings.TrimSuffix(name, ".proto")+".ext.proto")

	b, err := os.ReadFile(v)
	if err != nil {
		return err
	}
	if edit != nil {
		if b, err = edit(b); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	if _, err := os.Stat(ext); err == nil {
		g.say("  merge %s + %s", name, filepath.Base(ext))

		// The same refusal the contract merge makes, because it is the
		// same union: a file-level `features.` here lands on the whole
		// copied entity, payday's own fields included. The line is even
		// easier to carry into an entity overlay -- the entity file it is
		// copied from starts with one.
		if err := CheckOverlayFile(ext); err != nil {
			return err
		}

		// protobuf-merge takes files, so one that was edited has to be one
		// again.
		at := v
		if edit != nil {
			if at, err = writeTemp(b, "*.proto"); err != nil {
				return err
			}
			defer os.Remove(at)
		}

		b, err = g.run(ctx, "go", "tool", "protobuf-merge", at, ext)
		if err != nil {
			return err
		}
	}

	b = goPackage.ReplaceAll(b, []byte(fmt.Sprintf("option go_package = %q;", g.Layout.GoPackage())))
	b = protoPackage.ReplaceAll(b, []byte(fmt.Sprintf("package %s;", g.Layout.ProtoPkg)))

	// And what they import of each other, since they have moved with them.
	// `payday.proto` is deliberately not matched: it is the options file and
	// stays where it is, which is what keeps `payday.entity` one name in
	// every app rather than one per app.
	b = pdImport.ReplaceAll(b, []byte(
		fmt.Sprintf(`import "%s/payday/$1";`, filepath.ToSlash(g.Layout.ProtoPkg))))

	return os.WriteFile(filepath.Join(dst, name), b, 0o644)
}

// past copies payday's `Event` and `Fact` into an app that declares a past,
// each numbered with the domain the app declared for it, and takes them out of
// one that does not (payday#151).
//
// **Every run**, as the rest of payday's entities are, so that what an app has
// is what the payday it generates with ships and what its overlays add -- and
// the overlays are merged on the same path theirs are, with the same refusals.
//
// The number is put in **before** the overlay is merged, into the one line of
// the template that is a placeholder: the template is payday's file, written to
// be numbered, and the merge is a reprinting that payday does not control the
// shape of.
//
// Taking them out is the other half, and it is not left to anything else. A
// copy left behind is a schema with an `Event` the app no longer declares,
// which `pdgen.Read` refuses -- correctly, and in a file this wrote.
func (g Gen) past(ctx context.Context, dst string) error {
	if g.Layout.Past == nil {
		for _, v := range pastFiles {
			if err := os.Remove(filepath.Join(dst, v.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}

		return nil
	}

	src, err := PastDir()
	if err != nil {
		return err
	}

	for _, v := range pastFiles {
		n := g.Layout.Past.Domain(v.Own)
		g.say("  past: %s, domain %d", v.Message, n)

		// The template's placeholder, `domain: 0`: no domain at all, and so
		// nothing a copy left unnumbered could pass for.
		if err := g.copyEntity(ctx, filepath.Join(src, v.Name), dst, func(b []byte) ([]byte, error) {
			return numbered(b, 0, n)
		}); err != nil {
			return err
		}
	}

	return nil
}

// copied is what this directory says about itself, and it is a file beside the
// entities rather than a comment at the top of each of them.
//
// A comment there would be a comment on the .proto file, and protoc hands those
// to every generator: it came out at the top of the generated Go, the generated
// TypeScript, and anything else anybody ever generates from this schema, each
// one telling its reader to go and edit an overlay that has nothing to do with
// the file they have open.
const copied = `# Generated

Everything in this directory is written by ` + "`pd gen`" + `, and editing it lasts
until the next one -- the past's lock apart, below.

- ` + "`*.proto`" + ` are payday's own entities, copied in whole. They are copied
  rather than imported because everything generated from them has to be one set
  of types in one Go package: an ent schema in another package cannot have an
  edge to one here, and the wall between tenants is an edge.
- ` + "`event.proto`" + ` and ` + "`fact.proto`" + ` are here when the app declares a past
  -- ` + "`option (payday.app) = {past: {event: 17, fact: 16}};`" + ` -- numbered as it
  declares, and go when the declaration does.
- ` + "`archived.proto`" + ` and ` + "`access.proto`" + ` are at domains 5 and 6, or at the
  numbers the app gives them when its own entities hold those --
  ` + "`option (payday.app) = {domains: {archived: 240, access: 241}};`" + `.
- ` + "`*_svc.g.proto`" + ` are the service contracts generated from them, the same
  as the ones beside your own entities.
- ` + "`" + LockFile + "`" + `, with a past, is the record of every event message the app
  has declared, and the one file here that is not written over: ` + "`pd gen`" + ` only
  adds to it, and refuses a schema that lost or changed what it holds, since
  a row of an event is read back by name for as long as it is kept. Commit
  it. Taking something out of it by hand is right only for a type no
  deployment ever wrote a row of.

To change what payday's entities hold, write an overlay in ` + "`" + DirExt + `/payday/` + "`" + `
-- one file per entity, named after it, and it is merged in here on the next
generation.
`

// contracts writes the service contracts and payday's additions to them, into
// the scratch directories the merge reads.
func (r run) contracts(ctx context.Context) error {
	// The previous run's contracts, which are inputs to this one if they are
	// left where buf can read them.
	//
	// **At every depth.** This was one directory down, which is where an app's
	// own contracts are and not where payday's are: those are beside the
	// copies, in `proto/<pkg>/payday/`. One of them left behind is read as it
	// was, importing whatever an overlay last made it import -- and an overlay
	// that gives the holder an edge to an app's entity makes it import that
	// entity's contract, which the line above had just removed. Every
	// generation after the first then failed on a file nobody could find.
	if err := filepath.WalkDir(filepath.Join(r.Out, DirProto), func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if p == filepath.Join(r.Out, DirExt) {
				// Overlays, which are the app's and never generated.
				return fs.SkipDir
			}

			return nil
		case !strings.HasSuffix(p, "_svc.g.proto"):
			return nil
		}

		return os.Remove(p)
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return r.buf(ctx, tmplContracts(r.Layout, r.svc, r.pd))
}

// merge folds each contract together with what payday adds and with whatever
// the app wrote by hand, in that order.
//
// The app is last on purpose: payday's half is generated from the declaration
// and can be rewritten at any time, and the app's half is the only one a person
// typed. An overlay that redeclares a number payday owns is refused before any
// of this, in `pd gen`'s reading of the schema -- protobuf-merge takes the
// overlay's word, so `alias` would quietly become whatever it said.
//
// The contract keeps the name it was generated under -- `_svc.g.proto` all the
// way through -- and that is not only cosmetic. It used to be renamed to
// `_svc.proto` on the way out, which meant a contract and an overlay referred
// to the same file by two names until that line ran, and the merge kept both
// imports. The first hand-written RPC in any app hit it. There is no rename
// now, so there is nothing to reconcile: what a contract imports is what is on
// disk, before the merge and after it.
func (r run) merge(ctx context.Context) error {
	// Which overlays were actually used, so that one nothing reached can be
	// named afterwards. See [run.unreached].
	used := map[string]bool{}

	if err := r.walk(ctx, used); err != nil {
		return err
	}

	return r.unreached(used)
}

// unreached refuses a service overlay that nothing merged.
//
// The walk above is over the **contracts**, and it looks up an overlay beside
// each one. So an overlay whose name matches no contract is never visited at
// all: it generates cleanly, merges nothing, and the first sign is a method
// that is not there. A typo in a filename is the likeliest cause, and it is
// the one mistake here with no symptom until much later.
//
// `doctor` already says this about **entity** overlays, in almost these words.
// This is the same sentence for the other kind, said where it costs nothing to
// hear it -- at generation, rather than when somebody remembers to run doctor.
func (r run) unreached(used map[string]bool) error {
	root := r.Layout.Path(DirExt)
	if _, err := os.Stat(root); err != nil {
		return nil
	}

	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, "_svc.ext.proto") {
			return err
		}
		if used[p] {
			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}

		want := strings.TrimSuffix(rel, "_svc.ext.proto") + "_svc.g.proto"

		return fmt.Errorf("%s extends a contract that does not exist, so it is never merged: "+
			"nothing generates %s", r.Layout.Rel(DirExt, rel), want)
	})
}

func (r run) walk(ctx context.Context, used map[string]bool) error {
	return filepath.WalkDir(r.svc, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, "_svc.g.proto") {
			return err
		}

		rel, err := filepath.Rel(r.svc, p)
		if err != nil {
			return err
		}

		stem := strings.TrimSuffix(rel, "_svc.g.proto")
		out := filepath.Join(r.Out, DirProto, rel)

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		// What the generator wrote, before anything is merged onto it: the
		// RPCs here are its own and nobody else's.
		if err := checkContractRpcs(b, rel); err != nil {
			return err
		}

		for _, overlay := range []string{
			filepath.Join(r.pd, stem+"_svc.pd.proto"),
			r.Layout.Path(DirExt, stem+"_svc.ext.proto"),
		} {
			if _, err := os.Stat(overlay); err != nil {
				continue
			}

			used[overlay] = true

			r.say("  merge %s + %s", rel, filepath.Base(overlay))

			if err := CheckOverlayFile(overlay); err != nil {
				return err
			}
			if err := checkOverlayRpcs(b, overlay); err != nil {
				return err
			}

			// protobuf-merge takes files, so what has been merged so far has to
			// be one.
			tmp, err := writeTemp(b, "*.proto")
			if err != nil {
				return err
			}

			b, err = r.run(ctx, "go", "tool", "protobuf-merge", tmp, overlay)
			os.Remove(tmp)
			if err != nil {
				return err
			}
		}

		// The one `go_package` this app has, stamped rather than trusted. The
		// service generator carries the option over from the entity and drops
		// the `;name` off it, so a contract said `.../pb` where the entity said
		// `.../pb;widgetpb` -- two names for one Go package, which protoc
		// refuses in a heap of messages naming files nobody wrote.
		//
		// It is the app's entities that say it and everything generated that
		// follows, which is the same rule the copies in `proto/<pkg>/payday/`
		// are held to.
		b = goPackage.ReplaceAll(b, []byte(fmt.Sprintf("option go_package = %q;", r.Layout.GoPackage())))

		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}

		return os.WriteFile(out, b, 0o644)
	})
}

// fileFeature is a `features.*` set at the top of a file, outside any message.
var fileFeature = regexp.MustCompile(`(?m)^option features\.[a-z_]+\s*=`)

// CheckOverlayFile refuses an overlay that changes the file's features.
//
// It is the one thing an overlay can say that is not about what it adds. The
// merge unions the options, so a `features.field_presence = IMPLICIT` copied
// from the entity file lands on the **whole contract** -- every field of every
// message in it, including the ones the generator wrote and the app has never
// read.
//
// What it costs is not obvious, which is why it is refused rather than
// documented. Here it happened to take `HasId` off an Add request and stop the
// build, which is the lucky version; on a field nothing calls `Has` on it would
// change only what "not set" means on the wire, and nothing would say so.
//
// An overlay adds messages and Rpcs. What the file is is the contract's, and
// the contract is generated.
func CheckOverlayFile(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}

	m := fileFeature.FindIndex(b)
	if m == nil {
		return nil
	}

	// Only what is outside a message: inside one it is about that message and
	// is the app's to say.
	if bytes.Contains(b[:m[0]], []byte("message ")) {
		return nil
	}

	return fmt.Errorf(
		"%s: sets a file-level `features.` option, and the merge would put it on the whole "+
			"contract -- every field of every message in it, including the ones the generator "+
			"wrote.\n\n"+
			"    %s\n\n"+
			"Delete the line. An overlay adds messages and Rpcs; what the file **is** belongs "+
			"to the contract, which is generated",
		filepath.Base(p), strings.TrimSpace(string(b[m[0]:m[1]]))+" ...")
}

// rpcName finds the rpcs a proto file declares.
var rpcName = regexp.MustCompile(`(?m)^\s*rpc\s+(\w+)\s*\(`)

// ormRpcs is every RPC of a contract `protoc-gen-orm-service` writes that
// payday knows what to put in front of.
var ormRpcs = map[string]bool{
	"Add":     true,
	"AddMany": true,
	"Get":     true,
	"Patch":   true,
	"Apply":   true,
	"Erase":   true,
}

// checkContractRpcs refuses a contract with an RPC that payday's layers were
// not written for.
//
// # Why nothing else does
//
// What payday puts in front of an entity's server is written RPC by RPC,
// because what it says depends on what the RPC does: the gate refuses a row
// put into a tenant the caller cannot see, the Sink names a row and stamps its
// tenant, the Secret layer clears what a row is never answered with, and the
// trail is refused a row written by hand. A layer overrides the RPCs it knows,
// and an RPC it does not know it inherits from the server behind -- the
// generated one, with none of the above in front of it.
//
// So an RPC the generator learns before payday does is served past every
// rule, it compiles, and every test of the RPCs payday does know goes on
// passing. AddMany was the first: written as an Add the gate overrides, its
// rule would have been asked of an Add and not of an AddMany, which writes the
// same rows.
//
// # What it compares against
//
// The contract as the generator wrote it, before payday's half and the app's
// are merged onto it -- so what is asked is about the generator alone, and an
// app's own RPCs, which are the domain layer's to serve, are not refused.
func checkContractRpcs(b []byte, contract string) error {
	for _, m := range rpcName.FindAllSubmatch(b, -1) {
		name := string(m[1])
		if ormRpcs[name] {
			continue
		}

		return fmt.Errorf(
			"%s: protoc-gen-orm-service wrote `rpc %s`, which this payday does not know.\n\n"+
				"What payday puts in front of an entity -- the gate, the Sink, the Secret layer, "+
				"the trail -- is written for the RPCs it knows, and one it does not would be "+
				"served with none of it: no row refused for a tenant the caller cannot see, "+
				"nothing named or stamped, no secret cleared. Nothing would say so.\n\n"+
				"Use the protoc-gen-orm-service this payday is released with -- the one its "+
				"go.mod names -- or a payday that knows %s",
			contract, name, name)
	}

	return nil
}

// checkOverlayRpcs refuses an overlay that redeclares an rpc already in the
// contract.
//
// # Why nothing else does
//
// `protobuf-merge` matches rpcs by name and emits the **overlay's** request,
// response and body, so a redeclaration wins silently -- and it wins in the
// direction that matters, since the generated one is the one with the wall, the
// trail and the narrowing behind it. Its `Strict` mode does not help: the
// conflict kinds it can report cover fields and editions, and there is none for
// an rpc at all.
//
// So an overlay that declares `rpc Get(...)` replaces the generated `Get`, the
// file compiles, the tests that call it pass, and what is gone is whatever the
// generator was putting behind that name. That is the "compiles perfectly and
// is wrong" shape, which is the one thing a generator has to refuse rather than
// document.
//
// # What it compares against
//
// The contract **as merged so far**, which is why this is here rather than in
// [CheckOverlayFile]: the app's overlay is merged after payday's own, so by
// this point `b` carries the crud rpcs and `List` and `Watch` both. An overlay
// redeclaring any of them is caught by one rule.
//
// An app that genuinely wants a different `Get` has an answer already -- write
// an rpc of its own with its own name, which is what an overlay is for.
func checkOverlayRpcs(b []byte, overlay string) error {
	v, err := os.ReadFile(overlay)
	if err != nil {
		return err
	}

	has := map[string]bool{}
	for _, m := range rpcName.FindAllSubmatch(b, -1) {
		has[string(m[1])] = true
	}

	for _, m := range rpcName.FindAllSubmatch(v, -1) {
		name := string(m[1])
		if !has[name] {
			continue
		}

		return fmt.Errorf(
			"%s: declares `rpc %s`, which the contract already has.\n\n"+
				"The merge would take the overlay's word for it -- the request, the response "+
				"and the body -- and the one it replaces is the one with the wall, the trail "+
				"and the narrowing behind it. Nothing would say so.\n\n"+
				"An overlay **adds**. If a different %s is wanted, give it its own name",
			filepath.Base(overlay), name, name)
	}

	return nil
}

// code writes the messages, the stubs, the query helpers, the ent schema, the
// servers, and what payday makes of the declarations.
func (g Gen) code(ctx context.Context) error {
	// Into a staging directory, because the plugins write a tree rooted at the
	// module path and what is wanted is that tree laid over the app.
	stage, err := os.MkdirTemp("", "pd-gen-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	if err := g.buf(ctx, tmplCode(g.Layout, stage)); err != nil {
		return err
	}

	// The generated files that are still there from last time and would not be
	// written again -- an entity removed from the schema leaves its server
	// behind, and a stack that still compiles against a row that is gone is the
	// kind of thing found at run time.
	for _, d := range []string{DirBare, DirPd_, filepath.Join(DirEnt, "schema")} {
		if err := os.RemoveAll(filepath.Join(g.Out, d)); err != nil {
			return err
		}
	}
	vs, _ := filepath.Glob(filepath.Join(g.Out, DirEnt, "*.g.go"))
	for _, v := range vs {
		if err := os.Remove(v); err != nil {
			return err
		}
	}

	// `module=` strips the prefix, so the staging tree is already rooted where
	// the app is: `robot.pb.go` and `internal/ent/schema/robot.go` rather than
	// the whole import path spelled out as directories.
	wrote, err := copyTree(stage, g.Out, unsuffix)
	if err != nil {
		return err
	}

	// And then the message package where it used to be. Not where it is: an app
	// that moved `go_package` from the module root to `api/` leaves a whole
	// second copy of every message behind, and it **compiles** -- two packages
	// of the same types, one of them stale, with nothing to say which the next
	// reader is looking at.
	return sweep(g.Out, wrote)
}

// sweep removes generated Go under `root` that this run did not write, `wrote`
// being what it did.
//
// It looks at `*.pb.go` and `*.g.go` outside the directories that were removed
// whole -- which is the message package and nothing else -- and only at files
// whose first line says a generator wrote them. So it is by what a file says
// about itself rather than by where it is, and a hand-written file would have
// to be named `.pb.go` **and** claim to be generated to be taken.
//
// What it is for is `go_package` moving. Everything else generated is in a
// directory payday owns and is deleted with it; the messages are the one thing
// that lands wherever the schema says, so the only way to know the old place is
// to look.
func sweep(root string, wrote map[string]bool) error {
	skip := map[string]bool{
		filepath.Join(root, DirProto): true,
		filepath.Join(root, DirTs):    true,
		filepath.Join(root, DirEnt):   true,
		filepath.Join(root, DirBare):  true,
		filepath.Join(root, DirPd_):   true,
	}

	var empty []string

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if skip[p] || strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" {
				return fs.SkipDir
			}

			return nil
		case !strings.HasSuffix(p, ".pb.go") && !strings.HasSuffix(p, ".g.go"):
			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if wrote[rel] {
			return nil
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(b, []byte("// Code generated by")) {
			return nil
		}

		empty = append(empty, filepath.Dir(p))

		return os.Remove(p)
	})
	if err != nil {
		return err
	}

	// And the directory it was, when it held nothing else. An empty `api/` is
	// harmless and is still somebody opening it to find out why it is there.
	// [os.Remove] on a directory with anything in it fails, which is the check.
	for _, d := range empty {
		if d != root {
			os.Remove(d)
		}
	}

	return nil
}

// unsuffix takes the `.g` back off, and it is the whole of what keeps that
// marker inside `proto/`.
//
// A contract is `robot_svc.g.proto`, and every generator downstream names its
// output after the file it read -- so protoc-gen-go writes `robot_svc.g.pb.go`
// and protoc-gen-go-grpc writes `robot_svc.g_grpc.pb.go`. The marker means
// "generated from something you wrote", which is true of the contract and true
// of every one of those as well: carried along it says nothing and is merely in
// the way, and in TypeScript it ends up in an import somebody types.
//
// Only contracts are `.g.proto`, and every contract ends in `_svc`, so this is
// exact rather than a guess at what a `.g` in a name might have meant.
func unsuffix(name string) string { return strings.Replace(name, "_svc.g", "_svc", 1) }

// entRuntime runs ent over the schema that was just written.
//
// It is the one step that is not payday's own: ent generates its client, its
// predicates and its builders from the schema structs, and everything above
// reads those. It runs in the app's directory because that is where the module
// ent is generating into begins.
func (g Gen) entRuntime(ctx context.Context) error {
	// The flags come first: ent's command line refuses one that follows an
	// argument.
	cmd := exec.CommandContext(ctx, "go", "tool", "ent", "generate",
		"--target", "./"+DirEnt,
		"--feature", "sql/modifier",
		"--feature", "sql/versioned-migration",

		// A create that reads back the row it stored, which the generated
		// servers ask for: the trail holds every write's row, and an Add's
		// INSERT answers with it, in the same statement, where the dialect
		// has RETURNING. They do not compile against a client without it.
		"--feature", "sql/returning",

		// The migration engine, off the generated `Client`.
		//
		// `Client.Schema` is a `*migrate.Schema` and `NewClient` builds one,
		// which is an import, and the linker follows imports: the client pulls
		// in Atlas -- its diff planner, all three of its SQL dialects, and the
		// HCL parser those import for a schema language nothing here writes.
		// Every binary that opens the database carried it, whether or not it
		// ever migrates. Measured on this app's sandbox, that was 10.5 MB of a
		// 54 MB module a browser downloads.
		//
		// What migrates says so instead, with the driver it already had:
		// `migrate.NewSchema(drv).Create(ctx)`, which is `cli.Migrate` in the
		// app -- and `cli` is the package a sandbox does not import.
		"--feature", "sql/no-client-schema",

		"./"+DirEnt+"/schema")
	cmd.Dir = g.Out

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ent generate: %w\n%s", err, stderr.String())
	}

	return nil
}

// deps pins the buf modules the schema imports, the first time.
//
// It is here, between payday's entities and the first `buf generate`, because
// that is the only moment it can be. An app's own schema names `Tenant`,
// so the workspace does not compile until [run.entities] has copied payday's
// entities in -- and `buf dep update` compiles the workspace before it writes
// the lock. Run it before a generation, as the obvious ordering suggests, and
// it fails on a tree that has never been generated: the first command a new app
// runs, refusing.
//
// So `pd gen` owns it, the way it owns the buf template. Only when nothing is
// pinned: a lock with entries in it is this app's pin and is not a thing a
// generation may quietly move, which also means `pd gen --check` in CI never
// reaches the network -- the lock is committed.
func (r run) deps(ctx context.Context) error {
	at := filepath.Join(r.Layout.Work, "buf.lock")

	b, err := os.ReadFile(at)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Contains(b, []byte("deps:")) {
		return nil
	}

	r.say("  buf dep update")

	name, err := Buf(ctx)
	if err != nil {
		return err
	}
	if _, err := r.Gen.run(ctx, name, "dep", "update"); err != nil {
		return err
	}

	return nil
}

// buf runs one template.
//
// The template is payday's and is written for each run rather than kept in the
// app, which is the whole reason `pd gen` exists as a command instead of a page
// of instructions. `strategy: all` is the example: the default runs a plugin
// once per directory, and an entity whose tenant is declared in another one is
// then not a generation target when its own file is read. An app maintaining
// its own template gets that wrong once and generates a wall with a hole in it.
func (g Gen) buf(ctx context.Context, template string, args ...string) error {
	p, err := writeTemp([]byte(template), "*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(p)

	name, err := Buf(ctx)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, name, append([]string{"generate", "--template", p}, args...)...)
	cmd.Dir = g.Layout.Work

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buf generate: %w\n%s%s", err, stderr.String(), whyStale(g.Layout.Work, stderr.Bytes()))
	}

	// What a plugin says of a generation that succeeds is a warning -- a list
	// that would scan its table for every page, say -- and this run is the one
	// place anybody would hear it. It was kept in this buffer and dropped with
	// it, which is how an app went on generating thirty of those without a
	// word (lesomnus/rove#122).
	if v := strings.TrimSpace(stderr.String()); v != "" {
		g.say("%s", v)
	}

	return nil
}

// run answers with what a command wrote to stdout, and with its stderr in the
// error when it fails -- which is where every generator says what was wrong.
func (g Gen) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = g.Layout.Work

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w\n%s", name, err, stderr.String())
	}

	return stdout.Bytes(), nil
}

func writeTemp(b []byte, pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := f.Write(b); err != nil {
		os.Remove(f.Name())
		return "", err
	}

	return f.Name(), nil
}

// copyTree lays one tree over another, with `name` deciding what each file is
// called on the way, and answers with what it wrote as paths under `dst`.
func copyTree(src string, dst string, name func(string) string) (map[string]bool, error) {
	wrote := map[string]bool{}

	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel = filepath.Join(filepath.Dir(rel), name(filepath.Base(rel)))
		}

		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}

		wrote[rel] = true

		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		return nil, err
	}

	return wrote, nil
}

func goList(args ...string) (string, error) {
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		return "", err
	}

	return string(out), nil
}

// Ts writes the TypeScript half: the messages and the service descriptors, as
// protobuf-es emits them.
//
// It is a step of its own and not part of [Gen.Run], because the two have
// different audiences. A backend-only app runs `pd gen`, has no node_modules,
// and must not have its generation fail over a plugin it was never going to
// use; an app with a page in front of it asks for this as well.
//
// What it does **not** generate is a client. protobuf-es v2 emits the service
// descriptors, and `@connectrpc/connect`'s `createClient` takes those directly
// -- so the client is a runtime function of a descriptor rather than a file per
// service, and there is nothing to keep in step. That is the same discipline
// §10.5 asks of the local store: generate the declaration, implement it once.
func (g Gen) Ts(ctx context.Context) error {
	if g.Out == "" {
		g.Out = g.Layout.Root
	}

	plugin, err := TsPlugin(g.Layout)
	if err != nil {
		return err
	}

	// Refused below a floor rather than written: what comes out is the
	// generator's, committed, and imported against the runtime -- so an older
	// one writes code the rest of the page was not built with, and it is
	// found out at somebody else's compile. The generator's version is the
	// plugin's own, the one about to run.
	if vs, err := Below(g.Layout, plugin); err != nil {
		return err
	} else if len(vs) > 0 {
		names := make([]string, 0, len(vs))
		for _, v := range vs {
			names = append(names, fmt.Sprintf("%s %s is older than %s", v.Name, v.From, v.Floor))
		}
		return fmt.Errorf("%w: %s; `go tool pd upgrade` moves them to what payday builds with",
			ErrBelowFloor, strings.Join(names, ", "))
	}

	g.say("pd: typescript, with %s", plugin)

	// Removed rather than written over: a message taken out of the schema
	// leaves a file behind, and TypeScript that still imports it compiles until
	// somebody calls it.
	if err := os.RemoveAll(filepath.Join(g.Out, DirTsGen)); err != nil {
		return err
	}

	// `--include-imports`, which the Go passes do not need and this one cannot
	// do without. A Go plugin resolves `orm.proto` to a package the app already
	// depends on; TypeScript has no such thing, so a file the schema imports and
	// this does not generate is an import of a module that is not there. The
	// well-known types are the exception and protobuf-es skips them itself --
	// it ships them.
	if err := g.buf(ctx, tmplTs(g.Layout, g.Out, plugin), "--include-imports"); err != nil {
		return err
	}

	// And [unsuffix] here as well, with the difference that a TypeScript file
	// names the ones it imports: `robot_svc.g_pb.ts` is a name somebody types in
	// `ts/src`, so the specifiers move with the files.
	return renameTs(filepath.Join(g.Out, DirTsGen))
}

// tsImport is the `.g` inside a module specifier, which is the same substring
// that is in the file name and is replaced with it.
var tsImport = regexp.MustCompile(`_svc\.g(_pb\.js")`)

func renameTs(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		b = tsImport.ReplaceAll(b, []byte("_svc${1}"))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}

		name := unsuffix(d.Name())
		if name == d.Name() {
			return nil
		}

		return os.Rename(p, filepath.Join(filepath.Dir(p), name))
	})
}

// ErrNoTsPlugin is a working copy with no protobuf-es to generate with.
var ErrNoTsPlugin = errors.New("protoc-gen-es is not installed")

// ErrBelowFloor is a page whose generator, or the runtime its code imports, is
// older than the oldest this payday builds with (see [Floors]).
var ErrBelowFloor = errors.New("the page's generator is older than payday builds with")

// TsPlugin finds the protobuf-es plugin.
//
// It looks in the app's own TypeScript package first and then in the
// workspace's, which is the order an npm workspace resolves in, and refuses
// rather than falling back to `npx`: `npx` with nothing installed downloads
// whatever is newest, so a generation would silently use a different version of
// the plugin from the one the app's lockfile pins -- and the way that is found
// out is generated code that changed for no reason in somebody else's checkout.
func TsPlugin(l Layout) (string, error) {
	for _, d := range []string{l.Path(DirTs), l.Work, filepath.Join(l.Work, DirTs)} {
		p := filepath.Join(d, "node_modules", ".bin", "protoc-gen-es")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("%w: npm install --save-dev @bufbuild/protoc-gen-es, in %s",
		ErrNoTsPlugin, l.Rel(DirTs))
}
