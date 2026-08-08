package pdgen

import (
	"fmt"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/compiler/protogen"
)

const (
	pkgEntpage = protogen.GoImportPath("github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entpage")
	pkgEntsql  = protogen.GoImportPath("entgo.io/ent/dialect/sql")
	pkgTime    = protogen.GoImportPath("time")
	pkgUuid2   = protogen.GoImportPath("github.com/google/uuid")
)

// EmitSink writes the innermost server: the one the generated CRUD servers make,
// with a `List` on every entity that declared one.
//
// It has to be there rather than in a layer in front, and the reason is the same
// one that put the wall in the query. A list is the read the generated servers
// do not make, so nothing narrows it -- and a list cut short at a limit and
// filtered afterwards is one that any tenant can push another's rows out of by
// making enough of its own. So it is built with the same scope every other read
// carries, at the same depth.
//
// It is also what makes the stack compile at all: a `List` in the contract is a
// method on the service interface, and the generated CRUD server does not have
// one.
func EmitSink(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	lists := []*Entity{}
	for _, v := range s.Sorted() {
		if v.List != nil {
			lists = append(lists, v)
		}
	}
	if len(lists) == 0 {
		return
	}

	g.P("// Sink is the server the stack is built on: the generated CRUD servers,")
	g.P("// with a List on every entity that declared one.")
	g.P("//")
	g.P("// A list belongs here and not in a layer in front for the reason the wall")
	g.P("// does: it is the read the generated servers do not make, so nothing else")
	g.P("// narrows it -- and a list cut short at a limit and filtered afterwards is")
	g.P("// one that any tenant can push another's rows out of by making enough of")
	g.P("// its own.")
	g.P("//")
	g.P("//	sink, err := pd.NewSink(db, bare.WithMinter(pd.Minter()), bare.WithScope(pd.Wall()))")
	g.P("type Sink struct {")
	g.P("	", p.Bare.Ident("Server"))
	g.P("")
	g.P("	// w is what a Watch listens on, and is nil for a deployment that")
	g.P("	// serves none. It is here rather than on the generated Store because")
	g.P("	// that struct is the ORM generator's and knows nothing about payday.")
	g.P("	w *", pkgWatch.Ident("Watch"))
	g.P("}")
	g.P("")
	g.P("func NewSink(db *", p.Ent.Ident("Client"), ", opts ...", p.Bare.Ident("Option"), ") (Sink, error) {")
	g.P("	v, err := ", p.Bare.Ident("NewServer"), "(db, opts...)")
	g.P("	if err != nil {")
	g.P("		return Sink{}, err")
	g.P("	}")
	g.P("")
	g.P("	return Sink{Server: v}, nil")
	g.P("}")
	g.P("")
	g.P("// WithWatch answers with this server publishing to `w`, which is what a")
	g.P("// Watch reads from. A server built without one serves no Watch.")
	g.P("func (s Sink) WithWatch(w *", pkgWatch.Ident("Watch"), ") Sink {")
	g.P("	s.w = w")
	g.P("	return s")
	g.P("}")
	g.P("")
	g.P("var _ ", root.Ident("Server"), " = Sink{}")
	g.P("var _ ", pkgEnttx.Ident("Binder"), "[", root.Ident("Server"), "] = Sink{}")
	g.P("")
	g.P("// WithDriver answers with this server running on `drv`; see [Gate.WithDriver].")
	g.P("func (s Sink) WithDriver(drv ", pkgDialect.Ident("Driver"), ") (", root.Ident("Server"), ", error) {")
	g.P("	v, err := s.Server.WithDriver(drv)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return Sink{Server: v.(", p.Bare.Ident("Server"), "), w: s.w}, nil")
	g.P("}")
	g.P("")

	for _, v := range lists {
		emitList(g, v, s, p, root)
	}
}

func emitList(g *protogen.GeneratedFile, v *Entity, s *Schema, p Paths, root protogen.GoImportPath) {
	e := v.GoName()
	low := v.EntPkg()
	entPkg := p.Ent + "/" + protogen.GoImportPath(low)
	l := v.List

	g.P("type sink", e, " struct {")
	g.P("	", root.Ident(e+"ServiceServer"))
	g.P("	store ", p.Bare.Ident("Store"))
	g.P("	w     *", pkgWatch.Ident("Watch"))
	g.P("}")
	g.P("")
	g.P("func (s Sink) ", e, "() ", root.Ident(e+"ServiceServer"), " {")
	g.P("	return sink", e, "{s.Server.", e, "(), s.Server.Store, s.w}")
	g.P("}")
	g.P("")

	// The order, as entpage reads it and as ent orders by. Written out once so
	// the two cannot disagree -- which is a bug that shows as a page repeating
	// a row rather than as anything failing.
	g.P("// order", e, " is how ", e, "s come back.")
	g.P("//")
	g.P("// The last column is the key, and it is not decoration: a cursor cannot")
	g.P("// tell apart two rows equal in every column of the order, so the page after")
	g.P("// the first of them either repeats the second or skips it. Rows written by")
	g.P("// one request are stamped a moment apart at best.")
	g.P("var order", e, " = []", pkgEntpage.Ident("Order"), "{")
	for _, o := range l.Order {
		g.P("	{Column: ", entPkg.Ident("Field"+pascal(o.Field)), ", Desc: ", o.Desc, "},")
	}
	g.P("}")
	g.P("")

	g.P("const (")
	g.P("	// ", e, "PageSize is what a request that did not say gets, and")
	g.P("	// ", e, "PageLimit is the most it gets however loudly it asks.")
	g.P("	", e, "PageSize  = ", l.Size)
	g.P("	", e, "PageLimit = ", l.Max)
	g.P("")
	g.P("	// ", e, "FilterLimit is how many filters one request may carry. Each is a")
	g.P("	// predicate in the same query, so it is what says how much of the")
	g.P("	// database a request may ask to read -- and it is refused rather than")
	g.P("	// clamped, because dropping half the filters would answer a question")
	g.P("	// nobody asked.")
	g.P("	", e, "FilterLimit = ", l.Filters)
	g.P(")")
	g.P("")

	g.P("// List answers with the ", e, "s that match any of the given filters, or with")
	g.P("// every one there is if the request named none, a page at a time.")
	g.P("func (s sink", e, ") List(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"ListRequest"),
		") (*", root.Ident(e+"ListResponse"), ", error) {")
	g.P("	q := s.store.Db.", e, ".Query()")
	g.P("")

	// Through the same function the generated reads go through rather than by
	// asking the scope hook, because narrowing a read is the wall *and*
	// whatever else the generator put there -- a soft-erase predicate among
	// them -- and a list that reached past it for the hook alone would be the
	// one read that missed the rest.
	g.P("	// Through the same narrowing every generated read goes through, and not")
	g.P("	// by asking the scope alone: what narrows a read is the wall today and")
	g.P("	// the wall and something else tomorrow, and a list that reached past it")
	g.P("	// would be the one read that missed the something else.")
	g.P("	if p, err := ", p.Bare.Ident(e+"Narrow"), "(ctx, s.store.Scope, nil); err != nil {")
	g.P("		return nil, err")
	g.P("	} else if p != nil {")
	g.P("		q.Where(p)")
	g.P("	}")
	g.P("")

	if len(l.By) > 0 {
		g.P("	if fs := req.GetFilters(); len(fs) > 0 {")
		g.P("		if len(fs) > ", e, "FilterLimit {")
		g.P("			return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ",")
		g.P("				\"filters: %d of them, and %d is the most one list carries\", len(fs), ", e, "FilterLimit)")
		g.P("		}")
		g.P("")
		g.P("		ps := make([]", (p.Ent + "/predicate").Ident(e), ", 0, len(fs))")
		g.P("		for i, f := range fs {")
		g.P("			p, err := filter", e, "(f)")
		g.P("			if err != nil {")
		g.P("				return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ", \"filters[%d]: %s\", i, err)")
		g.P("			}")
		g.P("")
		g.P("			ps = append(ps, p)")
		g.P("		}")
		g.P("")
		g.P("		q.Where(", entPkg.Ident("Or"), "(ps...))")
		g.P("	}")
		g.P("")
	}

	// The cursor.
	g.P("	if v := req.GetAfter(); v != \"\" {")
	g.P("		var (")
	for i, o := range l.Order {
		f, _ := field(v.Entity, o.Field)
		g.P("			at", i, " ", goTypeOf(g, f.Type()))
	}
	g.P("		)")
	pf(g, "		if err := %s(v", g.QualifiedGoIdent(pkgEntpage.Ident("Decode")))
	for i := range l.Order {
		pf(g, ", &at%d", i)
	}
	g.P("); err != nil {")
	g.P("			return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ", \"after: %s\", err)")
	g.P("		}")
	g.P("")
	pf(g, "		p, err := %s(order%s, []any{", g.QualifiedGoIdent(pkgEntpage.Ident("After")), e)
	for i := range l.Order {
		if i > 0 {
			pf(g, ", ")
		}
		pf(g, "at%d", i)
	}
	g.P("})")
	g.P("		if err != nil {")
	g.P("			return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ", \"after: %s\", err)")
	g.P("		}")
	g.P("")
	g.P("		q.Where(p)")
	g.P("	}")
	g.P("")

	for _, w := range l.With {
		g.P("	q.With", pascal(w), "()")
	}
	if len(l.With) > 0 {
		g.P("")
	}

	g.P("	// One row more than the page, which is how \"is there another\" is answered")
	g.P("	// without a second query and without a count. The extra is dropped before")
	g.P("	// the answer is built; it was only ever asked for to see whether it was")
	g.P("	// there -- so a full last page answers with no cursor rather than sending")
	g.P("	// the caller back for an empty one.")
	g.P("	size := ", pkgEntpage.Ident("Size"), "(int(req.GetSize()), ", e, "PageSize, ", e, "PageLimit)")
	pf(g, "	us, err := q.Order(")
	for i, o := range l.Order {
		if i > 0 {
			pf(g, ", ")
		}
		if o.Desc {
			pf(g, "%s(%s())", g.QualifiedGoIdent(entPkg.Ident("By"+pascal(o.Field))),
				g.QualifiedGoIdent(pkgEntsql.Ident("OrderDesc")))
			continue
		}
		pf(g, "%s()", g.QualifiedGoIdent(entPkg.Ident("By"+pascal(o.Field))))
	}
	g.P(").Limit(size + 1).All(ctx)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	more := len(us) > size")
	g.P("	if more {")
	g.P("		us = us[:size]")
	g.P("	}")
	g.P("")
	g.P("	items := make([]*", root.Ident(e), ", len(us))")
	g.P("	for i, u := range us {")
	g.P("		items[i] = u.Proto()")
	g.P("	}")
	g.P("")
	g.P("	res := ", root.Ident(e+"ListResponse_builder"), "{Items: items}.Build()")
	g.P("	if more {")
	g.P("		last := us[len(us)-1]")
	pf(g, "		next, err := %s(", g.QualifiedGoIdent(pkgEntpage.Ident("Encode")))
	for i, o := range l.Order {
		if i > 0 {
			pf(g, ", ")
		}
		pf(g, "last.%s", pascal(o.Field))
	}
	g.P(")")
	g.P("		if err != nil {")
	g.P("			return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("Internal"), ", \"next: %s\", err)")
	g.P("		}")
	g.P("")
	g.P("		res.SetNext(next)")
	g.P("	}")
	g.P("")
	g.P("	return res, nil")
	g.P("}")
	g.P("")

	if len(l.By) > 0 {
		emitFilter(g, v, p, root)
	}
	if v.Watch {
		emitWatch(g, v, p, root)
	}
}

// emitFilter writes what one filter selects.
//
// A filter that names nothing is refused rather than taken to mean everything:
// the request said "these ones" and did not say which, which is a caller that
// built the request wrong.
func emitFilter(g *protogen.GeneratedFile, v *Entity, p Paths, root protogen.GoImportPath) {
	e := v.GoName()
	entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())

	g.P("// filter", e, " turns one filter into the predicate that selects what it")
	g.P("// names. Naming nothing is refused, since the request asked for \"these\" and")
	g.P("// did not say which.")
	g.P("func filter", e, "(f *", root.Ident(e+"Filter"), ") (", (p.Ent + "/predicate").Ident(e), ", error) {")
	g.P("	ps := make([]", (p.Ent + "/predicate").Ident(e), ", 0, 1)")
	for _, by := range v.List.By {
		if by.Ref {
			g.P("	if f.HasRef() {")
			g.P("		p, err := ", p.Bare.Ident(e+"Pick"), "(f.GetRef())")
			g.P("		if err != nil {")
			g.P("			return nil, err")
			g.P("		}")
			g.P("")
			g.P("		ps = append(ps, p)")
			g.P("	}")
			continue
		}

		name := pascal(by.Field)
		g.P("	if f.Has", name, "() {")
		switch by.Type {
		case ormpb.Type_TYPE_UUID:
			g.P("		k, err := ", pkgUuid2.Ident("FromBytes"), "(f.Get", name, "())")
			g.P("		if err != nil {")
			g.P("			return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ", \"", by.Field, ": %s\", err)")
			g.P("		}")
			g.P("")
			g.P("		ps = append(ps, ", entPkg.Ident(name+"EQ"), "(k))")
		default:
			g.P("		ps = append(ps, ", entPkg.Ident(name+"EQ"), "(f.Get", name, "()))")
		}
		g.P("	}")
	}
	g.P("	if len(ps) == 0 {")
	g.P("		return nil, ", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("InvalidArgument"), ", \"a filter that names nothing\")")
	g.P("	}")
	g.P("")
	g.P("	return ", entPkg.Ident("And"), "(ps...), nil")
	g.P("}")
	g.P("")
}

// goTypeOf is what a cursor column is decoded into.
func goTypeOf(g *protogen.GeneratedFile, t ormpb.Type) string {
	switch t {
	case ormpb.Type_TYPE_UUID:
		return g.QualifiedGoIdent(pkgUuid2.Ident("UUID"))
	case ormpb.Type_TYPE_TIME:
		return g.QualifiedGoIdent(pkgTime.Ident("Time"))
	case ormpb.Type_TYPE_STRING:
		return "string"
	}

	return "any"
}

// pf writes a fragment without a newline, which is how a call is built up
// across a loop. [protogen.GeneratedFile] is an io.Writer and P always ends a
// line, so there is nothing else to reach for.
func pf(g *protogen.GeneratedFile, format string, vs ...any) {
	ws := make([]any, len(vs))
	for i, v := range vs {
		if id, ok := v.(protogen.GoIdent); ok {
			ws[i] = g.QualifiedGoIdent(id)
			continue
		}
		ws[i] = v
	}

	fmt.Fprintf(g, format, ws...)
}

var _ = strings.Join
