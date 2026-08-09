package pdgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"google.golang.org/protobuf/compiler/protogen"
)

// Paths is where the generated code has to point, which is the one thing this
// generator cannot work out for itself: the ent runtime and the generated
// servers are put wherever the schema generators were told to put them.
type Paths struct {
	// Ent is the import path of the ent runtime, e.g. "app/internal/ent".
	Ent protogen.GoImportPath

	// Bare is the import path of the generated servers, e.g. "app/server/bare".
	Bare protogen.GoImportPath
}

const (
	pkgPdid    = protogen.GoImportPath("github.com/lesomnus/payday/pdid")
	pkgFrame   = protogen.GoImportPath("github.com/lesomnus/payday/frame")
	pkgSlug    = protogen.GoImportPath("github.com/lesomnus/payday/slug")
	pkgPderr   = protogen.GoImportPath("github.com/lesomnus/payday/pderr")
	pkgCtx     = protogen.GoImportPath("context")
	pkgStatus  = protogen.GoImportPath("google.golang.org/grpc/status")
	pkgCodes   = protogen.GoImportPath("google.golang.org/grpc/codes")
	pkgUuid    = protogen.GoImportPath("github.com/google/uuid")
	pkgFmt     = protogen.GoImportPath("fmt")
	pkgDriver  = protogen.GoImportPath("database/sql/driver")
	pkgVersion = protogen.GoImportPath("github.com/lesomnus/payday/version")
)

// EmitPayday writes which payday this file came out of, and the check that
// refuses a binary linking a different one.
//
// It is the only artefact of a generation payday writes the bytes of and the
// runtime can read: everything else here is protoc-gen-go's, protobuf-orm's or
// ent's. So this one file carries the stamp for the whole app.
//
// The failure it closes has no other symptom. An app that upgraded its payday
// dependency and did not regenerate **compiles, links and serves** -- on the
// domain table, the minter and the wall that the older payday wrote, read by a
// runtime that believes they are its own.
func EmitPayday(g *protogen.GeneratedFile, v string) {
	g.P("// Payday is the payday `pd gen` was run from.")
	g.P("//")
	g.P("// It is \"(devel)\" for a generation from a checkout or a workspace, where")
	g.P("// there is no version to name -- see [Check], which reads that as silence")
	g.P("// rather than as a mismatch.")
	g.P("const Payday = ", strconv.Quote(v))
	g.P("")

	g.P("// Check refuses a binary whose payday is not the one this file came out of.")
	g.P("//")
	g.P("// [NewSink] calls it, so an app that builds its stack the ordinary way has")
	g.P("// nothing to do. An app that assembles a Sink by hand calls it itself.")
	g.P("//")
	g.P("// It says nothing when either side cannot be named: a development build, a")
	g.P("// `replace`, a workspace. Refusing those would be refusing every `go test`")
	g.P("// in every app that develops against a checkout of payday.")
	g.P("func Check() error { return ", pkgVersion.Ident("Same"), "(Payday) }")
	g.P("")
}

// EmitDomains writes the domain of each entity, twice: as a constant for code
// that names one, and as a registration so that `pdid` can answer what an
// identifier holds and what a slug means.
//
// Both come from the same declaration, which is the point of generating them.
// Written by hand they are three places -- the constant, the name, the
// registration -- and any two of them can drift apart without anything failing
// to compile.
func EmitDomains(g *protogen.GeneratedFile, s *Schema) {
	vs := s.Sorted()

	g.P("// The domain of each entity, as declared by `(payday.entity).domain`.")
	g.P("//")
	g.P("// A domain outlives the row it named: an identifier read out of an audit")
	g.P("// trail says what kind of thing it was long after the row is gone. So a")
	g.P("// number is chosen once and never given to something else.")
	g.P("const (")
	for _, v := range vs {
		g.P(v.GoName(), "Domain ", pkgPdid.Ident("Domain"), " = ", v.Domain, " // ", strconv.Quote(v.Name))
	}
	g.P(")")
	g.P("")

	g.P("func init() {")
	for _, v := range vs {
		g.P("	", pkgPdid.Ident("Register"), "(", strconv.Quote(string(v.FullName())), ", ",
			v.GoName(), "Domain, ", strconv.Quote(v.Name), ")")
	}
	g.P("}")
	g.P("")

	g.P("// Domains is the domain of each entity by the full name of its message,")
	g.P("// which is the name a [Minter] is asked about.")
	g.P("var Domains = map[string]", pkgPdid.Ident("Domain"), "{")
	for _, v := range vs {
		g.P("	", strconv.Quote(string(v.FullName())), ": ", v.GoName(), "Domain,")
	}
	g.P("}")
	g.P("")
}

// EmitMinter writes what stamps a new row with the domain of the entity it
// belongs to, and refuses one that arrived carrying the wrong kind.
//
// The refusing is the half that is easy to leave out and is the reason this is
// worth generating. A request may say what identifier it wants, so a Robot
// could be stored under an identifier whose domain says Holder -- and then
// everything that reads that domain back is reading the caller's word rather
// than the schema's.
func EmitMinter(g *protogen.GeneratedFile, s *Schema, p Paths) {
	g.P("// Minter answers with the [bare.Minter] that gives every new row an")
	g.P("// identifier of its entity's domain, and refuses one of another.")
	g.P("//")
	g.P("// It is asked by name because that is what the generated servers know")
	g.P("// themselves by; an entity nothing declared is refused rather than given")
	g.P("// an identifier that says nothing.")
	g.P("func Minter() ", p.Bare.Ident("Minter"), " {")
	g.P("	return ", p.Bare.Ident("MinterFunc"), "(func(")
	g.P("		_ ", pkgCtx.Ident("Context"), ", entity string, given ", pkgUuid.Ident("UUID"), ", ok bool,")
	g.P("	) (", pkgUuid.Ident("UUID"), ", error) {")
	g.P("		d, found := Domains[entity]")
	g.P("		if !found {")
	g.P("			return ", pkgUuid.Ident("Nil"), ", ", pkgPdid.Ident("NoDomain"), "(entity)")
	g.P("		}")
	g.P("")
	g.P("		return ", pkgPdid.Ident("Mint"), "(d, given, ok)")
	g.P("	})")
	g.P("}")
	g.P("")
}

// EmitWall writes the predicate that every read of a tenanted entity carries.
//
// One method per entity, all of them the same shape, and **all of them
// present**: there is no embedded default that quietly says "no opinion", so an
// entity added to the schema arrives with its wall already on it. That is the
// whole difference between this and the same thing written by hand, where the
// method that is missing is the one nobody wrote.
func EmitWall(g *protogen.GeneratedFile, s *Schema, p Paths) {
	pred := p.Ent + "/predicate"

	g.P("// Wall answers with the scope that puts every read behind the tenant the")
	g.P("// row belongs to.")
	g.P("//")
	g.P("// It is installed on the innermost server rather than applied in front,")
	g.P("// which reads backwards and is the point: narrowing what a caller may see")
	g.P("// is a predicate, and a predicate belongs in the query. Done from in front")
	g.P("// it is an override of Get, Patch, Apply and Erase, once per entity and")
	g.P("// once more for every entity added afterwards.")
	g.P("//")
	g.P("//	sink, err := bare.NewServer(db, bare.WithScope(pd.Wall()))")
	g.P("//")
	g.P("// Narrowing is not refusing. A row out of the wall is a row the query does")
	g.P("// not match, so it is NotFound -- that it exists is itself something not to")
	g.P("// say.")
	g.P("func Wall() ", p.Bare.Ident("Scope"), " { return wall{} }")
	g.P("")

	g.P("// wall is what [Wall] answers with. It writes out every entity, including")
	g.P("// the ones that are not behind the wall, so that an entity added to the")
	g.P("// schema cannot arrive without a decision having been made about it.")
	g.P("type wall struct{}")
	g.P("")
	g.P("var _ ", p.Bare.Ident("Scope"), " = wall{}")
	g.P("")

	for _, v := range s.Sorted() {
		g.P("// ", v.GoName(), "Scope: ", why(v, s))
		g.P("func (wall) ", v.GoName(), "Scope(ctx ", pkgCtx.Ident("Context"),
			") (", pred.Ident(v.GoName()), ", error) {")

		if v.IsGlobal {
			g.P("	return nil, nil")
			g.P("}")
			g.P("")
			continue
		}

		g.P("	vs, all, err := ", pkgFrame.Ident("Narrow"), "(ctx)")
		g.P("	if all || err != nil {")
		g.P("		return nil, err")
		g.P("	}")
		g.P("")
		g.P("	return ", predicateOf(g, v, s, p), ", nil")
		g.P("}")
		g.P("")
	}
}

// EmitGrouped writes the second axis, and writes nothing when the app declared
// none -- which is most apps.
//
// # Why this is generated rather than left to the app
//
// It is fifteen lines per entity written by hand, and the fifteen lines are not
// the problem. `bare.Unscoped` is: an app writes the entities it has something
// to say about and embeds it for the rest, so **an entity added later is not
// narrowed and nothing says so.** That is the same hole payday generates the
// tenant wall to close, in the same place, for the same reason -- and a second
// axis with a hole in it is worse than none, because the first one looks like
// it is doing the work.
//
// # What it does not decide
//
// Which sets the caller may see. That is a policy question -- one actor is in
// several sets in most schemas that have them at all -- so it arrives the way
// the tenant's does: worked out by whatever holds those rules and handed in.
// What is generated is the predicate per entity, which is the part that is
// mechanical and the part that goes stale.
//
// # One hop
//
// A row is in the set its own field 3 names, and an entity with no field 3 is
// not narrowed. There is no walk out along edges the way `via:` has, and that
// is a limit rather than an oversight: the tenant is reachable from everything
// by construction, and a set is not. An app whose child rows belong to a set
// puts the edge on them -- which is what "every resource table carries it" is
// already the advice for.
func EmitGrouped(g *protogen.GeneratedFile, s *Schema, p Paths) {
	if s.Set == nil {
		return
	}

	pred := p.Ent + "/predicate"
	set := s.Set.GoName()

	g.P("// Sets is which ", set, "s a caller may see.")
	g.P("//")
	g.P("// `all` is how \"every one of them\" is said, and it is a second return")
	g.P("// rather than a value in the first: an empty list has to mean none, or the")
	g.P("// safe answer and the open one are the same value and a bug that loses the")
	g.P("// list opens the door. This is [frame.Narrow]'s shape for the same reason.")
	g.P("type Sets func(ctx ", pkgCtx.Ident("Context"), ") (vs []", pkgUuid.Ident("UUID"), ", all bool, err error)")
	g.P("")

	g.P("// Grouped answers with the scope that narrows every read to the ", set, "s")
	g.P("// `of` says the caller may see.")
	g.P("//")
	g.P("// It goes beside the wall rather than instead of it, and both are applied:")
	g.P("//")
	g.P("//	bare.WithScope(bare.Scopes{pd.Wall(), pd.Grouped(of)})")
	g.P("//")
	g.P("// The two are not the same question. The wall is who holds the row; this is")
	g.P("// which set inside them it is in. An app that narrows only by the set is one")
	g.P("// where two tenants naming the same ", set, " see each other's rows.")
	g.P("func Grouped(of Sets) ", p.Bare.Ident("Scope"), " { return grouped{of} }")
	g.P("")

	g.P("// grouped is what [Grouped] answers with. It writes out every entity,")
	g.P("// including the ones that are in no set, so that an entity added to the")
	g.P("// schema cannot arrive without a decision having been made about it.")
	g.P("type grouped struct{ of Sets }")
	g.P("")
	g.P("var _ ", p.Bare.Ident("Scope"), " = grouped{}")
	g.P("")

	for _, v := range s.Sorted() {
		g.P("// ", v.GoName(), "Scope: ", whySet(v, s))
		g.P("func (x grouped) ", v.GoName(), "Scope(ctx ", pkgCtx.Ident("Context"),
			") (", pred.Ident(v.GoName()), ", error) {")

		if !v.IsSet && v.Set == "" {
			g.P("	return nil, nil")
			g.P("}")
			g.P("")
			continue
		}

		g.P("	if x.of == nil {")
		g.P("		return nil, nil")
		g.P("	}")
		g.P("")
		g.P("	vs, all, err := x.of(ctx)")
		g.P("	if all || err != nil {")
		g.P("		return nil, err")
		g.P("	}")
		g.P("")

		at := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())
		if v.IsSet {
			// From inside the set there is exactly one, the same way a tenant
			// is its own wall.
			g.P("	return ", at.Ident("IDIn"), "(vs...), nil")
		} else {
			// The foreign key rather than a traversal; see [collapse].
			g.P("	return ", at.Ident(pascal(v.Set)+"IDIn"), "(vs...), nil")
		}

		g.P("}")
		g.P("")
	}
}

// whySet is the one-line reason a generated scope narrows the way it does.
func whySet(v *Entity, s *Schema) string {
	switch {
	case v.IsSet:
		return "it is the set, so the sets a caller may see are the rows they may see."
	case v.Set != "":
		return fmt.Sprintf("in the %s its %q names.", s.Set.GoName(), v.Set)
	default:
		return "in no set -- it declared no field 3, so this narrows nothing."
	}
}

// predicateOf renders the wall of one entity: the tenant's own identity, or a
// walk out along the edges the schema named until it reaches one.
func predicateOf(g *protogen.GeneratedFile, v *Entity, s *Schema, p Paths) string {
	tenantIn := fmt.Sprintf("%s(vs...)",
		g.QualifiedGoIdent((p.Ent + "/" + protogen.GoImportPath(s.Tenant.EntPkg())).Ident("IDIn")))

	if v.IsTenant {
		// From inside a tenant there is exactly one, which is what a tenant
		// being a wall comes down to.
		return tenantIn
	}

	if len(v.Columns) > 0 {
		// A row that names its tenant without holding an edge to it. The
		// predicate is on this entity's own column, so there is nothing to
		// walk: `audit.TenantIDIn(vs...)`.
		//
		// Several columns is OR, and it is the trail: one write has a tenant
		// whose row changed and a tenant whose actor made it, and the record is
		// readable by both.
		at := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())

		vs := make([]string, len(v.Columns))
		for i, name := range v.Columns {
			vs[i] = fmt.Sprintf("%s(vs...)", g.QualifiedGoIdent(at.Ident(pascal(name)+"In")))
		}
		if len(vs) == 1 {
			return vs[0]
		}

		return fmt.Sprintf("%s(%s)", g.QualifiedGoIdent(at.Ident("Or")), strings.Join(vs, ", "))
	}

	// Built inside out: the innermost predicate is the tenant's, and each step
	// back along the path wraps it in the edge that reached it.
	//
	// The **last** step is not a traversal, and that is the whole of what
	// [collapse] is about. Walking it would ask the database, once per row,
	// whether the tenant it already holds the key of exists -- and the answer is
	// yes by construction, because the key is a foreign key. So the last hop
	// reads the column instead, and what was a correlated subquery on every read
	// of every tenanted entity becomes a comparison the planner can index.
	at := v.Entity
	var steps []string
	for i, name := range v.Via {
		e, _ := edge(at, name)
		if i == len(v.Via)-1 {
			return strings.Join(steps, "") + collapse(g, at, name, p) + strings.Repeat(")", len(steps))
		}

		steps = append(steps, fmt.Sprintf("%s(", g.QualifiedGoIdent(
			(p.Ent+"/"+protogen.GoImportPath(strings.ToLower(string(at.FullName().Name())))).
				Ident("Has"+pascal(name)+"With"))))
		at = e.Target()
	}

	return strings.Join(steps, "") + tenantIn + strings.Repeat(")", len(steps))
}

// collapse is the last hop of a `via`, read off the foreign key rather than
// walked.
//
// `HasTenantWith(tenant.IDIn(vs...))` and `tenant_id IN vs` answer the same
// question, and they answer it the same way **because the key is a foreign
// key**: a row cannot hold the identifier of a tenant that is not there.
// Without that guarantee the two differ, which is worth saying out loud -- the
// integrity constraint is not a cost paid for the join, it is what makes the
// join skippable.
//
// What it is worth is measurable rather than theoretical. On SQLite the walked
// form plans as a correlated subquery probing the tenant table once per row;
// this plans as a filter on the row's own column. Two hops go from four steps
// with a CORRELATED SCALAR SUBQUERY to two without one.
//
// It used to be written out by hand -- a `predicate.X` closure comparing
// `s.C(<Edge>Column)` -- with a startup check that the column really was on the
// table being read from, because a wall that narrows to the wrong rows says
// nothing. ent generates the predicate now: the edge is bound to a field, so
// `<Edge>IDIn` exists exactly when the key is on that table, and there is no
// assumption left to check.
func collapse(g *protogen.GeneratedFile, at graph.Entity, name string, p Paths) string {
	pkg := p.Ent + "/" + protogen.GoImportPath(strings.ToLower(string(at.FullName().Name())))

	return fmt.Sprintf("%s(vs...)", g.QualifiedGoIdent(pkg.Ident(pascal(name)+"IDIn")))
}

// why is the one-line reason a reader of the generated file gets, so that it
// says what the schema meant rather than only what it renders to.
func why(v *Entity, s *Schema) string {
	switch {
	case v.IsTenant:
		return "a tenant is inside itself, which is what a tenant being a wall comes down to."
	case v.IsGlobal:
		return "declared `global`, so it is not behind the wall at all."
	case len(v.Columns) == 1:
		return fmt.Sprintf("a row belongs to the tenant its %q names, which it holds without an edge.", v.Columns[0])
	case len(v.Columns) > 1:
		return fmt.Sprintf("a row is readable by every tenant it names -- %s -- which is the trail.",
			strings.Join(v.Columns, ", "))
	default:
		return fmt.Sprintf("a row belongs to the tenant its %q reaches.", strings.Join(v.Via, "."))
	}
}

// pascal is a name as ent spells it in Go.
//
// The initialisms are ent's own list and not a style preference: a predicate is
// looked up by the name ent generated, so `tenant_id` has to come out
// `TenantID` and not `TenantId` or nothing compiles.
func pascal(v string) string {
	var b strings.Builder
	for _, part := range strings.Split(v, "_") {
		if part == "" {
			continue
		}
		if up, ok := initialisms[part]; ok {
			b.WriteString(up)
			continue
		}

		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}

	return b.String()
}

// initialisms is what ent writes in capitals. It is short because the only
// names that reach here are the ones a schema wrote down, and it grows when
// something that should have been capitalized was not.
// camel is a proto field name as **protoc-gen-go** writes it in Go.
//
// It is [pascal] without the initialisms, and the difference is the whole
// reason it exists: ent writes `object_id` as `ObjectID` and protobuf-go writes
// it as `ObjectId`, so a generated line that reaches for a message accessor and
// one that reaches for a predicate cannot share a name.
func camel(v string) string {
	var b strings.Builder
	for _, part := range strings.Split(v, "_") {
		if part == "" {
			continue
		}

		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}

	return b.String()
}

var initialisms = map[string]string{
	"id":   "ID",
	"ids":  "IDs",
	"url":  "URL",
	"uri":  "URI",
	"uuid": "UUID",
	"ip":   "IP",
}
