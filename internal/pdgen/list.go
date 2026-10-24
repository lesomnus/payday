package pdgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/compiler/protogen"
)

const (
	pkgEntpage = protogen.GoImportPath("github.com/protobuf-orm/ent/dialect/sql/sqlpage")
	pkgEntsql  = protogen.GoImportPath("github.com/protobuf-orm/ent/dialect/sql")
	pkgTime    = protogen.GoImportPath("time")
	pkgUuid2   = protogen.GoImportPath("uuid")
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
	// Every entity the Sink has something to say about: a list to answer, a
	// name to fold, a tenancy to stamp, or a write that makes what a
	// transaction was told about a row stale. They share one wrapper per
	// entity because two wrappers over one service would each have to know
	// about the other to pass the methods it did not override.
	wraps := []*Entity{}
	for _, v := range s.Sorted() {
		if v.List != nil || v.Alias || v.Stamp != "" || len(v.Agrees) > 0 || forgets(v) {
			wraps = append(wraps, v)
		}
	}
	if len(wraps) == 0 {
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
	g.P("")
	g.P("	// namer decides the alias of a row being added, and nil is")
	g.P("	// `slug.Names`: fold what was given, make one up when nothing was.")
	g.P("	// See [Sink.WithNamer].")
	g.P("	namer ", pkgSlug.Ident("Namer"))
	g.P("")
	g.P("	// joined says this server was put on somebody else's transaction, and")
	g.P("	// is set by [Sink.WithDriver] because that is the only way it happens.")
	g.P("	//")
	g.P("	// What it is read for is the retry in Add and AddMany. A constraint")
	g.P("	// violation ends the transaction it happened in -- PostgreSQL refuses")
	g.P("	// everything after one until it is rolled back -- so a second attempt")
	g.P("	// inside a transaction this server did not open would fail for a reason")
	g.P("	// that has nothing to do with names, and would take the caller's other")
	g.P("	// writes with it.")
	g.P("	joined bool")
	g.P("}")
	g.P("")
	g.P("func NewSink(db *", p.Ent.Ident("Client"), ", opts ...", p.Bare.Ident("Option"), ") (Sink, error) {")
	g.P("	// Before anything is built on it. What this refuses is a binary whose")
	g.P("	// payday is not the one that wrote this file, which is a thing that")
	g.P("	// compiles and serves and is wrong; see [Check].")
	g.P("	if err := Check(); err != nil {")
	g.P("		return Sink{}, err")
	g.P("	}")
	g.P("")
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

	g.P("// WithNamer answers with this server deciding aliases with `n`.")
	g.P("//")
	g.P("// It is the fourth of the hooks the generated servers converge on --")
	g.P("// `bare.Minter` decides the key, `bare.Recorder` is told about the write,")
	g.P("// `bare.Scope` narrows the read, and this decides the name. It is payday's")
	g.P("// rather than the ORM generator's because an alias is payday's idea: it is")
	g.P("// what a slug resolves against, and nothing in `orm` has a word for it.")
	g.P("//")
	g.P("// Unset is `slug.Names`: fold what was given, and make a name up when")
	g.P("// nothing was. An app whose rows have to be named says so here, where a")
	g.P("// reader finds it:")
	g.P("//")
	g.P("//	sink = sink.WithNamer(slug.RequiredFor(nil, \"payday.Tenant\", \"app.Project\"))")
	g.P("func (s Sink) WithNamer(n ", pkgSlug.Ident("Namer"), ") Sink {")
	g.P("	s.namer = n")
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
	g.P("	// Everything this Sink was told, carried across. A field left out here")
	g.P("	// is a rule the requests inside a transaction go around, and it fails")
	g.P("	// only there -- which is the hardest place to notice it.")
	g.P("	return Sink{Server: v.(", p.Bare.Ident("Server"), "), w: s.w, namer: s.namer, joined: true}, nil")
	g.P("}")
	g.P("")

	for _, v := range wraps {
		e := v.GoName()

		g.P("type sink", e, " struct {")
		g.P("	", root.Ident(e+"ServiceServer"))
		g.P("	store ", p.Bare.Ident("Store"))
		g.P("	w      *", pkgWatch.Ident("Watch"))
		g.P("	namer  ", pkgSlug.Ident("Namer"))
		g.P("	joined bool")
		g.P("}")
		g.P("")
		if admits(v) {
			ruled(g, p, root, "Sink", e, "s.Server."+e+"(), s.Server.Store, s.w, s.namer, s.joined")
			emitSinkAdmit(g, v, root)
		} else {
			g.P("func (s Sink) ", e, "() ", root.Ident(e+"ServiceServer"), " {")
			g.P("	return sink", e, "{s.Server.", e, "(), s.Server.Store, s.w, s.namer, s.joined}")
			g.P("}")
			g.P("")
		}

		if v.Alias {
			emitAlias(g, v, root)
		}
		if forgets(v) {
			emitForget(g, v, root)
		}
		if v.Stamp != "" || len(v.Agrees) > 0 {
			emitTenancy(g, v, s, p, root)
		}
		if v.List != nil {
			emitList(g, v, s, p, root)
		}
	}

	for _, v := range wraps {
		if v.List != nil {
			emitCursor(g)
			break
		}
	}
}

// emitCursor writes what every list's cursor is read and written with.
//
// A cursor is the values of the last row's order columns, and it is also of an
// order and of a way: carried on in another order, or the other way round, it
// names a place in a sequence it is not in, and the page it answers with is
// neither the next one nor an error. So a cursor says which it is of, and one
// of another is refused.
//
// Except a cursor of a list's own order read forwards, which says nothing
// beside its values, as every cursor did before orders could be named -- so
// that one a client is holding still carries on after the deployment moved to
// a version that names them.
func emitCursor(g *protogen.GeneratedFile) {
	g.P("// pdAfter reads a cursor into the values it carries, refusing one of another")
	g.P("// order, or of this one read the other way.")
	g.P("func pdAfter(v string, tag string, into ...any) error {")
	g.P("	if tag == \"\" {")
	g.P("		return ", pkgEntpage.Ident("Decode"), "(v, into...)")
	g.P("	}")
	g.P("")
	g.P("	var was string")
	g.P("	if err := ", pkgEntpage.Ident("Decode"), "(v, append([]any{&was}, into...)...); err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("	if was != tag {")
	g.P("		return ", pkgFmt.Ident("Errorf"), "(\"%w: it is of another order, or of this one read the other way\", ", pkgEntpage.Ident("ErrCursor"), ")")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")
	g.P("// pdNext writes a cursor of the values given, saying which order it is of.")
	g.P("func pdNext(tag string, vs ...any) (string, error) {")
	g.P("	if tag == \"\" {")
	g.P("		return ", pkgEntpage.Ident("Encode"), "(vs...)")
	g.P("	}")
	g.P("")
	g.P("	return ", pkgEntpage.Ident("Encode"), "(append([]any{tag}, vs...)...)")
	g.P("}")
	g.P("")
	g.P("// pdReverse is an order read the other way round, every column at once.")
	g.P("func pdReverse(by []", pkgEntpage.Ident("Order"), ") []", pkgEntpage.Ident("Order"), " {")
	g.P("	out := make([]", pkgEntpage.Ident("Order"), ", len(by))")
	g.P("	for i, o := range by {")
	g.P("		out[i] = ", pkgEntpage.Ident("Order"), "{Column: o.Column, Desc: !o.Desc}")
	g.P("	}")
	g.P("")
	g.P("	return out")
	g.P("}")
	g.P("")
}

// admits is whether the Sink says anything about a row being added: what it
// is named, or which tenant it is in.
func admits(v *Entity) bool { return v.Alias || v.Stamp != "" || len(v.Agrees) > 0 }

// emitSinkAdmit writes what the Sink says about a row an Add would write: its
// name ([emitAlias]) and then its tenancy ([emitTenancy]), as the rule
// [ruled] puts on the server behind.
//
// A rule, for the reason the gate's is one: an AddMany writes the rows an Add
// of each of its items would have, and each of them has to be named and placed
// as that Add would have named and placed it. Said as an Add, it was not said
// of them at all -- an AddMany inherited from the server behind wrote a name
// nobody folded into a row nobody stamped.
//
// And one rule for both, where there used to be an Add for each: an entity
// that had a name and a tenancy both wrote its Add once, for the name, and
// the tenancy it declared was never asked.
func emitSinkAdmit(g *protogen.GeneratedFile, v *Entity, root protogen.GoImportPath) {
	e := v.GoName()
	tenancy := v.Stamp != "" || len(v.Agrees) > 0

	// What it says, in the order it says it.
	row := "a row an Add of " + e + " writes"
	var tenant string
	switch {
	case v.Stamp != "" && len(v.Agrees) > 0:
		tenant = "stamps its tenant, refusing it when its other paths to a tenant do not agree"
	case v.Stamp != "":
		tenant = "stamps its tenant"
	case len(v.Agrees) > 0:
		tenant = "refuses it when its paths to a tenant do not agree"
	}
	switch {
	case v.Alias && tenant != "":
		comment(g, "admit decides the name of "+row+", refusing one that was given and cannot be one, and then "+tenant+".")
	case v.Alias:
		comment(g, "admit decides the name of "+row+", refusing one that was given and cannot be one.")
	default:
		comment(g, "admit is what is said of the tenancy of "+row+": it "+tenant+".")
	}
	g.P("//")
	g.P("// It is asked of an Add and of every item of an AddMany; see [Sink.", e, "].")
	if v.Alias {
		g.P("//")
		g.P("// The name goes through [Sink.WithNamer], which is unset in most")
		g.P("// deployments and then means: fold what was given, and make a name up")
		g.P("// when nothing was -- for the reason `bare.Minter` makes a key up. An app")
		g.P("// whose rows have to be named says so with `slug.Required`; see")
		g.P("// `slug.Names` for why that is the way round it is.")
	}
	g.P("func (s sink", e, ") admit(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"AddRequest"),
		") (*", root.Ident(e+"AddRequest"), ", error) {")
	g.P("	// Copied rather than written to: the request belongs to whoever called,")
	g.P("	// and for a call made in this process that is a message they may still")
	g.P("	// be holding -- a server that changed a caller's own field would be")
	g.P("	// changing a value they can read back.")
	g.P("	r := ", pkgProto.Ident("CloneOf"), "(req)")
	if v.Alias {
		g.P("")
		g.P("	v, err := ", pkgSlug.Ident("NameWith"), "(ctx, s.namer, \"", v.FullName(), "\", req.GetAlias(), req)")
		g.P("	if err != nil {")
		g.P("		return nil, ", pkgPderr.Ident("At"), "(\"alias\", err)")
		g.P("	}")
		g.P("	r.SetAlias(v)")
	}
	if tenancy {
		g.P("")
		g.P("	if err := s.tenancy(ctx, r); err != nil {")
		g.P("		return nil, err")
		g.P("	}")
	}
	g.P("")
	g.P("	return r, nil")
	g.P("}")
	g.P("")
}

// comment writes `text` as a comment wrapped to the width of the rest of the
// generated code, for a sentence put together out of parts.
func comment(g *protogen.GeneratedFile, text string) {
	line := "//"
	for _, w := range strings.Fields(text) {
		if line != "//" && len(line)+1+len(w) > 76 {
			g.P(line)
			line = "//"
		}
		line += " " + w
	}
	g.P(line)
}

// emitAlias writes the overrides that keep a name what the rules allow: Add
// and AddMany try again when a name made up was taken, and Patch folds one it
// was given.
//
// It is on the Sink rather than in a layer in front, and that is the whole
// point of where it sits: the Sink is what *both* stacks are built on, so the
// path a deployment does its own work through -- the one with no wall and no
// gate -- cannot write a name the served path would have refused. A layer would
// have left exactly that hole, and a hole that only opens on the admin path is
// one nothing notices until a row written by hand cannot be found by name.
//
// What it does is fold, and refuse a name that is not one. Folding is why:
// "  Acme " and "acme" are one tenant to a person and two rows to a unique
// index, and the only place the difference can be closed once is on the way in.
// Refusing is what keeps `@acme/arm-01` able to name anything at all -- a row
// called "Not An Alias!!" is reachable by identifier and by nothing a person
// writes. Both are [emitSinkAdmit]'s, which every try goes through.
//
// What it does with a row nobody named is `slug.Namer`'s, and by default it
// makes a name up; see there.
func emitAlias(g *protogen.GeneratedFile, v *Entity, root protogen.GoImportPath) {
	e := v.GoName()

	g.P("// Add tries again when the name it made up was taken.")
	g.P("//")
	g.P("// The name is [sink", e, ".admit]'s, and every try goes through it, so a")
	g.P("// name made up is made up again and a name given is the same each time.")
	g.P("func (s sink", e, ") Add(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"AddRequest"),
		") (*", root.Ident(e), ", error) {")

	g.P("	// How many names to try. More than one only when the caller named")
	g.P("	// nothing -- then the name is this server's and a collision is this")
	g.P("	// server's to resolve. A name the caller gave is theirs, and quietly")
	g.P("	// choosing a different one would write a row they did not ask for.")
	g.P("	//")
	g.P("	// And only when this Add opens its own transaction; see [Sink.joined].")
	g.P("	tries := 1")
	g.P("	if req.GetAlias() == \"\" && !s.joined {")
	g.P("		tries = ", pkgSlug.Ident("Tries"))
	g.P("	}")
	g.P("")
	g.P("	for try := 0; ; try++ {")
	g.P("		res, err := s.", e, "ServiceServer.Add(ctx, req)")
	g.P("		if err == nil || try+1 >= tries || ", pkgStatus.Ident("Code"), "(err) != ",
		pkgCodes.Ident("AlreadyExists"), " {")
	g.P("			// Whatever it was, said in the words it was said in. Rewriting")
	g.P("			// it into \"no free name\" would assert the one thing this cannot")
	g.P("			// know -- a duplicate key is the same code -- and would say it")
	g.P("			// loudest exactly when it is wrong.")
	g.P("			return res, err")
	g.P("		}")
	g.P("	}")
	g.P("}")
	g.P("")

	if v.Rpcs().HasAddMany() {
		g.P("// AddMany tries again, as Add does, when it made up the name of an item and")
		g.P("// the call was refused AlreadyExists -- the whole call, with every name it")
		g.P("// made up made up again, since a refusal of a statement does not say which")
		g.P("// of its rows it was about. A name an item was given is the same each time.")
		g.P("//")
		g.P("// Every name made up is a chance to collide, so a call of many is refused")
		g.P("// more often than an Add of one; `slug.Tries` says how much more.")
		g.P("func (s sink", e, ") AddMany(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"AddManyRequest"),
			") (*", root.Ident(e+"AddManyResponse"), ", error) {")
		g.P("	// More than one try only when some item was named by nobody, and only")
		g.P("	// in a transaction of its own; see Add.")
		g.P("	tries := 1")
		g.P("	if !s.joined {")
		g.P("		for _, v := range req.GetItems() {")
		g.P("			if v.GetAlias() == \"\" {")
		g.P("				tries = ", pkgSlug.Ident("Tries"))
		g.P("				break")
		g.P("			}")
		g.P("		}")
		g.P("	}")
		g.P("")
		g.P("	for try := 0; ; try++ {")
		g.P("		res, err := s.", e, "ServiceServer.AddMany(ctx, req)")
		g.P("		if err == nil || try+1 >= tries || ", pkgStatus.Ident("Code"), "(err) != ",
			pkgCodes.Ident("AlreadyExists"), " {")
		g.P("			return res, err")
		g.P("		}")
		g.P("	}")
		g.P("}")
		g.P("")
	}

	g.P("// Patch folds a name it was given, and says nothing about one it was not.")
	g.P("//")
	g.P("// The presence is the whole of the difference from [sink", e, ".Add]: a patch")
	g.P("// that does not mention the alias is not a patch setting it to the empty")
	g.P("// string, and refusing one would make every patch of any other field carry")
	g.P("// the name along.")
	g.P("//")
	g.P("// The namer is **not** asked here, and that is the second difference. It")
	g.P("// decides the name of a row being made; a patch that cleared the alias is")
	g.P("// a caller asking for something invalid, and answering that with an")
	g.P("// invented name would hand them a row they did not ask for.")
	if forgets(v) {
		g.P("//")
		g.P("// And it forgets what the transaction was told about the row; see")
		g.P("// [sink", e, ".Apply].")
	}
	g.P("func (s sink", e, ") Patch(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"PatchRequest"),
		") (*", root.Ident(e), ", error) {")
	if forgets(v) {
		g.P("	", pkgSeen.Ident("Forget"), "(s.store.Db.Driver(), ", strconv.Quote(e), ", req.GetRef())")
		g.P("")
	}
	g.P("	if !req.HasAlias() {")
	g.P("		return s.", e, "ServiceServer.Patch(ctx, req)")
	g.P("	}")
	g.P("")
	g.P("	v, err := ", pkgSlug.Ident("ParseAlias"), "(req.GetAlias())")
	g.P("	if err != nil {")
	g.P("		return nil, ", pkgPderr.Ident("At"), "(\"alias\", err)")
	g.P("	}")
	g.P("")
	g.P("	req = ", pkgProto.Ident("CloneOf"), "(req)")
	g.P("	req.SetAlias(v)")
	g.P("")
	g.P("	return s.", e, "ServiceServer.Patch(ctx, req)")
	g.P("}")
	g.P("")
}

// forgets is whether a write to a row of `v` makes what a transaction was told
// about that row stale: a row named by key, which is the only kind `payday/seen`
// remembers, with a Patch or an Erase to write it with.
func forgets(v *Entity) bool {
	r := v.Rpcs()
	return byKey(v) && (r.HasPatch() || r.HasErase())
}

// emitForget writes the Sink's Erase, and its Patch and Apply, of an entity
// whose rows a transaction remembers: each forgets what the transaction was
// told about the row it writes, before it writes it ([seen.Forget]).
//
// What a transaction remembers is about the row as it was: that a frame could
// see it through the wall, and the set beside it, and which tenant a walk from
// it arrives at. An Erase takes the row out of every read, and one that takes
// it for good lets an Add put another under its key, in another tenant; a
// Patch or an Apply may move it to a set the caller cannot see. So the next
// question about the row is asked again.
//
// On the Sink, because that is what both stacks are built on: a write the
// deployment makes through the server with no wall is a write to the row as
// much as one through the gate is.
func emitForget(g *protogen.GeneratedFile, v *Entity, root protogen.GoImportPath) {
	e := v.GoName()
	forget := func(ref string) {
		g.P("	", pkgSeen.Ident("Forget"), "(s.store.Db.Driver(), ", strconv.Quote(e), ", ", ref, ")")
		g.P("")
	}

	if v.Rpcs().HasErase() {
		g.P("// Erase forgets what the transaction was told about the row, before it is")
		g.P("// erased: whether a frame may see it, and where a walk from it arrives.")
		g.P("// Neither is true of a row that is gone, nor of another put under its")
		g.P("// key. See [seen.Forget].")
		g.P("func (s sink", e, ") Erase(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"Ref"),
			") (*", root.Ident(e+"EraseResponse"), ", error) {")
		forget("req")
		g.P("	return s.", e, "ServiceServer.Erase(ctx, req)")
		g.P("}")
		g.P("")
	}
	if !v.Rpcs().HasPatch() {
		return
	}

	g.P("// Apply forgets what the transaction was told about the row, before it is")
	g.P("// written: a document may move it to a set the caller cannot see. See")
	g.P("// [seen.Forget].")
	g.P("func (s sink", e, ") Apply(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"ApplyRequest"),
		") (*", root.Ident(e), ", error) {")
	forget("req.GetRef()")
	g.P("	return s.", e, "ServiceServer.Apply(ctx, req)")
	g.P("}")
	g.P("")

	// The Patch of an entity with an alias is the one that folds it, and
	// forgets there; see [emitAlias].
	if v.Alias {
		return
	}
	g.P("// Patch forgets what the transaction was told about the row, before it is")
	g.P("// written; see [sink", e, ".Apply].")
	g.P("func (s sink", e, ") Patch(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"PatchRequest"),
		") (*", root.Ident(e), ", error) {")
	forget("req.GetRef()")
	g.P("	return s.", e, "ServiceServer.Patch(ctx, req)")
	g.P("}")
	g.P("")
}

func emitList(g *protogen.GeneratedFile, v *Entity, s *Schema, p Paths, root protogen.GoImportPath) {
	e := v.GoName()
	entPkg := p.EntPkg(v)
	l := v.List

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
	for _, n := range l.Orders {
		g.P("// order", e, pascal(n.Name), " is the order a request names as ", strings.ToUpper(n.Name), ".")
		g.P("var order", e, pascal(n.Name), " = []", pkgEntpage.Ident("Order"), "{")
		for _, o := range n.Order {
			g.P("	{Column: ", entPkg.Ident("Field"+pascal(o.Field)), ", Desc: ", o.Desc, "},")
		}
		g.P("}")
		g.P("")
	}

	// What each order's cursor is read and written with: the columns' values,
	// typed as the columns are, and the order it is of.
	emitOrderCursor(g, v, "", l.Order, entPkg, p)
	for _, n := range l.Orders {
		emitOrderCursor(g, v, pascal(n.Name), n.Order, entPkg, p)
	}

	g.P("// orderBy", e, " is an order as ent orders a query by it.")
	g.P("func orderBy", e, "(by []", pkgEntpage.Ident("Order"), ") []", entPkg.Ident("OrderOption"), " {")
	g.P("	out := make([]", entPkg.Ident("OrderOption"), ", len(by))")
	g.P("	for i, o := range by {")
	g.P("		if o.Desc {")
	g.P("			out[i] = ", pkgEntsql.Ident("OrderByField"), "(o.Column, ", pkgEntsql.Ident("OrderDesc"), "()).ToFunc()")
	g.P("			continue")
	g.P("		}")
	g.P("")
	g.P("		out[i] = ", pkgEntsql.Ident("OrderByField"), "(o.Column).ToFunc()")
	g.P("	}")
	g.P("")
	g.P("	return out")
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

	// Which order, and which way. One the schema declared, always, and never
	// one a request makes up: an order is a walk of an index, and which
	// indexes there are is the schema's to know.
	g.P("	// Which order, and which way: one the schema declared, and never one a")
	g.P("	// request makes up, since an order is a walk of an index.")
	g.P("	by, after, next, tag := order", e, ", after", e, ", next", e, ", \"\"")
	if len(l.Orders) > 0 {
		prefix := orderPrefix(v)
		g.P("	switch req.GetOrder() {")
		g.P("	case ", root.Ident(e+"ListOrder_"+prefix+"UNSPECIFIED"), ":")
		for _, n := range l.Orders {
			g.P("	case ", root.Ident(e+"ListOrder_"+prefix+strings.ToUpper(n.Name)), ":")
			g.P("		by, after, next, tag = order", e, pascal(n.Name), ", after", e, pascal(n.Name), ", next", e, pascal(n.Name), ", ", strconv.Quote(n.Name))
		}
		g.P("	default:")
		g.P("		return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ", \"order: %v is not an order this list is read in\", req.GetOrder())")
		g.P("	}")
	}
	g.P("	if req.GetReverse() {")
	g.P("		// The same index, walked from the other end.")
	g.P("		by, tag = pdReverse(by), tag+\"/reverse\"")
	g.P("	}")
	g.P("")

	// The cursor.
	g.P("	if v := req.GetAfter(); v != \"\" {")
	g.P("		p, err := after(v, by, tag)")
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
	g.P("	us, err := q.Order(orderBy", e, "(by)...).Limit(size + 1).All(ctx)")
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
	g.P("		v, err := next(us[len(us)-1], tag)")
	g.P("		if err != nil {")
	g.P("			return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("Internal"), ", \"next: %s\", err)")
	g.P("		}")
	g.P("")
	g.P("		res.SetNext(v)")
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

// emitOrderCursor writes how a cursor of one order is read and written: the
// order's columns, typed as they are, in its order.
func emitOrderCursor(g *protogen.GeneratedFile, v *Entity, name string, by []Order, entPkg protogen.GoImportPath, p Paths) {
	e := v.GoName()

	g.P("func after", e, name, "(v string, by []", pkgEntpage.Ident("Order"), ", tag string) (func(*", pkgEntsql.Ident("Selector"), "), error) {")
	g.P("	var (")
	for i, o := range by {
		f, _ := field(v.Entity, o.Field)
		g.P("		at", i, " ", goTypeOf(g, f.Type()))
	}
	g.P("	)")
	pf(g, "	if err := pdAfter(v, tag")
	for i := range by {
		pf(g, ", &at%d", i)
	}
	g.P("); err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	pf(g, "	return %s(by, []any{", g.QualifiedGoIdent(pkgEntpage.Ident("After")))
	for i := range by {
		if i > 0 {
			pf(g, ", ")
		}
		pf(g, "at%d", i)
	}
	g.P("})")
	g.P("}")
	g.P("")

	g.P("func next", e, name, "(last *", (p.Ent).Ident(e), ", tag string) (string, error) {")
	pf(g, "	return pdNext(tag")
	for _, o := range by {
		pf(g, ", last.%s", pascal(o.Field))
	}
	g.P(")")
	g.P("}")
	g.P("")
}

// emitFilter writes what one filter selects.
//
// A filter that names nothing is refused rather than taken to mean everything:
// the request said "these ones" and did not say which, which is a caller that
// built the request wrong.
func emitFilter(g *protogen.GeneratedFile, v *Entity, p Paths, root protogen.GoImportPath) {
	e := v.GoName()
	entPkg := p.EntPkg(v)

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

		// Two names for one field, and they are not the same word. The
		// filter is a protobuf message, so its accessor is whatever
		// protoc-gen-go called it -- `GetObjectId`. The predicate is ent's,
		// and ent capitalises an initialism -- `ObjectIdEQ`. They agree on
		// every field that does not end in one, which is why this was one
		// variable until a filter on `object_id` was declared.
		if by.Edge != "" {
			at := camel(by.Edge)

			g.P("	if f.Has", at, "() {")
			g.P("		w := f.Get", at, "()")
			g.P("		if b := w.GetId(); len(b) > 0 {")
			g.P("			// The **foreign key column** on this row, which is what an")
			g.P("			// edge is. A subquery for a comparison against an indexed")
			g.P("			// column is work nobody asked for.")
			g.P("			k, err := ", pkgEntUuid.Ident("FromBytes"), "(b)")
			g.P("			if err != nil {")
			g.P("				return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ", \"", by.Edge, ": %s\", err)")
			g.P("			}")
			g.P("")
			g.P("			ps = append(ps, ", entPkg.Ident(pascal(by.Edge)+"IdEQ"), "(k))")
			g.P("		} else {")
			g.P("			// Named some other way -- an alias, a slug. Resolving it")
			g.P("			// would be a read, and a predicate is built without one, so")
			g.P("			// it becomes a condition on the target instead. One hop,")
			g.P("			// against whatever index that column has.")
			// The **short** name here and the full one in the proto. Every
			// entity of an app generates into one Go package however many
			// proto packages it declares, so the identifier is `ThingPick`
			// even when the message is `shared.Thing`.
			g.P("			q, err := ", p.Bare.Ident(goNameOf(by.Target)+"Pick"), "(w)")
			g.P("			if err != nil {")
			g.P("				return nil, err")
			g.P("			}")
			g.P("")
			g.P("			ps = append(ps, ", entPkg.Ident("Has"+pascal(by.Edge)+"With"), "(q))")
			g.P("		}")
			g.P("	}")

			continue
		}

		at, col := camel(by.Field), pascal(by.Field)

		if by.Map {
			emitMapFilter(g, v, by, at, p, entPkg)
			continue
		}

		g.P("	if f.Has", at, "() {")
		switch by.Type {
		case ormpb.Type_TYPE_UUID:
			g.P("		k, err := ", pkgEntUuid.Ident("FromBytes"), "(f.Get", at, "())")
			g.P("		if err != nil {")
			g.P("			return nil, ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("InvalidArgument"), ", \"", by.Field, ": %s\", err)")
			g.P("		}")
			g.P("")
			g.P("		ps = append(ps, ", entPkg.Ident(col+"EQ"), "(k))")
		default:
			g.P("		ps = append(ps, ", entPkg.Ident(col+"EQ"), "(f.Get", at, "()))")
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

// emitMapFilter writes the predicate for a `map<string, string>` filter: every
// pair named has to be present and equal.
//
// One predicate per pair rather than one containment test, because the three
// engines spell containment three ways and `sqljson` already spells the two
// halves of a pair for all of them. What is written is what it says: the key is
// there, and what is under it is this.
//
// # Why the key is asked about separately
//
// A missing key and an empty value are different rows, and a filter that could
// not tell them apart would widen instead of narrowing -- `draft=""` matching
// every row that has never heard of `draft`. `HasKey` is what says which of the
// two this is, and it is written even where the engine's own comparison would
// have excluded the row anyway: what it costs is a term, and what it buys is
// that the predicate reads as the question.
//
// # Why the keys are sorted
//
// So that the same filter is the same SQL. A map's iteration order is
// deliberately not stable, and an unstable statement is one a prepared-statement
// cache holds several copies of and a test cannot be written against.
func emitMapFilter(g *protogen.GeneratedFile, v *Entity, by By, at string, p Paths, entPkg protogen.GoImportPath) {
	e := v.GoName()
	col := pascal(by.Field)

	g.P("	if m := f.Get", at, "(); len(m) > 0 {")
	g.P("		ks := make([]string, 0, len(m))")
	g.P("		for k := range m {")
	g.P("			ks = append(ks, k)")
	g.P("		}")
	g.P("		", pkgSort.Ident("Strings"), "(ks)")
	g.P("")
	g.P("		for _, k := range ks {")
	g.P("			ps = append(ps, ", (p.Ent + "/predicate").Ident(e), "(func(s *", pkgEntSql.Ident("Selector"), ") {")
	g.P("				s.Where(", pkgEntSql.Ident("And"), "(")
	g.P("					", pkgSqlJson.Ident("HasKey"), "(", entPkg.Ident("Field"+col), ", ", pkgSqlJson.Ident("Path"), "(k)),")
	g.P("					", pkgSqlJson.Ident("ValueEQ"), "(", entPkg.Ident("Field"+col), ", m[k], ", pkgSqlJson.Ident("Path"), "(k)),")
	g.P("				))")
	g.P("			}))")
	g.P("		}")
	g.P("	}")
}

// cursorTypes is what a cursor reads each type of column it may carry back
// into: the Go type ent holds the column in, which is what [emitOrderCursor]
// writes the cursor from. Read into the type it was written from, JSON gives a
// value back exactly; read into `any`, a number is a float64, and an int64 past
// 2^53 comes back as a neighbour -- a cursor that lands short of the row it was
// taken from, and a page after it that answers that row again.
//
// The two that are not built in are written qualified, in [goTypeOf]. A type
// not here is not an order's; see [orderable].
var cursorTypes = map[ormpb.Type]string{
	ormpb.Type_TYPE_UUID: "",
	ormpb.Type_TYPE_TIME: "",

	ormpb.Type_TYPE_STRING: "string",
	ormpb.Type_TYPE_BOOL:   "bool",

	// An enum is the number of its value, and ent holds it as one.
	ormpb.Type_TYPE_ENUM: "int32",

	ormpb.Type_TYPE_INT32:    "int32",
	ormpb.Type_TYPE_SINT32:   "int32",
	ormpb.Type_TYPE_SFIXED32: "int32",
	ormpb.Type_TYPE_UINT32:   "uint32",
	ormpb.Type_TYPE_FIXED32:  "uint32",
	ormpb.Type_TYPE_INT64:    "int64",
	ormpb.Type_TYPE_SINT64:   "int64",
	ormpb.Type_TYPE_SFIXED64: "int64",
	ormpb.Type_TYPE_UINT64:   "uint64",
	ormpb.Type_TYPE_FIXED64:  "uint64",
	ormpb.Type_TYPE_FLOAT:    "float32",
	ormpb.Type_TYPE_DOUBLE:   "float64",
}

// goTypeOf is what a cursor column is decoded into; see [cursorTypes].
func goTypeOf(g *protogen.GeneratedFile, t ormpb.Type) string {
	switch t {
	case ormpb.Type_TYPE_UUID:
		return g.QualifiedGoIdent(pkgUuid2.Ident("UUID"))
	case ormpb.Type_TYPE_TIME:
		return g.QualifiedGoIdent(pkgTime.Ident("Time"))
	}

	v, ok := cursorTypes[t]
	if !ok {
		// [orderable] refused it when the schema was read.
		panic(fmt.Sprintf("a cursor of a %s column, which no order may have", t))
	}

	return v
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

// emitTenancy writes what keeps a `stamp` true and an `agrees` honest.
//
// It is on the Sink for the reason [emitAlias] is: the Sink is what **both**
// stacks are built on, so the path a deployment does its own work through --
// the one with no wall and no gate -- cannot write a row the served path would
// have refused. A layer would leave exactly that hole, and a stamp that is only
// filled on the served path is worse than none: the row lands with an empty
// column and the wall makes it invisible to everybody.
//
// Both happen before the write and neither is re-checked afterwards, which is
// what the immutability rules in [Schema.checkStamp] and [Schema.checkAgrees]
// buy: no step of any path here can be repointed, so what was true when the row
// was written stays true.
func emitTenancy(g *protogen.GeneratedFile, v *Entity, s *Schema, p Paths, root protogen.GoImportPath) {
	e := v.GoName()

	// `via` is always read: it is what a stamp is filled from and what an
	// `agrees` is compared against. One reader per path, so the two do not each
	// write their own.
	paths := [][]string{v.Via}
	paths = append(paths, v.Agrees...)

	for _, path := range paths {
		emitTenantAt(g, v, path, s, p, root)
	}

	g.P("// tenancy fills what the schema says this row's tenant is, and refuses a")
	g.P("// row whose other paths do not agree with it.")
	g.P("//")
	g.P("// The request is already this server's copy by the time it arrives, so it")
	g.P("// is written to rather than cloned again.")
	g.P("func (s sink", e, ") tenancy(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(e+"AddRequest"), ") error {")

	if v.Stamp != "" {
		g.P("	want, err := s.tenantAt", pathName(v.Via), "(ctx, req.Get", camel(v.Via[0]), "())")
		g.P("	if err != nil {")
		g.P("		return err")
		g.P("	}")
		g.P("")
		g.P("	// Stamped, whatever the caller said. It is not a field of the request")
		g.P("	// and a caller that reached this server another way does not get to")
		g.P("	// keep what they put there.")
		g.P("	req.Set", camel(v.Stamp), "(want)")
	} else {
		// Nothing to stamp, so the first `agrees` path is what the rest are
		// compared against -- and it is `via` itself.
		g.P("	want, err := s.tenantAt", pathName(v.Via), "(ctx, req.Get", camel(v.Via[0]), "())")
		g.P("	if err != nil {")
		g.P("		return err")
		g.P("	}")
	}

	for _, path := range v.Agrees {
		at := camel(path[0])
		g.P("")
		g.P("	if req.Has", at, "() {")
		g.P("		got, err := s.tenantAt", pathName(path), "(ctx, req.Get", at, "())")
		g.P("		if err != nil {")
		g.P("			return err")
		g.P("		}")
		g.P("		if string(got) != string(want) {")
		g.P("			// Both exist and both are visible to whoever is writing --")
		g.P("			// the gate already saw to that. What it cannot see is that")
		g.P("			// they are in different tenants, because it asks \"may I read")
		g.P("			// this\" and a caller who holds several tenants may read both.")
		g.P("			return ", pkgPderr.Ident("Invalidf"), "(\"", path[0], "\",")
		g.P("				\"it is in another tenant than ", strings.Join(v.Via, "."), "\")")
		g.P("		}")
		g.P("	}")
	}

	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")

}

// emitTenantAt writes the reader for one path: the tenant it arrives at, read
// from below the wall.
//
// Below, deliberately. Reading through the wall would answer NotFound for a row
// in another tenant, and a disagreement -- the one thing `agrees` is for --
// would arrive as "there is no such row" instead of as a disagreement.
//
// It is one query. ent traverses an edge in the statement rather than by
// fetching the row and asking again, so a path of any depth is a join and not a
// round trip per hop.
func emitTenantAt(g *protogen.GeneratedFile, v *Entity, path []string, s *Schema, p Paths, root protogen.GoImportPath) {
	e := v.GoName()

	first, ok := edge(v.Entity, path[0])
	if !ok {
		return
	}

	head := string(first.Target().FullName().Name())

	g.P("// tenantAt", pathName(path), " is the tenant \"", strings.Join(path, "."), "\" arrives at.")
	g.P("func (s sink", e, ") tenantAt", pathName(path), "(ctx ", pkgCtx.Ident("Context"),
		", ref *", root.Ident(head+"Ref"), ") ([]byte, error) {")
	g.P("	if ref == nil {")
	g.P("		return nil, ", pkgPderr.Ident("Invalidf"), "(\"", path[0],
		"\", \"required: it is what says which tenant this belongs to\")")
	g.P("	}")
	g.P("")

	// Once a transaction, when nothing past the first step can be repointed:
	// then the tenant this walk arrives at from a row is decided for good, and
	// the next write naming the same row asks what the last one was told. The
	// first step is not part of it -- it is the edge of the row being written,
	// and what is remembered starts at the row that edge names, so it is shared
	// by every edge onto that row: `Robot.tenant` and not `lead.tenant`.
	in := "	"
	walk, kept := rest(first.Target(), path[1:])
	if to, ok := s.of(first.Target().FullName()); kept && ok && byKey(to) {
		g.P("	// Asked once a transaction: no step past the first can be repointed, so")
		g.P("	// where this walk arrives from a row is decided until the row is")
		g.P("	// written. See [seen.Tenant].")
		g.P("	return ", pkgSeen.Ident("Tenant"), "(s.store.Db.Driver(), ", strconv.Quote(to.GoName()), ", ",
			strconv.Quote(walk), ", ref, func() ([]byte, error) {")
		in = "		"
	}

	g.P(in, "// The row the edge names, picked the way every other reference is.")
	g.P(in, "pick, err := ", p.Bare.Ident(head+"Pick"), "(ref)")
	g.P(in, "if err != nil {")
	g.P(in, "	return nil, ", pkgPderr.Ident("At"), "(\"", path[0], "\", err)")
	g.P(in, "}")
	g.P("")

	q := "s.store.Db." + head + ".Query().Where(pick)"
	for _, step := range path[1:] {
		q += ".Query" + camel(step) + "()"
	}

	g.P(in, "k, err := ", q, ".OnlyId(ctx)")
	g.P(in, "if err != nil {")
	g.P(in, "	// Not there, or more than one -- both are the reference being wrong,")
	g.P(in, "	// and both are the caller's to fix.")
	g.P(in, "	return nil, ", pkgPderr.Ident("Invalidf"), "(\"", path[0],
		"\", \"it does not name one row\")")
	g.P(in, "}")
	g.P("")
	g.P(in, "return k[:], nil")
	if in != "	" {
		g.P("	})")
	}
	g.P("}")
	g.P("")
}

// rest is the walk from `at` along `steps`, as `payday/seen` names it -- the
// edges it takes from the entity it starts at -- and whether every one of
// those edges is immutable.
func rest(at graph.Entity, steps []string) (string, bool) {
	walk := []string{}
	kept := true
	for _, step := range steps {
		e, ok := edge(at, step)
		if !ok {
			return "", false
		}
		if !e.IsImmutable() {
			kept = false
		}

		walk = append(walk, step)
		at = e.Target()
	}

	return strings.Join(walk, "."), kept
}

// pathName is a path as one Go identifier: "holder.tenant" is "HolderTenant".
func pathName(path []string) string {
	out := ""
	for _, v := range path {
		out += camel(v)
	}

	return out
}
