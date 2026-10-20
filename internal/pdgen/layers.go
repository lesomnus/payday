package pdgen

import (
	"slices"
	"strconv"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdpb"
)

const (
	pkgGate    = protogen.GoImportPath("github.com/lesomnus/payday/gate")
	pkgAudit   = protogen.GoImportPath("github.com/lesomnus/payday/audit")
	pkgEnttx   = protogen.GoImportPath("github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx")
	pkgDialect = protogen.GoImportPath("github.com/protobuf-orm/ent/dialect")
	pkgEmpty   = protogen.GoImportPath("google.golang.org/protobuf/types/known/emptypb")
	pkgProto   = protogen.GoImportPath("google.golang.org/protobuf/proto")
	pkgPatchpb = protogen.GoImportPath("github.com/lesomnus/protobuf-patch/patchpb")
	pkgReflect = protogen.GoImportPath("google.golang.org/protobuf/reflect/protoreflect")
)

// Own is the entity payday ships under that marker, and nil for a schema that
// does not hold it.
//
// By the marker and never by the name. Reading `"payday.Tenant"` is what made
// the proto package payday's rather than the app's -- and it did so in the one
// way that is worst: renaming it did not fail, it made the layers built out of
// these entities not be generated, so reads stayed walled and writes stopped
// being. See [CheckOwn], which is what refuses an absence now.
func (s *Schema) Own(v pdpb.Own) *Entity {
	if v == pdpb.Own_OWN_UNSPECIFIED {
		// Everything an app writes says this, so answering with one of them
		// would be answering that any entity is payday's.
		return nil
	}

	for _, e := range s.Entities {
		if e.Own == v {
			return e
		}
	}

	return nil
}

// Has reports whether the schema holds the entity payday ships under that
// marker.
func (s *Schema) Has(v pdpb.Own) bool { return s.Own(v) != nil }

// EmitGate writes the layer that holds the rules a wall cannot: whether a
// tenant may be put up or taken down, and which tenant a holder may be added
// to.
//
// Both are about a row that does not exist yet, so there is nothing to narrow
// and no predicate to write -- which is why they are a layer at all while
// everything else about the wall is a query.
//
// The judgement is not here. It is in `payday/gate`, as functions, and this
// calls them; a reader looking for what a rule *is* goes there. What is
// generated is the wiring, because a layer is a `struct{ app.Overlay }` and
// `app.Server` is an interface generated per app.
func EmitGate(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	// A schema without them does not reach here from `pd gen`; [CheckOwn]
	// refuses it first, because what this writes is the whole of the `Add`
	// tenant check and losing it quietly leaves reads walled and writes not.
	if !s.Has(pdpb.Own_OWN_TENANT) || !s.Has(pdpb.Own_OWN_HOLDER) {
		return
	}

	g.P("// Gate is the layer that says what a caller may do with a request.")
	g.P("//")
	g.P("// It goes outermost, so nothing behind it has to ask again -- though most")
	g.P("// of what it stands for is not enforced here at all: the wall is a")
	g.P("// predicate and predicates belong in the query, so [Wall] is installed on")
	g.P("// the innermost server. What is left is the two rules that are about a")
	g.P("// row which does not exist yet.")
	g.P("//")
	g.P("//	s, err := app.Build(walled, core.Build(), pd.AuditBuild(), pd.GateBuild())")
	g.P("type Gate struct {")
	g.P("	", root.Ident("Overlay"))
	g.P("")
	// The layers behind are asked through `Next()`, which has no driver to
	// give; this is the one [Gate.WithDriver] was handed, kept so that what
	// the gate asks about a row is asked once in the transaction it names.
	g.P("	// drv is the driver this stack was put on, and nil for one that was not:")
	g.P("	// a transaction it runs in keeps what the gate has asked, for the rest")
	g.P("	// of it. See [seen].")
	g.P("	drv ", pkgDialect.Ident("Driver"))
	g.P("}")
	g.P("")
	g.P("func NewGate(next ", root.Ident("Server"), ") Gate {")
	g.P("	return Gate{Overlay: ", root.Ident("NewOverlay"), "(next)}")
	g.P("}")
	g.P("")
	g.P("var _ ", root.Ident("Server"), " = Gate{}")
	g.P("")

	// Every layer of a stack has to be rebindable for any of it to be, and a
	// layer that is not is only found out when a transaction is started. It
	// cannot be inherited from the overlay: the overlay holds what is behind
	// this server but has no way to make this server again, and a layer left
	// out of the rebinding is a layer the requests inside the transaction go
	// around.
	g.P("var _ ", pkgEnttx.Ident("Binder"), "[", root.Ident("Server"), "] = Gate{}")
	g.P("")
	g.P("// WithDriver answers with this stack running on `drv`, which is how several")
	g.P("// servers are put on one transaction.")
	g.P("//")
	g.P("// Every layer writes this and none can inherit it: an overlay holds what is")
	g.P("// behind it and has no way to make itself again, so a layer that did not")
	g.P("// write it would be missing from the rebuilt stack and the requests inside")
	g.P("// the transaction would go around it.")
	g.P("func (s Gate) WithDriver(drv ", pkgDialect.Ident("Driver"), ") (", root.Ident("Server"), ", error) {")
	g.P("	next, err := ", pkgEnttx.Ident("Rebind"), "(s.Next(), drv)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return Gate{Overlay: ", root.Ident("NewOverlay"), "(next), drv: drv}, nil")
	g.P("}")
	g.P("")
	g.P("// GateBuild makes a builder of this layer so that it can be stacked.")
	g.P("func GateBuild() ", root.Ident("Builder"), " { return gateBuilder{} }")
	g.P("")
	g.P("type gateBuilder struct{}")
	g.P("")
	g.P("func (gateBuilder) Build(next ", root.Ident("Server"), ") (", root.Ident("Server"), ", error) {")
	g.P("	return NewGate(next), nil")
	g.P("}")
	g.P("")

	// Tenant: neither putting one up nor taking one down is asked for from
	// inside one.
	g.P("type gateTenant struct {")
	g.P("	Gate")
	g.P("	", root.Ident("TenantServiceServer"))
	g.P("}")
	g.P("")
	ruled(g, p, root, "Gate", "Tenant", "s, s.Next().Tenant()")
	g.P("// admit refuses every row: no tenant is served by an Add, nor by an")
	g.P("// AddMany if the entity had one. A tenant is put up by whoever runs the")
	g.P("// deployment, through a server this layer is not in front of.")
	g.P("func (s gateTenant) admit(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident("TenantAddRequest"),
		") (*", root.Ident("TenantAddRequest"), ", error) {")
	g.P("	return nil, ", pkgGate.Ident("ErrDeployment"), "(\"put up\")")
	g.P("}")
	g.P("")
	g.P("// Erase is not served either, and it would take everything in the tenant")
	g.P("// with it.")
	g.P("func (s gateTenant) Erase(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident("TenantRef"),
		") (*", root.Ident("TenantEraseResponse"), ", error) {")
	g.P("	return nil, ", pkgGate.Ident("ErrDeployment"), "(\"taken down\")")
	g.P("}")
	g.P("")

	// Holder: the one rule about a holder that is not a predicate.
	g.P("type gateHolder struct {")
	g.P("	Gate")
	g.P("	", root.Ident("HolderServiceServer"))
	g.P("}")
	g.P("")
	ruled(g, p, root, "Gate", "Holder", "s, s.Next().Holder()")
	g.P("// admit is the one thing about a holder this layer still says, and it is here")
	g.P("// because it is the one that is not a predicate: the row does not exist yet,")
	g.P("// so there is nothing to narrow. Reading one, changing one and erasing one")
	g.P("// are all the wall.")
	g.P("//")
	g.P("// The check is a read of the tenant **through the wall** rather than a")
	g.P("// comparison against the scope, which costs a query and is worth it. A")
	g.P("// reference names a tenant by identifier or by alias, and answering \"is this")
	g.P("// one of mine\" without a query means holding every tenant in scope in full --")
	g.P("// fine while that is the caller's own and wrong as soon as it is a list a")
	g.P("// credential or a policy narrowed to.")
	g.P("//")
	g.P("// NotFound and not a refusal, which is the same answer every other read of a")
	g.P("// tenant gives: that one exists is itself something a caller who may not see")
	g.P("// it should not be told. It also gets a tenant that simply is not there")
	g.P("// right, which comparing against a scope did not.")
	g.P("//")
	g.P("// And what an app's overlay added to the holder is asked here with it, as it")
	g.P("// is of the app's own entities: a stamp a request may not assert, and an")
	g.P("// edge, which is a read of whatever it names.")
	g.P("//")
	g.P("// It is what an Add of a holder is asked, and an AddMany of every item it")
	g.P("// carries if the entity had one; see [ruled].")
	g.P("func (s gateHolder) admit(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident("HolderAddRequest"),
		") (*", root.Ident("HolderAddRequest"), ", error) {")
	g.P("	if _, err := ", pkgGate.Ident("Actor"), "(ctx); err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")

	// What an app's overlay says about a holder is said here as well, and not
	// in a gate of its own beside this one: that was two `gateHolder`s, and an
	// app that stamped a field of payday's holder had a server that did not
	// compile (#29). An overlay is how an app adds to payday's entity, so this
	// is the holder's whole `Add` -- the stamps before the read, as
	// [emitAdmit] has them, since they are a fact about the request.
	holder := s.Own(pdpb.Own_OWN_HOLDER)
	stamps(g, holder)

	readThrough(g, root, "req.GetTenant()", s.Own(pdpb.Own_OWN_TENANT), "	")
	g.P("")

	// And every edge an overlay gave it, for the reason on [emitAdmit]: an
	// edge is a read. That payday's own holder has none but its tenant is not
	// a promise about what an app adds to it.
	others(g, s, root, holder, "tenant")

	g.P("	return req, nil")
	g.P("}")
	g.P("")

	emitAdmitPatch(g, holder, s, root)

	emitAdmit(g, s, p, root)
}

// ruled writes the accessor of a layer's server for one entity, with the
// layer's `admit` put on the server behind as the rule of the RPCs that add
// (`bare.<E>WithAddRule`).
//
// A rule rather than an `Add` the layer overrides, because what the layer says
// about a row an Add would write it has to say about every row an AddMany
// writes. Written as an Add it is said in one of the two places, and the
// AddMany inherited from the server behind writes past it the day the entity
// asks for one. As a rule both are asked -- an AddMany once for each of its
// items, refused naming the first it refuses -- and what reaches the server
// behind is still one call.
//
// The rule is a method of the layer's server as it was before the rule was
// put on, so that it has everything the layer holds and calls nothing that
// would ask it again.
func ruled(g *protogen.GeneratedFile, p Paths, root protogen.GoImportPath, recv string, name string, fields string) {
	svc := root.Ident(name + "ServiceServer")

	g.P("func (s ", recv, ") ", name, "() ", svc, " {")
	g.P("	v := ", strings.ToLower(recv), name, "{", fields, "}")
	g.P("	v.", name, "ServiceServer = ", p.Bare.Ident(name+"WithAddRule"), "(v.", name, "ServiceServer, v.admit)")
	g.P("	return v")
	g.P("}")
	g.P("")
}

// emitAdmit writes, for every other entity behind the wall, the check
// payday's own holder has always had (`gateHolder.admit`).
//
// # The hole this closes
//
// The wall is a predicate, so it is on the query -- and an `Add` has no query.
// The identifier in an edge becomes a foreign key with nothing consulted, so a
// caller could put a row into a tenant it cannot see. Not a read leak: it never
// gets to look. What it gets is a row the victim reads as their own, usage the
// victim is billed for, and -- because the trail row is stamped with the
// **actor's** tenant -- a write the victim's audit log does not record.
//
// It was already written once, above, for `Holder`. What was missing is that
// the same sentence is true of every entity an app declares, and an app has no
// way to notice: every read is narrowed, so the row it planted disappears
// immediately afterwards and looks like a write that failed.
//
// # What is checked
//
// The **first hop** of the path that reaches the tenant, read through the wall.
// For the ordinary entity that is its `tenant` edge; for one that reaches the
// tenant through another row it is that row, and reading it is narrowed for the
// same reason. Either way the question is "may this caller see the thing it
// says this row belongs to", and a read is how it is asked -- see the comment
// on `gateHolder.admit` for why a comparison against the scope is not enough.
//
// **Every** edge whose target is behind the wall, and not only the first hop.
// That is a change, and the reasoning it replaces is worth keeping because it
// was careful and wrong: *an edge pointing at some other row in another tenant
// is a different question -- referential, not tenancy -- and is not asked
// here.*
//
// What that missed is that an edge is a **read**. A `Select` walks it, and a
// nested one walks further -- so a caller who may write a row of their own may
// point an edge at a row in another tenant and then read it back through their
// own. Demonstrated in an app on this framework: a caller allowed nothing but
// `EmailService/Add` and `EmailService/Get` planted `Email.vouched_by` on an
// identity of another tenant's and read back that identity's provider and
// subject, then its holder's alias, name and description, then that holder's
// tenant. One hop later the wall is not there.
//
// It is the same shape the generator already fixed once for **liveness** -- a
// select reached an erased parent because nothing narrowed the nested read --
// with *scope* substituted for *erased*. Fixing it at the write is not the only
// place it could be fixed, and it is the place where the wall's own invariant
// already lives.
//
// # What it costs
//
// A read per edge per `Add`, where it used to be one. Most entities have one or
// two, and an `Add` is not the path a server spends its time on. The old
// weighing dismissed this cost against a benefit it did not believe in; the
// benefit has since been demonstrated, so the weighing changes.
//
// Until a transaction writes a great many rows about the same few, which is
// what an import is: then it was the same read again for every one of them,
// and most of what the transaction sent (lesomnus/payday#84). So a read of a
// row named by key is asked once in a transaction begun with `dialect.BeginTx`
// -- see [readThrough].
//
// An edge to a **global** entity is not checked, because there is nothing to
// see past: a global row belongs to no tenant and is readable by everybody.
func emitAdmit(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	for _, v := range s.Sorted() {
		// The tenant is refused outright above and the holder's gate is written
		// out there too, with whatever an overlay added to it: a gate here as
		// well would be a second type of the same name (#29).
		if v.IsTenant || v.Own == pdpb.Own_OWN_HOLDER {
			continue
		}

		// No Add, nothing to admit: the gate is the Add's rule. An entity no
		// server writes is one of these.
		if !v.Rpcs().HasAdd() && !v.Rpcs().HasAddMany() {
			continue
		}

		// An entity that is not behind the wall has nothing to check. An entity
		// that names its tenant with a column (`field:`) is refused a
		// hand-written row by the audit layer.
		walled := !v.IsGlobal && len(v.Via) > 0

		var (
			hop string
			to  *Entity
		)
		if walled {
			hop = v.Via[0]

			e, ok := edge(v.Entity, hop)
			if !ok {
				walled = false
			} else if to, ok = s.of(e.Target().FullName()); !ok {
				walled = false
			}
		}
		if !walled && len(v.Stamped) == 0 {
			// Nothing to say about this entity at all: it is not behind the
			// wall, and it declares no stamp a request must not assert.
			continue
		}

		name := v.GoName()
		g.P("type gate", name, " struct {")
		g.P("	Gate")
		g.P("	", root.Ident(name+"ServiceServer"))
		g.P("}")
		g.P("")
		ruled(g, p, root, "Gate", name, "s, s.Next()."+name+"()")

		if walled {
			g.P("// admit refuses a ", name, " put into a ", to.GoName(), " this caller cannot see.")
			g.P("//")
			g.P("// The wall is a predicate and an Add has no query, so without this the")
			g.P("// identifier in `", hop, "` becomes a foreign key with nothing consulted.")
			g.P("// The row is then invisible to whoever planted it and visible to whoever")
			g.P("// holds that ", to.GoName(), ", which is the shape of the bug rather than a")
			g.P("// mitigation of it.")
			g.P("//")
			g.P("// NotFound rather than a refusal, for the reason on `gateHolder.admit`:")
			g.P("// that a row exists is itself something a caller who may not see it")
			g.P("// should not be told.")
		} else {
			g.P("// admit refuses a ", name, " whose request asserts a stamp.")
		}
		g.P("//")
		g.P("// It is what an Add is asked, and an AddMany of every item it carries;")
		g.P("// see [Gate.", name, "].")
		g.P("func (s gate", name, ") admit(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(name+"AddRequest"),
			") (*", root.Ident(name+"AddRequest"), ", error) {")

		// The stamps first, because they are a fact about the **request** and
		// cost no read: a request that asserts one is malformed whether or not
		// the rows it names are visible.
		stamps(g, v)

		if walled {
			seen(g, root, hop, to)
		}

		// And the second axis, when the app declared one. It is an isolation
		// boundary the same way the tenant is -- payday says so by reading
		// field 3 as one -- so an Add into a set the caller cannot see is the
		// same bug one level down.
		if walled && v.Set != "" && s.Set != nil {
			seen(g, root, v.Set, s.Set)
		}

		// And every other edge, for the reason at the top of this function,
		// skipping the two above rather than emitting them twice.
		if walled {
			others(g, s, root, v, hop, v.Set)
		}

		g.P("	return req, nil")
		g.P("}")
		g.P("")

		emitAdmitPatch(g, v, s, root)
	}
}

// emitAdmitPatch asks the same question of a `Patch` that moves an edge.
//
// # Why an Add check is not enough
//
// Because an edge that is not immutable can be pointed somewhere else
// afterwards, and the read it opens is the same read. A caller writes a row of
// their own with no edge at all, or a harmless one, and then patches it onto a
// row in another tenant.
//
// `/Patch` is closed at the transport by `grpcx.GeneralWrite` in most
// deployments, and that is not a reason to leave this out. `AllowGeneralWrites`
// is one line of configuration; an app's own command tree may build a server
// without the interceptor; and an app that adds a narrow write of its own has a
// second door onto the same column. This is the reading a rule about a **row**
// keeps, rather than the one a rule about a *transport* keeps -- and it is the
// same argument an app on this framework already wrote down about `Role.Patch`
// and `ApiKey.Patch`, which ask their own question in the layer *precisely so
// that the setting is a decision about the API rather than about who may become
// the administrator*.
//
// # Only the ones that can move
//
// An immutable edge is refused by the write itself, so asking about it here
// would be a read per Patch for an answer nothing can act on. The tenancy hop
// and the second axis are almost always immutable for that reason, so what is
// usually left is nothing at all and this emits no method.
func emitAdmitPatch(g *protogen.GeneratedFile, v *Entity, s *Schema, root protogen.GoImportPath) {
	moves := []struct {
		at string
		to *Entity
	}{}

	for e := range v.Edges() {
		if e.IsImmutable() {
			continue
		}

		to, ok := s.of(e.Target().FullName())
		if !ok || to.IsGlobal {
			continue
		}

		moves = append(moves, struct {
			at string
			to *Entity
		}{e.Name(), to})
	}
	if len(moves) == 0 {
		return
	}

	name := v.GoName()

	g.P("// Patch refuses an edge moved onto a row this caller cannot see.")
	g.P("//")
	g.P("// The wall narrows the row being written and says nothing about what the")
	g.P("// write points **at**. An edge is a read -- a `Select` walks it -- so one")
	g.P("// moved out of the caller's scope is a way through the wall one hop later,")
	g.P("// exactly as it would have been at `Add`.")
	g.P("func (s gate", name, ") Patch(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(name+"PatchRequest"),
		") (*", root.Ident(name), ", error) {")
	for _, m := range moves {
		seen(g, root, m.at, m.to)
	}
	g.P("	return s.", name, "ServiceServer.Patch(ctx, req)")
	g.P("}")
	g.P("")
}

// stamps writes the refusal of a request that asserts what the deployment
// establishes.
//
// It is here, in the gate, and that is the whole of what makes it right. A
// stamp is not a permission and not a column a caller may never touch: it is a
// fact somebody's **request** may not assert, and the gate is the layer that
// exists because there is a request at all. An app's own work goes through a
// server the gate is not on -- `init`, a seed, the call that writes the stamp
// on the day the thing happened -- and is untouched, without anybody having to
// list those callers.
//
// `Patch` is left alone for the same reason: that is the road the stamp is
// written by.
func stamps(g *protogen.GeneratedFile, v *Entity) {
	for _, at := range v.Stamped {
		p, ok := prop(v.Entity, at)
		if !ok {
			continue
		}
		if f, ok := p.(graph.Field); ok && (f.IsVersion() || f.IsErased()) {
			// Neither is in the request at all -- the server stamps both --
			// so there is nothing for one to assert.
			continue
		}

		g.P("	if ", asserted(p), " {")
		g.P("		return nil, ", pkgPderr.Ident("Invalidf"), "(", strconv.Quote(at), ", ",
			strconv.Quote("is established by this deployment and not asserted by a request"), ")")
		g.P("	}")
		g.P("")
	}
}

// asserted is the condition an `Add` request meets by saying anything about
// `p`.
//
// The request's shape decides it rather than the entity's, and that shape is
// the ORM's (`protoc-gen-orm-service`, `xMsgAddRequest`): a scalar the row
// cannot do without is declared `IMPLICIT` and has no `Has` -- its zero is what
// every request that did not set it reads as -- and everything else keeps the
// presence it has. Asking `Has` of the first kind is what did not compile
// (#29); a timestamp, which is what stamps had been, has presence either way.
//
// Where there is presence it is the test, being the stronger one: a value set
// on purpose to its zero is still a request saying something. Where there is
// none, a zero is saying nothing, and so is an empty list or map.
func asserted(p graph.Prop) string {
	at := pascal(p.Name())
	fd := p.Descriptor()

	if fd.IsList() || fd.IsMap() {
		return "len(req.Get" + at + "()) != 0"
	}
	if _, ok := p.(graph.Field); !ok || !p.Type().IsScalar() || p.IsOptional() {
		return "req.Has" + at + "()"
	}

	switch fd.Kind() {
	case protoreflect.BoolKind:
		return "req.Get" + at + "()"
	case protoreflect.StringKind:
		return "req.Get" + at + "() != \"\""
	case protoreflect.BytesKind:
		return "len(req.Get" + at + "()) != 0"
	default:
		// A number, or an enum, whose zero is the value that names nothing.
		return "req.Get" + at + "() != 0"
	}
}

// others writes [seen] for every edge of `v` but the ones in `skip`: an edge is
// a read, so one pointing out of the caller's scope is a way through the wall
// one hop later (see [emitAdmit]).
//
// In declaration order, so that the generated file is stable.
func others(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath, v *Entity, skip ...string) {
	for e := range v.Edges() {
		at := e.Name()
		if slices.Contains(skip, at) {
			continue
		}

		target, ok := s.of(e.Target().FullName())
		if !ok || target.IsGlobal {
			// A target this schema does not describe, or one that belongs to
			// no tenant. Neither has a wall to be on the wrong side of.
			continue
		}

		seen(g, root, at, target)
	}
}

// seen writes the read that asks whether the caller may see what an edge names.
//
// Through the layer's own `Next()`, so it travels every scope the app installed
// -- the wall, and the second axis if there is one -- rather than comparing
// against one of them and missing the other.
func seen(g *protogen.GeneratedFile, root protogen.GoImportPath, edge string, to *Entity) {
	g.P("	if ref := req.Get", pascal(edge), "(); ref != nil {")
	readThrough(g, root, "ref", to, "		")
	g.P("	}")
	g.P("")
}

// readThrough writes the read itself, of the `to` that `ref` names, through the
// wall, and what it is answered with when it finds nothing.
//
// Once a transaction for a row named by key, when the transaction was begun
// with `dialect.BeginTx` -- which the gate learns from the driver it was put on
// (`Gate.drv`): a write that names the same row as the write before it asks
// the same question, and the answer is the one it got then. That is not the
// comparison against the scope `gateHolder.admit` refuses to make -- it is this
// read, remembered -- and `payday/seen` says why remembering it neither waits
// on a write nor reaches past the wall. A row named some other way is read
// every time, since the name may be another row's by then.
func readThrough(g *protogen.GeneratedFile, root protogen.GoImportPath, ref string, to *Entity, in string) {
	get := func(in string) {
		g.P(in, "_, err := s.Gate.Next().", to.GoName(), "().Get(ctx, ",
			root.Ident(to.GoName()+"GetRequest_builder"), "{")
		g.P(in, "	Ref: ", ref, ",")
		g.P(in, "}.Build())")
	}

	if byKey(to) {
		g.P(in, "if err := ", pkgSeen.Ident("Visible"), "(ctx, s.Gate.drv, ", strconv.Quote(to.GoName()), ", ", ref, ", func() error {")
		get(in + "	")
		g.P(in, "	return err")
		g.P(in, "}); err != nil {")
	} else {
		g.P(in, "if _, err := s.Gate.Next().", to.GoName(), "().Get(ctx, ",
			root.Ident(to.GoName()+"GetRequest_builder"), "{")
		g.P(in, "	Ref: ", ref, ",")
		g.P(in, "}.Build()); err != nil {")
	}
	g.P(in, "	if ", pkgStatus.Ident("Code"), "(err) == ", pkgCodes.Ident("NotFound"), " {")
	g.P(in, "		return nil, ", pkgGate.Ident("ErrNotFound"), "(", strconv.Quote(to.GoName()), ")")
	g.P(in, "	}")
	g.P("")
	g.P(in, "	return nil, err")
	g.P(in, "}")
}

// byKey is whether a reference to `v` can name its row by identifier, which is
// what `payday/seen` remembers an answer for.
//
// Every entity behind the wall can -- payday names those rows by identifier,
// as [emitSubject] does -- and this is the check that it still holds rather
// than a reference that stopped compiling.
func byKey(v *Entity) bool {
	k := v.Key()
	return k != nil && k.Name() == "id" && k.Type() == ormpb.Type_TYPE_UUID
}

// EmitAudit writes the layer that refuses hand-written trail rows, and the
// recorder that writes the real ones.
//
// The two do not look alike and that is the design. The recorder is not a layer
// at all: it is handed to the generated servers and called from inside the
// transaction that makes a write, so every RPC that changes anything is on the
// trail without anybody having listed them. The layer is here because a trail a
// deployment can edit is evidence of nothing.
func EmitAudit(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	// Refused by [CheckOwn] before this, for a real app; see there.
	if !s.Has(pdpb.Own_OWN_AUDIT) {
		return
	}

	g.P("// Audit is the layer that refuses a trail row written by hand.")
	g.P("//")
	g.P("// The Rpcs exist because the trail is an entity like any other and a test is")
	g.P("// far plainer for having them. A deployment serves none of the ones that")
	g.P("// write: a trail somebody can edit is evidence of nothing.")
	g.P("type Audit struct {")
	g.P("	", root.Ident("Overlay"))
	g.P("}")
	g.P("")
	g.P("func NewAudit(next ", root.Ident("Server"), ") Audit {")
	g.P("	return Audit{", root.Ident("NewOverlay"), "(next)}")
	g.P("}")
	g.P("")
	g.P("var _ ", root.Ident("Server"), " = Audit{}")
	g.P("var _ ", pkgEnttx.Ident("Binder"), "[", root.Ident("Server"), "] = Audit{}")
	g.P("")
	g.P("// WithDriver answers with this stack running on `drv`; see [Gate.WithDriver].")
	g.P("func (s Audit) WithDriver(drv ", pkgDialect.Ident("Driver"), ") (", root.Ident("Server"), ", error) {")
	g.P("	next, err := ", pkgEnttx.Ident("Rebind"), "(s.Next(), drv)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return NewAudit(next), nil")
	g.P("}")
	g.P("")
	g.P("// AuditBuild makes a builder of this layer so that it can be stacked.")
	g.P("func AuditBuild() ", root.Ident("Builder"), " { return auditBuilder{} }")
	g.P("")
	g.P("type auditBuilder struct{}")
	g.P("")
	g.P("func (auditBuilder) Build(next ", root.Ident("Server"), ") (", root.Ident("Server"), ", error) {")
	g.P("	return NewAudit(next), nil")
	g.P("}")
	g.P("")

	g.P("type auditService struct {")
	g.P("	Audit")
	g.P("	", root.Ident("AuditServiceServer"))
	g.P("}")
	g.P("")
	g.P("func (s Audit) Audit() ", root.Ident("AuditServiceServer"), " {")
	g.P("	return auditService{s, s.Next().Audit()}")
	g.P("}")
	g.P("")

	for _, rpc := range []struct{ name, in, out string }{
		{"Add", "AuditAddRequest", "Audit"},
		{"Patch", "AuditPatchRequest", "Audit"},
		{"Apply", "AuditApplyRequest", "Audit"},
	} {
		g.P("func (s auditService) ", rpc.name, "(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(rpc.in),
			") (*", root.Ident(rpc.out), ", error) {")
		g.P("	return nil, errTrail()")
		g.P("}")
		g.P("")
	}
	g.P("func (s auditService) Erase(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident("AuditRef"),
		") (*", root.Ident("AuditEraseResponse"), ", error) {")
	g.P("	return nil, errTrail()")
	g.P("}")
	g.P("")
	g.P("// errTrail is Unimplemented and not PermissionDenied, and to everybody: it")
	g.P("// is not about who is asking, and no credential changes it.")
	g.P("func errTrail() error {")
	g.P("	return ", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("Unimplemented"), ",")
	g.P("		\"the trail is written by what happened, not by anybody asking\")")
	g.P("}")
	g.P("")

	emitRecorder(g, s, p, root)
}

// emitRecorder writes what turns a write into a line of the trail.
func emitRecorder(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	g.P("// Recorder writes one row of the trail for every write the generated")
	g.P("// servers make, keeping as much of each as `content` says of its kind.")
	g.P("//")
	g.P("// It is not a layer and it does not override an Rpc. The servers call it")
	g.P("// from inside the transaction that makes the write, so the row and the")
	g.P("// record of it hold or fall together -- and so every Rpc that changes")
	g.P("// anything is on the trail without anybody having listed them.")
	g.P("//")
	g.P("//	content, err := c.Audit.Contents()")
	g.P("//	sink, err := bare.NewServer(db, bare.WithRecorder(pd.Recorder(content)))")
	g.P("//")
	g.P("// The content is asked for rather than defaulted, so that a deployment's")
	g.P("// `audit.content` is not something an app can configure and then forget to")
	g.P("// hand over: the zero value is the default, and passing it says so.")
	g.P("func Recorder(content ", pkgAudit.Ident("Contents"), ") ", p.Bare.Ident("Recorder"), " { return recorder{content} }")
	g.P("")
	g.P("type recorder struct{ content ", pkgAudit.Ident("Contents"), " }")
	g.P("")
	g.P("var _ ", p.Bare.Ident("SnapshotRecorder"), " = recorder{}")
	g.P("")
	// Every write's, since every write is a line of the trail: the method is
	// not asked about. An Erase is handed no row whatever is asked -- what an
	// erase leaves is not a row the write read -- and [subject] reads one for
	// it, as it did for every write before this.
	g.P("// Snapshots asks for the row each write leaves, which is what a line of the")
	g.P("// trail holds. The write has it already: an Add's INSERT reads it back, in")
	g.P("// the same statement where the dialect has RETURNING, and a Patch or an")
	g.P("// Apply reads it back to answer with. So Record reads it again only for an")
	g.P("// Erase, which hands none over.")
	g.P("func (recorder) Snapshots(string) ", p.Bare.Ident("SnapshotOptions"), " {")
	g.P("	return ", p.Bare.Ident("SnapshotOptions"), "{Value: true}")
	g.P("}")
	g.P("")
	g.P("// Record writes what happened. It runs inside the write's transaction, so an")
	g.P("// error here takes the write with it -- which is the answer wanted: a write")
	g.P("// that could not be accounted for did not happen.")
	g.P("//")
	g.P("// It writes through the server it was handed, which runs on that transaction")
	g.P("// and does not record; a recorder that recorded its own writes would not")
	g.P("// stop.")
	g.P("func (r recorder) Record(ctx ", pkgCtx.Ident("Context"), ", s ", p.Bare.Ident("Server"),
		", c ", p.Bare.Ident("Change"), ") error {")
	g.P("	// Which entity this is about, before anything decides what of the")
	g.P("	// write may be kept: a field declared `secret:` or `personal:` belongs")
	g.P("	// to one entity, and the identifier's domain byte is the only thing")
	g.P("	// that says which.")
	g.P("	key, err := ", pkgAudit.Ident("Identifier"), "(c.Key)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	// How much the deployment keeps of this kind is its to say, and which
	// values are never kept at all is the schema's; see `audit.Content`.
	g.P("	level := r.content.Of(key.Domain())")
	g.P("	md, secret, personal := described(key)")
	g.P("")
	// Read before anything is taken out of it, which is what lets the trail
	// say a field was set when it may not say what to.
	g.P("	// What the write said: the document it was compiled from, and for an")
	g.P("	// Add, which was compiled from none, the row it made as a patch from")
	g.P("	// the empty row.")
	g.P("	doc := c.Patch")
	g.P("	if doc == nil && c.Value != nil && added(c.By) {")
	g.P("		if doc, err = ", pkgAudit.Ident("Created"), "(c.Value); err != nil {")
	g.P("			return ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("Internal"), ", \"the trail of %s: %s\", c.Method, err)")
	g.P("		}")
	g.P("	}")
	g.P("	fields := []uint32{}")
	g.P("	if md != nil {")
	g.P("		fields = ", pkgAudit.Ident("FieldsOf"), "(doc, md)")
	g.P("	}")
	g.P("")
	// The digest of the row the write left (payday#102), made from the one it
	// handed over -- so no statement more than the write already made. Every
	// write but an Erase hands one, and the newest line of the trail about a
	// live row is never an Erase's, which is what a check of the rows asks.
	g.P("	// A keyed digest of the row the write left, when the deployment keeps")
	g.P("	// them: of the row it handed over, without what the entity declared")
	g.P("	// `secret:` or `personal:`, and over the fields this build knows.")
	g.P("	digest, covered := []byte{}, []uint32{}")
	g.P("	if d := r.content.Digest; d != nil && c.Value != nil && md != nil {")
	g.P("		row := ", pkgProto.Ident("Clone"), "(c.Value)")
	g.P("		", pkgAudit.Ident("Clear"), "(row, secret)")
	g.P("		", pkgAudit.Ident("Clear"), "(row, personal)")
	g.P("		covered = ", pkgAudit.Ident("Covered"), "(md, ", pkgSlices.Ident("Concat"), "(secret, personal))")
	g.P("		if digest, err = d.Of(key, row, covered); err != nil {")
	g.P("			return ", pkgStatus.Ident("Errorf"), "(", pkgCodes.Ident("Internal"), ", \"the trail of %s: %s\", c.Method, err)")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	var patch ", pkgProto.Ident("Message"))
	g.P("	if level != ", pkgAudit.Ident("ContentFields"), " {")
	g.P("		if v := ", pkgAudit.Ident("Withhold"), "(doc, ", pkgSlices.Ident("Concat"), "(secret, personal)); v != nil {")
	g.P("			patch = v")
	g.P("		}")
	g.P("	}")
	g.P("")
	g.P("	v, err := ", pkgAudit.Ident("Of"), "(ctx, c.Method, c.Key, patch)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	g.P("	// The row this happened to, which is where the object's tenant and the")
	g.P("	// state after the write both come from: the one the write handed over,")
	g.P("	// or read when it handed none. Absent for an Erase that took the row")
	g.P("	// with it; see [subject].")
	g.P("	tenant, value, err := subject(ctx, s, v.Object, c.Value, level == ", pkgAudit.Ident("ContentValue"), ")")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("	if tenant == ", pkgUuid.Ident("Nil"), "()", " {")
	g.P("		// Nothing to file it under but the actor's own, which is what the")
	g.P("		// trail said for every row before this. It is the honest fallback")
	g.P("		// rather than a zero: a record nobody can read is not a record.")
	g.P("		tenant = ", pkgUuid.Ident("UUID"), "(v.Tenant)")
	g.P("	}")
	g.P("")
	// A variable, because the field carries a default and so the builder takes
	// a pointer -- *not given* and *given as zero* are different things to a
	// request, and zero here is `pdid.Unknown`, which is a real answer. This
	// write always knows, so it always says.
	g.P("	domain := uint32(v.Object.Domain())")
	g.P("")
	if keepsTrail(s) {
		// The same row as the one below, decided here as that one is -- its
		// key, the moment it was recorded -- and written with the rest of what
		// the transaction recorded rather than now. See [emitKeptTrail].
		g.P("	// Kept for the commit, when the transaction keeps anything: written")
		g.P("	// there with the rest of what it recorded, a statement for every few")
		g.P("	// hundred rows rather than one each. The row is decided here, as the")
		g.P("	// one below is -- its key, the moment it was recorded -- and only the")
		g.P("	// writing waits. See [keptTrail].")
		g.P("	if k, ok := ", pkgDialect.Ident("KeepOf"), "(s.Store.Db.Driver()); ok {")
		g.P("		q := s.Store.Db.Audit.Create().")
		g.P("			SetId(", pkgUuid.Ident("New"), "()).")
		g.P("			SetTenantId(tenant).")
		g.P("			SetActorTenantId(", pkgUuid.Ident("UUID"), "(v.Tenant)).")
		g.P("			SetActorId(", pkgUuid.Ident("UUID"), "(v.Actor)).")
		g.P("			SetTraceId(notNull(v.Trace)).")
		g.P("			SetAction(v.Action).")
		g.P("			SetObjectId(", pkgUuid.Ident("UUID"), "(v.Object)).")
		g.P("			SetDomain(domain).")
		g.P("			SetPatch(notNull(v.Patch)).")
		g.P("			SetValue(notNull(value)).")
		g.P("			SetFields(fields).")
		g.P("			SetDigest(digest).")
		g.P("			SetDigestFields(covered).")
		g.P("			SetDateCreated(", pkgTime.Ident("Now"), "().UTC())")
		g.P("		if !v.Counterpart.IsZero() {")
		g.P("			q.SetCounterpartTenantId(", pkgUuid.Ident("UUID"), "(v.Counterpart))")
		g.P("		}")
		g.P("")
		g.P("		return keptTrailOf(k, s.Store.Db).add(ctx, q)")
		g.P("	}")
		g.P("")
	}
	g.P("	_, err = s.Audit().Add(ctx, ", root.Ident("AuditAddRequest_builder"), "{")
	g.P("		TenantId:      tenant[:],")
	g.P("		ActorTenantId: v.Tenant.Bytes(),")
	g.P("		ActorId:       v.Actor.Bytes(),")
	// The three columns payday computes that are NOT NULL; see [emitNotNull]
	// for why they are normalized here rather than where each is produced.
	g.P("		TraceId:       notNull(v.Trace),")
	g.P("		Action:        v.Action,")
	g.P("		ObjectId:      v.Object.Bytes(),")
	// The kind, as a column, because a byte inside a `uuid` answers *what was
	// this row about* and no index answers *which rows were about robots*. The
	// second is the question a retention policy is made of; see the note on
	// `domain` in payday's audit.proto.
	g.P("		Domain:        &domain,")
	g.P("		Patch:         notNull(v.Patch),")
	g.P("		Value:         notNull(value),")
	g.P("		Fields:        fields,")
	g.P("		Digest:        digest,")
	g.P("		DigestFields:  covered,")
	// Zero unless a layer said otherwise, which is nearly every write. It is
	// the other tenant this one was about, and the wall on the trail counts
	// it; see `audit.Concerning`.
	g.P("		CounterpartTenantId: v.CounterpartBytes(),")
	g.P("	}.Build())")
	g.P("")
	g.P("	return err")
	g.P("}")
	g.P("")

	emitNotNull(g)
	emitHidden(g, s, root)
	emitSubject(g, s, p, root)
	if keepsTrail(s) {
		emitKeptTrail(g, s, p)
	}
	emitDigests(g, s, p, root)

	// Referenced only so that the import is kept when a schema has no patch.
	g.P("var _ *", pkgPatchpb.Ident("Patch"))
	g.P("")
}

// keepsTrail is whether the recorder writes a row of the trail itself, which is
// how it keeps one for the commit of a transaction that keeps anything.
//
// It can when the trail is payday's own and nothing more, so that every column
// is one it knows how to fill. An overlay may add to payday's entities, and a
// column added to the trail is one only the server that was generated with it
// can fill -- so that trail is written a row at a time as it always was, a
// statement each, rather than with a column missing.
func keepsTrail(s *Schema) bool {
	v := s.Own(pdpb.Own_OWN_AUDIT)
	if v == nil {
		return false
	}

	own := map[string]bool{
		"id": true, "tenant_id": true, "actor_id": true, "trace_id": true,
		"action": true, "object_id": true, "patch": true, "date_created": true,
		"actor_tenant_id": true, "value": true, "counterpart_tenant_id": true,
		"domain": true, "fields": true, "digest": true, "digest_fields": true,
	}
	n := 0
	for p := range v.Props() {
		if _, ok := p.(graph.Field); !ok || !own[p.Name()] {
			return false
		}
		n++
	}

	return n == len(own)
}

// emitKeptTrail writes where a transaction keeps the trail it records until it
// commits.
//
// # Why
//
// Every write is a row of the trail, written by the recorder inside the
// transaction that makes it -- and so a statement of its own, a round trip
// each. A transaction that writes many rows, which is what an import is, spent
// a quarter of what it sent on those (#84). They are written together instead,
// as [dialect.Keep] lets a transaction do: before it commits, a statement for
// every few hundred rows.
//
// # What it does not change
//
// That the row and the write hold or fall together: the rows are written in
// the transaction, by its commit, and an error doing so is the commit's -- the
// transaction is rolled back and nothing in it holds. That a write is on the
// trail: the commit is what writes them, so whoever ends the transaction cannot
// leave them out. What a row says: it is decided when the write is recorded,
// its moment included, and only the writing waits.
//
// Nor what a statement about the trail finds inside that transaction. A row
// kept to be written is a row the table does not have yet, and erasure is the
// write that showed what that costs: it blanks what the trail holds of a person
// in the same transaction as the writes that put it there, and blanked a table
// the newest of those rows were not in -- they were written after it, whole.
// So any statement about the trail in the transaction -- counting it, blanking
// it, reading it back -- has what is kept written first
// ([dialect.Keep.BeforeStatement]), and finds the table as if every row had
// been written when it was recorded. A transaction that sends none, which is
// what writing many rows is, has them written by its commit alone.
//
// It is the statement that is asked rather than the code that sends it,
// because that is where every way of reaching the table meets: a client built
// any way, a query or a count or an update, payday's trail store and an app's
// own erasure alike. A statement is about the trail when the table's name is a
// word of it, which is how a builder writes a table whatever it quotes it with.
//
// # Every few hundred
//
// Rather than all at once, because a statement holds only so many arguments --
// SQLite and PostgreSQL both stop somewhere near sixty thousand, and a row here
// is a dozen -- and because what is kept is held in memory until it is
// written. A few hundred rows a statement is already a few hundred times fewer
// statements.
func emitKeptTrail(g *protogen.GeneratedFile, s *Schema, p Paths) {
	table := (p.Ent + "/" + protogen.GoImportPath(s.Own(pdpb.Own_OWN_AUDIT).EntPkg())).Ident("Table")

	g.P("// keptTrail is the trail a transaction has recorded and not written yet.")
	g.P("//")
	g.P("// It is kept by the transaction ([dialect.Keep]) and written by its commit,")
	g.P("// or a few hundred rows at a time before that, or ahead of any statement")
	g.P("// about the trail the transaction sends. The commit is what writes it, so")
	g.P("// a transaction cannot be ended with any of it left out, and one rolled")
	g.P("// back takes it along unwritten.")
	g.P("type keptTrail struct {")
	g.P("	// db is the client of the first write the transaction recorded, which")
	g.P("	// is as good as any other's: every one of them is that transaction.")
	g.P("	db   *", p.Ent.Ident("Client"))
	g.P("	rows []*", p.Ent.Ident("AuditCreate"))
	g.P("")
	g.P("	// writing is the rows being written, by a statement that is itself")
	g.P("	// about the trail: asked to write them first, it finds them on the way.")
	g.P("	writing bool")
	g.P("}")
	g.P("")
	g.P("// keptTrailKey is what a transaction keeps its [keptTrail] under.")
	g.P("type keptTrailKey struct{}")
	g.P("")
	g.P("// keptTrailRows is how many rows of the trail one statement writes.")
	g.P("const keptTrailRows = 500")
	g.P("")
	g.P("// keptTrailAbout is whether a statement is about the trail: the table's")
	g.P("// name as a word of it. Every statement that is about it has what is kept")
	g.P("// written first, so that it finds every row the transaction recorded.")
	g.P("var keptTrailAbout = ", pkgRegexp.Ident("MustCompile"), "(`\\b` + ", pkgRegexp.Ident("QuoteMeta"), "(", table, ") + `\\b`).MatchString")
	g.P("")
	g.P("func keptTrailOf(k *", pkgDialect.Ident("Keep"), ", db *", p.Ent.Ident("Client"), ") *keptTrail {")
	g.P("	return k.Value(keptTrailKey{}, func() any {")
	g.P("		v := &keptTrail{db: db}")
	g.P("		k.BeforeCommit(v.write)")
	g.P("		k.BeforeStatement(keptTrailAbout, v.write)")
	g.P("		return v")
	g.P("	}).(*keptTrail)")
	g.P("}")
	g.P("")
	g.P("func (v *keptTrail) add(ctx ", pkgCtx.Ident("Context"), ", q *", p.Ent.Ident("AuditCreate"), ") error {")
	g.P("	v.rows = append(v.rows, q)")
	g.P("	if len(v.rows) < keptTrailRows {")
	g.P("		return nil")
	g.P("	}")
	g.P("")
	g.P("	return v.write(ctx)")
	g.P("}")
	g.P("")
	g.P("// write writes what is kept, a statement for each few hundred rows.")
	g.P("func (v *keptTrail) write(ctx ", pkgCtx.Ident("Context"), ") error {")
	g.P("	if v.writing {")
	g.P("		return nil")
	g.P("	}")
	g.P("	v.writing = true")
	g.P("	defer func() { v.writing = false }()")
	g.P("")
	g.P("	for len(v.rows) > 0 {")
	g.P("		n := min(len(v.rows), keptTrailRows)")
	g.P("		if err := v.db.Audit.CreateBulk(v.rows[:n]...).Exec(ctx); err != nil {")
	g.P("			return ", pkgFmt.Ident("Errorf"), "(\"write the trail: %w\", err)")
	g.P("		}")
	g.P("		v.rows = v.rows[n:]")
	g.P("	}")
	g.P("")
	g.P("	return nil")
	g.P("}")
	g.P("")
}

// emitHidden writes the patch document as the trail may hold it: without the
// fields the schema said are never answered with.
//
// # The half of `secret:` that was not there
//
// `(payday.field).secret` clears a column on the way out of a read, and the
// trail is the other way a row leaves: `hide<E>` is what keeps a verifier out
// of `Audit.value`, which is the row as the write left it.
//
// `Audit.patch` is the same write from the other end -- the document it was
// compiled from -- and nothing touched it. So the one RPC whose whole job is to
// take a secret in and never hand one back wrote the argon2 string into the
// trail in full, and the trail is **served**: `AuditService` is a generated
// service like any other, and the wall files a credential's row under its
// person's tenant. Anybody in that tenant whose role reaches the trail could
// read the password hash of everybody in it.
//
// Which is the read `CredentialService` is unregistered to prevent, arriving
// by the road nobody had looked down.
//
// # Entries and not values
//
// An entry is dropped rather than blanked. A patch is what the caller asked
// for, and *asked for `secret` to become something* is a true and complete
// record of the ask with the value taken out -- but there is no shape for it:
// the value is a `oneof` and an entry carrying none is a document that no
// longer applies. What the trail keeps instead is every other entry of the
// same write, and `Audit.value` says what the row became.
//
// # By number, and only the first segment
//
// A path is a list of segments and the first is the entity's own field. A
// deeper one is inside a message-valued field, and a message field cannot be
// declared `secret` -- the declaration is on the column. Matching the first
// segment by **number** rather than by name is the same choice `pdgen` makes
// everywhere: a name is a label and the number is the field.
func emitHidden(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath) {
	g.P("// described is the message the rows of `key`'s entity are, and the fields of")
	g.P("// it a row of the trail holds no value of: the ones it declared `secret`,")
	g.P("// which nothing answers with, and the ones it declared `personal`, which the")
	g.P("// trail records the setting of and not the value (#92). Nil for a domain")
	g.P("// this app does not have.")
	g.P("//")
	g.P("// Which entity it is comes from the identifier, as everywhere else here: a")
	g.P("// `pdid` carries its domain, so one switch answers it for every entity")
	g.P("// there will ever be.")
	g.P("func described(key ", pkgPdid.Ident("Id"), ") (", pkgReflect.Ident("MessageDescriptor"), ", []", pkgReflect.Ident("FieldDescriptor"), ", []", pkgReflect.Ident("FieldDescriptor"), ") {")
	g.P("	switch key.Domain() {")
	for _, v := range s.Sorted() {
		g.P("	case ", v.GoName(), "Domain:")
		g.P("		return described", v.GoName(), ".md, described", v.GoName(), ".secret, described", v.GoName(), ".personal")
	}
	g.P("	}")
	g.P("")
	g.P("	return nil, nil, nil")
	g.P("}")
	g.P("")

	g.P("// description is what [described] answers for one entity.")
	g.P("type description struct {")
	g.P("	md               ", pkgReflect.Ident("MessageDescriptor"))
	g.P("	secret, personal []", pkgReflect.Ident("FieldDescriptor"))
	g.P("}")
	g.P("")
	g.P("func describe(m ", pkgProto.Ident("Message"), ", secret, personal []", pkgReflect.Ident("FieldNumber"), ") description {")
	g.P("	md := m.ProtoReflect().Descriptor()")
	g.P("	out := description{md: md}")
	g.P("	for _, n := range secret {")
	g.P("		out.secret = append(out.secret, md.Fields().ByNumber(n))")
	g.P("	}")
	g.P("	for _, n := range personal {")
	g.P("		out.personal = append(out.personal, md.Fields().ByNumber(n))")
	g.P("	}")
	g.P("")
	g.P("	return out")
	g.P("}")
	g.P("")

	numbers := func(ns []uint32) string {
		out := make([]string, 0, len(ns))
		for _, n := range ns {
			out = append(out, strconv.FormatUint(uint64(n), 10))
		}
		return strings.Join(out, ", ")
	}
	g.P("var (")
	for _, v := range s.Sorted() {
		// By number, which is what a document names a field by and what the
		// declaration is about; see SecretNumbers.
		g.P("	described", v.GoName(), " = describe((*", root.Ident(v.GoName()), ")(nil),")
		g.P("		[]", pkgReflect.Ident("FieldNumber"), "{", numbers(v.SecretNumbers), "},")
		g.P("		[]", pkgReflect.Ident("FieldNumber"), "{", numbers(v.PersonalNumbers), "})")
	}
	g.P(")")
	g.P("")

	g.P("// hidden is `p` without the entries the schema said are never answered with,")
	g.P("// or nil when there is nothing left to say.")
	g.P("//")
	g.P("// It is the redactor of every copy of a write that leaves the write -- the")
	g.P("// trail's, the outbox's and the in-process publish's -- and what it takes")
	g.P("// out is a `secret:` and nothing else. The trail takes a `personal:` field")
	g.P("// out as well, and does that itself: the outbox and the publish carry the")
	g.P("// row they are about, which whoever may read the row reads the person in.")
	g.P("func hidden(key ", pkgPdid.Ident("Id"), ", p *", pkgPatchpb.Ident("Patch"), ") *", pkgPatchpb.Ident("Patch"), " {")
	g.P("	_, secret, _ := described(key)")
	g.P("")
	g.P("	return ", pkgAudit.Ident("Withhold"), "(p, secret)")
	g.P("}")
	g.P("")

	g.P("// added is whether `by`, the write a server made, made a row: an Add, or an")
	g.P("// AddMany's for one of its items. See `bare.Change.By`.")
	g.P("func added(by string) bool {")
	adds := []any{}
	for _, v := range s.Sorted() {
		for _, rpc := range []struct {
			name string
			has  bool
		}{
			{"Add", v.Rpcs().HasAdd()},
			{"AddMany", v.Rpcs().HasAddMany()},
		} {
			if !rpc.has {
				continue
			}
			if len(adds) > 0 {
				adds = append(adds, ",\n\t\t")
			}
			adds = append(adds, root.Ident(v.GoName()+"Service_"+rpc.name+"_FullMethodName"))
		}
	}
	if len(adds) > 0 {
		g.P("	switch by {")
		g.P(append(append([]any{"	case "}, adds...), ":")...)
		g.P("		return true")
		g.P("	}")
		g.P("")
	}
	g.P("	return false")
	g.P("}")
	g.P("")
}

// emitNotNull writes the one place a value passes through before the trail's
// NOT NULL columns see it.
//
// # Why the assembly point and not the producers
//
// Four things compute one of these -- the trace, the patch document, the row
// read back, and the answer for a row there is none of -- and every one of
// them is a `[]byte` that can come out nil. Defending each is defending a
// list, and a list is only as good as whoever adds the next entry to it. The
// trace and the patch each carry their own "empty rather than nil, and here is
// the trap"; the read for an entity with no tenant sits beside them and
// answered nil, and every write to a global entity was refused by PostgreSQL
// and taken by SQLite, where no test could see it.
//
// The builder below is where all four meet, and it is the last thing between
// them and the column. A path added to `subject` is covered by having been
// added, which is the difference between this and a fifth per-site fix.
//
// # Why not the runtime
//
// `payday/audit` is where the trail's decisions live, and two of these four
// already answer empty there. It cannot hold this one: `value` is a row read
// back through the app's own servers, and payday has no name for those types.
// So a defense there reaches the two columns that have never been the bug and
// not the one that has been it twice -- and a defense in two halves is the
// list again, with two places to remember instead of four.
//
// # What it must not be given
//
// `counterpart_tenant_id`, which is why this is a call per column rather than
// something applied to the request. That column is nullable on purpose -- nil
// is how a row says there was no other party to the write -- and normalizing
// it would put a zero identifier nobody holds in a column the wall reads. Nil
// is taken away only where the column has left it no room to mean anything.
func emitNotNull(g *protogen.GeneratedFile) {
	g.P("// notNull is `v` as a column that says it always has bytes takes it.")
	g.P("//")
	g.P("// The two drivers disagree about exactly one value: a nil `[]byte` is Sql")
	g.P("// NULL to pgx and an empty blob to SQLite's driver. So a nil bound to a NOT")
	g.P("// NULL column is a write that passes on the database the tests run on and is")
	g.P("// refused by the one the app is deployed on.")
	g.P("//")
	g.P("// Nothing is hidden by it. These columns are NOT NULL, so there is no nil for")
	g.P("// them to mean: an empty `trace_id` is a call nobody traced, an empty `patch`")
	g.P("// is a write that was not compiled from a document, an empty `value` is a row")
	g.P("// that was not there to read. `counterpart_tenant_id` is nullable so that nil")
	g.P("// can say there was no other party, and it does not come through here.")
	g.P("func notNull(v []byte) []byte {")
	g.P("	if v == nil {")
	g.P("		return []byte{}")
	g.P("	}")
	g.P("")
	g.P("	return v")
	g.P("}")
	g.P("")
}

// emitViaAbsent guards the first hop of a `via` path against not being there.
//
// The edge may be nullable, and payday asks for that: a schema gains field 3
// after it already has rows, and a required edge could never be added to one.
// So a row that names no next hop is a real row, and it has no tenant.
//
// Read without this guard, `GetId()` on the absent edge answers no bytes and
// parsing them fails -- which made the **write** fail, with an Internal, from
// inside the recorder. The row could not be created at all.
//
// The answer is the identifier that means "nothing to file it under", which the
// caller already handles by falling back to the actor's own tenant. The row's
// bytes go back either way: the trail can still say what happened even when it
// cannot say whose.
func emitViaAbsent(g *protogen.GeneratedFile, via string) {
	g.P("		if !row.Has", camel(via), "() {")
	g.P("			return ", pkgUuid.Ident("Nil"), "()", ", b, nil")
	g.P("		}")
	g.P("")
}

// emitSubject writes the read that answers, for a row that just changed, which
// tenant it belonged to and what it looked like.
//
// # Why the recorder reads at all
//
// The trail is filed under the tenant of the **thing that changed**, so that
// whoever holds that thing can read what was done to it. `bare.Change` cannot
// carry that: it is the ORM generator's type and the ORM generator has never
// heard of a tenant. payday's recorder can, because payday generated it and
// knows every entity's path to one.
//
// Which entity it is comes from the identifier, not from the method name. A
// `pdid` carries its domain, so one switch answers it for every entity there
// will ever be -- and it is the same fact `object_id` is stored for.
//
// # And what it costs
//
// Nothing for the row of an Add, a Patch or an Apply: the write hands it over
// (see [recorder.Snapshots] in what this emits), an Add's INSERT having read
// it back in the same statement where the dialect has RETURNING. What is read
// here is an Erase's row, which no write hands over, and a second row for an
// entity that reaches its tenant through another one. That is the price of a
// trail two parties can read, and it is paid on the write path rather than on
// the read path.
//
// It was a read for every write, which on Rove's import was nine statements of
// every row's thirty-seven (lesomnus/payday#84).
//
// A soft Erase is one read more -- the bare Get misses the row it has just
// stamped and the erased-inclusive read follows it -- and a soft Erase of an
// entity that reaches its tenant through another row is one more again. An
// ordinary write pays neither, since the retry is behind a NotFound and a live
// row's Get does not miss; the one that does pay is the write whose `via`
// parent is already erased, and it pays on the hop rather than on itself.
//
// # The Erase that took the row with it
//
// An entity erased hard has no row left by the time this runs -- the recorder
// is called inside the transaction, after the delete. Then this answers with
// the nil identifier and the caller falls back to the actor's tenant, which is
// what the trail said for everything before this existed.
//
// An entity erased softly has its row, and for a while this said it "answers
// normally". It did not: every read the bare server makes narrows to the rows
// still here, so the row a soft Erase had just stamped was NotFound to the
// record of that very erase, and the record took the hard path -- the actor's
// tenant, an empty value -- which is precisely the row the erased side cannot
// read. So a NotFound for a softly erased entity is retried past the erased
// filter; see [emitErased] for the seam, and for the one that was rejected.
func emitSubject(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	g.P("// subject is the tenant of the row `key` names and the row itself, and the")
	g.P("// nil identifier when there is no such row any more.")
	g.P("//")
	g.P("// The row is `image` when the write handed one over -- see")
	g.P("// [recorder.Snapshots] -- and is read when it did not. It is answered with")
	g.P("// only when `keep` says the trail keeps it, which is the `value` content")
	g.P("// level, and then without what the entity declared `secret` or `personal`;")
	g.P("// otherwise the bytes are empty, and the row is read for its tenant alone.")
	g.P("func subject(ctx ", pkgCtx.Ident("Context"), ", s ", p.Bare.Ident("Server"),
		", key ", pkgPdid.Ident("Id"), ", image ", pkgProto.Ident("Message"), ", keep bool) (", pkgUuid.Ident("UUID"), ", []byte, error) {")
	g.P("	switch key.Domain() {")

	for _, v := range s.Sorted() {
		if v.IsGlobal {
			// Not behind the wall, so there is no tenant to file it under.
			continue
		}
		if !v.Serves() {
			// Written by hand, and never recorded, so never asked about.
			continue
		}

		g.P("	case ", v.GoName(), "Domain:")
		// The row the write handed over is the one a Get here would answer
		// with: read in the write's own transaction, just after it, its edges
		// as keys -- by the INSERT itself, for an Add. Taking it is a statement
		// fewer for every write, and on an import of a few thousand rows that
		// was a few thousand round trips.
		g.P("		row, handed := image.(*", root.Ident(v.GoName()), ")")
		if !v.Rpcs().HasGet() {
			// Served with no Get, so a row that was not handed over is not
			// one this can read, and is filed as a global one is.
			g.P("		if !handed {")
			g.P("			return ", pkgUuid.Ident("Nil"), "()", ", []byte{}, nil")
			g.P("		}")
		} else {
			g.P("		if !handed {")
			g.P("			var err error")
			g.P("			row, err = s.", v.GoName(), "().Get(ctx, ", root.Ident(v.GoName()+"GetRequest_builder"), "{")
			g.P("				Ref: ", root.Ident(v.GoName()+"Ref_builder"), "{Id: key.Bytes()}.Build(),")
			g.P("			}.Build())")
			if !v.IsHard {
				// Keyed off the entity's shape and not the action, because the
				// action answers less. This server has no scope and runs inside
				// the write's own transaction, on rows that write just touched,
				// so for a soft entity a NotFound has one meaning: erased --
				// stamped by the Erase this record is for, or sitting on the path
				// a still-live row reaches its tenant through. The second is a
				// write recorded through an erased parent, whose trail row
				// belongs to that parent's tenant no less than an Erase's does,
				// and no test of the action would have caught it.
				g.P("			// Erased softly is still a row; see [erased", v.GoName(), "].")
				g.P("			if ", pkgStatus.Ident("Code"), "(err) == ", pkgCodes.Ident("NotFound"), " {")
				g.P("				row, err = erased", v.GoName(), "(ctx, s, key)")
				g.P("			}")
			}
			g.P("			if err != nil {")
			g.P("				if ", pkgStatus.Ident("Code"), "(err) == ", pkgCodes.Ident("NotFound"), " {")
			// Empty rather than nil: the trail's `value` is NOT NULL, and a nil
			// `[]byte` is SQL NULL to pgx while the SQLite driver makes it an empty
			// blob. A row that is really gone is the one case that reaches here --
			// erased hard, for a soft entity nothing at all -- and it is exactly
			// the case no SQLite test can fail on.
			g.P("					return ", pkgUuid.Ident("Nil"), "()", ", []byte{}, nil")
			g.P("				}")
			g.P("")
			g.P("				return ", pkgUuid.Ident("Nil"), "()", ", nil, err")
			g.P("			}")
			g.P("		}")
		}
		g.P("")
		// What the entity declared the trail never holds a value of, cleared
		// before it holds a copy: a `secret:`, which the layer that does this
		// on the way out of a read clears too, and a `personal:`, which that
		// layer leaves for whoever may read the row (#92).
		//
		// The layer that clears a secret on the way out is in front of the sink
		// and this is behind it -- the recorder reads the bare server on
		// purpose, so that a row is recorded as it was written rather than as
		// somebody was allowed to see it. Which is right for every column but
		// these: a verifier written into `value` is a second copy of it, in the
		// one table nothing erases, reachable by anybody who may read the trail.
		// Found by writing it: an argon2id hash sat in the trail of a deployment
		// whose `CredentialService` is unregistered and closed precisely so that
		// it could not be read. A person's address is the same copy, of a value
		// that is theirs to have corrected and erased.
		//
		// A row `Get` just answered with is nobody else's, and is cleared in
		// place. One the write handed over is every recorder's -- a `Recorders`
		// hands each of them the same one -- and is cleared on a copy, or the
		// next recorder would be told of a row without the column it wrote.
		//
		// And none of it unless the trail keeps the row, which is the `value`
		// content level: at the others the row is read for its tenant and
		// nothing of it is kept, so there is nothing to copy or to clear.
		g.P("		b := []byte{}")
		g.P("		if keep {")
		if len(v.Secrets)+len(v.Personal) > 0 {
			g.P("			if handed {")
			g.P("				row = ", pkgProto.Ident("Clone"), "(row).(*", root.Ident(v.GoName()), ")")
			g.P("			}")
			g.P("			_, secret, personal := described(key)")
			g.P("			", pkgAudit.Ident("Clear"), "(row, secret)")
			g.P("			", pkgAudit.Ident("Clear"), "(row, personal)")
			g.P("")
		}
		g.P("			v, err := ", pkgProto.Ident("Marshal"), "(row)")
		g.P("			if err != nil {")
		g.P("				return ", pkgUuid.Ident("Nil"), "()", ", nil, err")
		g.P("			}")
		g.P("			b = v")
		g.P("		}")
		g.P("")

		switch {
		case v.IsTenant:
			// A tenant is its own, which is what a tenant being a wall comes
			// down to.
			g.P("		k, err := ", pkgEntUuid.Ident("FromBytes"), "(row.GetId())")

		case len(v.Columns) > 0:
			// A row that names its tenant with a column. The trail is the only
			// one, and it is never the subject of a write anybody records.
			g.P("		k, err := ", pkgEntUuid.Ident("FromBytes"), "(row.Get", camel(v.Columns[0]), "())")

		case len(v.Via) == 1:
			emitViaAbsent(g, v.Via[0])
			g.P("		k, err := ", pkgEntUuid.Ident("FromBytes"), "(row.Get", camel(v.Via[0]), "().GetId())")

		default:
			// It reaches the tenant through another row, so the walk is a
			// second read -- and a third, if the schema ever declares one that
			// far away.
			emitViaAbsent(g, v.Via[0])
			g.P("		up, err := ", pkgPdid.Ident("From"), "(row.Get", camel(v.Via[0]), "().GetId())")
			g.P("		if err != nil {")
			g.P("			return ", pkgUuid.Ident("Nil"), "()", ", nil, err")
			g.P("		}")
			g.P("")
			g.P("		k, _, err := subject(ctx, s, up, nil, false)")
			g.P("		if err != nil {")
			g.P("			return ", pkgUuid.Ident("Nil"), "()", ", nil, err")
			g.P("		}")
			g.P("")
			g.P("		return k, b, nil")
		}

		if len(v.Via) <= 1 {
			g.P("		if err != nil {")
			g.P("			return ", pkgUuid.Ident("Nil"), "()", ", nil, err")
			g.P("		}")
			g.P("")
			g.P("		return k, b, nil")
		}

		g.P("")
	}

	g.P("	}")
	g.P("")
	g.P("	// An entity with no tenant to file under: one declared `global: {}`,")
	g.P("	// which the switch above skips, or a domain nothing registered, which")
	g.P("	// is an identifier from somewhere else. Nothing to read either way.")
	//
	// Empty rather than nil, for the reason the NotFound branch above says: a
	// nil `[]byte` is SQL NULL to pgx and the trail's `value` is NOT NULL, so
	// nil here is every write to a global entity failing on PostgreSQL and
	// passing on SQLite. It is the same trap twice, which is why it is worth
	// saying twice.
	g.P("	return ", pkgUuid.Ident("Nil"), "()", ", []byte{}, nil")
	g.P("}")
	g.P("")

	for _, v := range s.Sorted() {
		if v.IsGlobal || v.IsHard {
			// Nothing behind the wall to file, or no row left to read; the
			// caller's fallback is the whole of the answer for both.
			continue
		}

		emitErased(g, v, p, root)
	}
}

// emitErased writes the read [emitSubject]'s switch falls back to when the
// bare one answers NotFound: the same row, among the ones already erased.
//
// # Why it reads ent rather than the bare server
//
// Every reference the bare server builds carries the erased filter. That is
// upstream's code and upstream's rule, and the rule is right everywhere else:
// an erased row is not a row a read answers with. The recorder is the one
// caller for whom it is wrong -- the row it asks about was erased by the very
// write it is recording -- and the bare API has no words for "and the erased
// ones"; growing some for a single caller would put erased rows within reach
// of every server built on it. So the recorder steps around the bare server to
// ent, on the same transaction, through the Select the bare Get itself uses --
// and keeps the erased filter, inverted: this read is only for rows the bare
// one cannot see, so between the two every row is found exactly once, and a
// row found by neither was erased hard.
//
// # Why not the pre-image
//
// The rejected seam was to read the row before the erase stamps it and hand
// the copy down to the recorder. That read has nowhere honest to live: the pd
// layers run before the erase's transaction exists, so the copy can be stale
// by the time the stamp lands -- and an erase made below them, by a batch or
// by an app calling the sink, would still file the broken row. Reading back
// inside the transaction costs the stamp being in `value`, and that is not a
// cost: the value's contract is the row as the event left it, and a soft
// erase leaves the row stamped.
func emitErased(g *protogen.GeneratedFile, v *Entity, p Paths, root protogen.GoImportPath) {
	e := v.GoName()
	entPkg := p.Ent + "/" + protogen.GoImportPath(v.EntPkg())

	g.P("// erased", e, " is the row `key` names among the rows already erased, which no")
	g.P("// bare read answers: erasure is part of every reference that server builds.")
	g.P("// The recorder is the one caller that has to see past it -- the row it asks")
	g.P("// about was erased by the very write it is recording, and a trail row built")
	g.P("// blind was filed under the actor's tenant with an empty value, which the")
	g.P("// tenant whose row was erased could not read.")
	g.P("func erased", e, "(ctx ", pkgCtx.Ident("Context"), ", s ", p.Bare.Ident("Server"),
		", key ", pkgPdid.Ident("Id"), ") (*", root.Ident(e), ", error) {")
	g.P("	k, err := ", pkgEntUuid.Ident("FromBytes"), "(key.Bytes())")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	q := s.Db.", e, ".Query().Where(", entPkg.Ident("IdEQ"), "(k), ",
		entPkg.Ident(pascal(v.GetErasedField().Name())+"NotNil"), "())")
	g.P("	", p.Bare.Ident(e+"SelectInit"), "(q, nil)")
	g.P("")
	g.P("	v, err := q.Only(ctx)")
	g.P("	if err != nil {")
	g.P("		if ", p.Ent.Ident("IsNotFound"), "(err) {")
	g.P("			return nil, ", pkgStatus.Ident("Error"), "(", pkgCodes.Ident("NotFound"), ", \"", e, " not found\")")
	g.P("		}")
	g.P("")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return v.Proto(), nil")
	g.P("}")
	g.P("")
}

// EmitSecret writes the layer that keeps a declared secret off the wire.
//
// # What it is for
//
// A password hash and an API key hash are written and never answered with, and
// until `(payday.field).secret` existed there was nowhere to say so. Apps said
// it at **registration** instead -- leaving the generated service off the
// server -- which works, is checkable, and is said in the app that happens to
// hold the field rather than beside the field. So the next app storing a
// verifier rediscovered the argument.
//
// # Why a layer rather than a smaller message
//
// The clean answer is that the field is not in the response type at all, and
// that is not payday's to do: the entity message and its `Select` are written
// by the ORM's generators, from the same schema, upstream of anything here.
//
// A layer is what payday can write, and what it buys is the same in practice --
// nothing that goes out of this server carries the value. What it does not buy
// is the field being *visibly* absent to somebody reading the generated types,
// which is why this layer's presence is worth a line in an app's stack:
//
//	app.Build(walled, core.Build(), pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild())
//
// # It is not a permission
//
// There is no caller for whom this is answered, so there is no argument to it
// and no way to ask. A permission would imply a level at which handing out a
// verifier becomes right.
func EmitSecret(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	var vs []*Entity
	for _, v := range s.Entities {
		// One that declared a secret and is served: a secret no server answers
		// with is nothing for a layer to clear.
		if len(v.Secrets) > 0 && v.Serves() {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		// Nothing declared one, so there is no layer -- rather than an empty
		// one an app would stack for no reason.
		return
	}

	g.P("// Secret is the layer that keeps a declared secret off the wire.")
	g.P("//")
	g.P("// Every field marked `(payday.field).secret` is cleared on the way out:")
	g.P("// from what an `Add` echoes, what a `Get` answers, and every item of a")
	g.P("// `List` or an `AddMany`. The write side is untouched, because writing is")
	g.P("// the half that works.")
	g.P("//")
	g.P("//	app.Build(walled, core.Build(), pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild())")
	g.P("//")
	g.P("// It clears rather than refuses. A caller asking for a column that is not")
	g.P("// answered has not done anything wrong -- `Select{All: true}` is the")
	g.P("// ordinary way to ask for a row -- and an error there would make the")
	g.P("// common call the failing one.")
	g.P("type Secret struct {")
	g.P("	", root.Ident("Overlay"))
	g.P("}")
	g.P("")
	g.P("func NewSecret(next ", root.Ident("Server"), ") Secret {")
	g.P("	return Secret{", root.Ident("NewOverlay"), "(next)}")
	g.P("}")
	g.P("")
	g.P("var _ ", root.Ident("Server"), " = Secret{}")
	g.P("")
	g.P("// WithDriver answers with this stack running on `drv`.")
	g.P("//")
	g.P("// Every layer writes this and none can inherit it: an overlay holds what")
	g.P("// is behind it and cannot make itself again.")
	g.P("func (s Secret) WithDriver(drv ", pkgDialect.Ident("Driver"), ") (", root.Ident("Server"), ", error) {")
	g.P("	next, err := ", pkgEnttx.Ident("Rebind"), "(s.Next(), drv)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return NewSecret(next), nil")
	g.P("}")
	g.P("")
	g.P("func SecretBuild() ", root.Ident("Builder"), " { return secretBuilder{} }")
	g.P("")
	g.P("type secretBuilder struct{}")
	g.P("")
	g.P("func (secretBuilder) Build(next ", root.Ident("Server"), ") (", root.Ident("Server"), ", error) {")
	g.P("	return NewSecret(next), nil")
	g.P("}")
	g.P("")

	for _, v := range vs {
		emitSecretOf(g, v, root)
	}
}

// emitSecretOf writes the wrapper for one entity that declared a secret.
func emitSecretOf(g *protogen.GeneratedFile, e *Entity, root protogen.GoImportPath) {
	name := e.GoName()
	lower := "secret" + name

	g.P("func (s Secret) ", name, "() ", root.Ident(name+"ServiceServer"), " {")
	g.P("	return ", lower, "{s, s.Next().", name, "()}")
	g.P("}")
	g.P("")
	g.P("type ", lower, " struct {")
	g.P("	Secret")
	g.P("	", root.Ident(name+"ServiceServer"))
	g.P("}")
	g.P("")

	// The four that answer with the entity, of the ones it has.
	for _, m := range []struct {
		rpc, req string
		has      bool
	}{
		{"Add", name + "AddRequest", e.Rpcs().HasAdd()},
		{"Get", name + "GetRequest", e.Rpcs().HasGet()},
		{"Patch", name + "PatchRequest", e.Rpcs().HasPatch()},
		{"Apply", name + "ApplyRequest", e.Rpcs().HasPatch()},
	} {
		if !m.has {
			continue
		}

		g.P("func (s ", lower, ") ", m.rpc, "(ctx ", pkgCtx.Ident("Context"),
			", req *", root.Ident(m.req), ") (*", root.Ident(name), ", error) {")
		g.P("	v, err := s.", name, "ServiceServer.", m.rpc, "(ctx, req)")
		g.P("")
		g.P("	return hide", name, "(v), err")
		g.P("}")
		g.P("")
	}

	// And every row an AddMany answers with, for an entity that has one.
	if e.Rpcs().HasAddMany() {
		g.P("func (s ", lower, ") AddMany(ctx ", pkgCtx.Ident("Context"),
			", req *", root.Ident(name+"AddManyRequest"), ") (*", root.Ident(name+"AddManyResponse"), ", error) {")
		g.P("	v, err := s.", name, "ServiceServer.AddMany(ctx, req)")
		g.P("	for _, w := range v.GetItems() {")
		g.P("		hide", name, "(w)")
		g.P("	}")
		g.P("")
		g.P("	return v, err")
		g.P("}")
		g.P("")
	}

	// Only if there is one. `List` was emitted unconditionally, which named
	// `<E>ListRequest` and `s.<E>ServiceServer.List` for an entity that declared
	// no list -- `pd gen` said nothing and `go build` failed inside a generated
	// file nobody is allowed to edit.
	if e.List != nil {
		g.P("func (s ", lower, ") List(ctx ", pkgCtx.Ident("Context"),
			", req *", root.Ident(name+"ListRequest"), ") (*", root.Ident(name+"ListResponse"), ", error) {")
		g.P("	v, err := s.", name, "ServiceServer.List(ctx, req)")
		g.P("	if v == nil {")
		g.P("		return v, err")
		g.P("	}")
		g.P("")
		g.P("	for _, w := range v.GetItems() {")
		g.P("		hide", name, "(w)")
		g.P("	}")
		g.P("")
		g.P("	return v, err")
		g.P("}")
		g.P("")
	}

	// And the stream, which is the one read that cannot be narrowed by asking.
	//
	// A `WatchRequest` has no `select`, so an item carries the whole message and
	// a caller has no way to leave a column out. Without this wrapper a
	// watchable entity that declared a secret **streamed the secret** -- to
	// anybody the wall let read a row, on the one read where nothing had to ask
	// for it.
	//
	// # Why it is hidden and not refused
	//
	// Refusing `watch:` on an entity with a `secret:` field was the other fix,
	// and it is louder. It is also wrong, and the reason is what a watch is
	// *for*: the first real subject of a sync channel is **this person's
	// credentials changed, stop trusting what you were told**, and the row that
	// changed is exactly the one holding a verifier. Refusing would make the
	// one thing worth watching the one thing that cannot be watched.
	//
	// So it is hidden, which is what every other read of the same row already
	// does. A stream that omits a column is not a surprise when `Get` omits it
	// too, and the column is one the entity declared it never answers with.
	if e.Watch {
		g.P("func (s ", lower, ") Watch(req *", root.Ident(name+"WatchRequest"),
			", out ", pkgGrpc.Ident("ServerStreamingServer"), "[", root.Ident(name+"WatchResponse"), "]) error {")
		g.P("	return s.", name, "ServiceServer.Watch(req, ", lower, "Stream{out})")
		g.P("}")
		g.P("")

		g.P("// ", lower, "Stream clears the secret on its way out.")
		g.P("//")
		g.P("// It wraps the stream rather than the response because the server below")
		g.P("// sends whenever it has something, and there is no single place a")
		g.P("// message passes through on the way back -- which is the difference")
		g.P("// between a stream and a call.")
		g.P("type ", lower, "Stream struct {")
		g.P("	", pkgGrpc.Ident("ServerStreamingServer"), "[", root.Ident(name+"WatchResponse"), "]")
		g.P("}")
		g.P("")
		g.P("func (s ", lower, "Stream) Send(v *", root.Ident(name+"WatchResponse"), ") error {")
		g.P("	for _, w := range v.GetItems() {")
		g.P("		hide", name, "(w.GetValue())")
		g.P("	}")
		g.P("")
		g.P("	return s.ServerStreamingServer.Send(v)")
		g.P("}")
		g.P("")
	}

	ns := make([]string, 0, len(e.SecretNumbers))
	for _, n := range e.SecretNumbers {
		ns = append(ns, strconv.FormatUint(uint64(n), 10))
	}

	g.P("// hide", name, " clears what this entity declared it never answers with.")
	g.P("//")
	g.P("// A nil row passes through, because an error is answered with one and the")
	g.P("// caller of this is handing both on.")
	g.P("//")
	g.P("// By field number and through the descriptor, rather than a setter per")
	g.P("// field, because a setter's argument has a type and this has to hold for")
	g.P("// every field a row can carry. A `Set<F>(nil)` per secret is what this")
	g.P("// was, and it compiled only while every secret anybody had declared was")
	g.P("// `bytes`: a `string` one ended the build in a generated file, and an")
	g.P("// enum would have needed its own type spelled out here to say zero.")
	g.P("//")
	g.P("// Clear is also the truer word. A field with presence is **absent**")
	g.P("// afterwards rather than present and empty, which is what \"never answered")
	g.P("// with\" says; one without presence reads as its zero value either way.")
	g.P("// And the numbers are the ones `hidden` filters the trail's patch by, so")
	g.P("// the two cannot come to disagree about which fields they are.")
	g.P("func hide", name, "(v *", root.Ident(name), ") *", root.Ident(name), " {")
	g.P("	if v == nil {")
	g.P("		return nil")
	g.P("	}")
	g.P("")
	g.P("	m := v.ProtoReflect()")
	g.P("	fs := m.Descriptor().Fields()")
	g.P("	for _, n := range []", pkgReflect.Ident("FieldNumber"), "{", strings.Join(ns, ", "), "} {")
	g.P("		m.Clear(fs.ByNumber(n))")
	g.P("	}")
	g.P("")
	g.P("	return v")
	g.P("}")
	g.P("")
}
