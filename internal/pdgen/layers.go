package pdgen

import (
	"strconv"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/pdpb"
)

const (
	pkgGate    = protogen.GoImportPath("github.com/lesomnus/payday/gate")
	pkgAudit   = protogen.GoImportPath("github.com/lesomnus/payday/audit")
	pkgEnttx   = protogen.GoImportPath("github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx")
	pkgDialect = protogen.GoImportPath("entgo.io/ent/dialect")
	pkgEmpty   = protogen.GoImportPath("google.golang.org/protobuf/types/known/emptypb")
	pkgProto   = protogen.GoImportPath("google.golang.org/protobuf/proto")
	pkgPatchpb = protogen.GoImportPath("github.com/lesomnus/protobuf-patch/patchpb")
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
	g.P("}")
	g.P("")
	g.P("func NewGate(next ", root.Ident("Server"), ") Gate {")
	g.P("	return Gate{", root.Ident("NewOverlay"), "(next)}")
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
	g.P("	return NewGate(next), nil")
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
	g.P("func (s Gate) Tenant() ", root.Ident("TenantServiceServer"), " {")
	g.P("	return gateTenant{s, s.Next().Tenant()}")
	g.P("}")
	g.P("")
	g.P("// Add is not served. A tenant is put up by whoever runs the deployment,")
	g.P("// through a server this layer is not in front of.")
	g.P("func (s gateTenant) Add(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident("TenantAddRequest"),
		") (*", root.Ident("Tenant"), ", error) {")
	g.P("	return nil, ", pkgGate.Ident("ErrDeployment"), "(\"put up\")")
	g.P("}")
	g.P("")
	g.P("// Erase is not served either, and it would take everything in the tenant")
	g.P("// with it.")
	g.P("func (s gateTenant) Erase(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident("TenantRef"),
		") (*", pkgEmpty.Ident("Empty"), ", error) {")
	g.P("	return nil, ", pkgGate.Ident("ErrDeployment"), "(\"taken down\")")
	g.P("}")
	g.P("")

	// Holder: the one rule about a holder that is not a predicate.
	g.P("type gateHolder struct {")
	g.P("	Gate")
	g.P("	", root.Ident("HolderServiceServer"))
	g.P("}")
	g.P("")
	g.P("func (s Gate) Holder() ", root.Ident("HolderServiceServer"), " {")
	g.P("	return gateHolder{s, s.Next().Holder()}")
	g.P("}")
	g.P("")
	g.P("// Add is the one thing about a holder this layer still says, and it is here")
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
	g.P("func (s gateHolder) Add(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident("HolderAddRequest"),
		") (*", root.Ident("Holder"), ", error) {")
	g.P("	if _, err := ", pkgGate.Ident("Actor"), "(ctx); err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	if _, err := s.Gate.Next().Tenant().Get(ctx, ", root.Ident("TenantGetRequest_builder"), "{")
	g.P("		Ref: req.GetTenant(),")
	g.P("	}.Build()); err != nil {")
	g.P("		if ", pkgStatus.Ident("Code"), "(err) == ", pkgCodes.Ident("NotFound"), " {")
	g.P("			return nil, ", pkgGate.Ident("ErrNotFound"), "(\"Tenant\")")
	g.P("		}")
	g.P("")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return s.HolderServiceServer.Add(ctx, req)")
	g.P("}")
	g.P("")

	emitAdmit(g, s, root)
}

// emitAdmit writes, for every other entity behind the wall, the check
// `gateHolder.Add` has always made for payday's own.
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
// on `gateHolder.Add` for why a comparison against the scope is not enough.
//
// It is exactly the wall's own invariant and nothing more: a row belongs to a
// tenant the caller may see. An edge pointing at some *other* row in another
// tenant is a different question -- referential, not tenancy -- and is not
// asked here.
//
// # What it costs
//
// One read per `Add`. Adds are not the path a server spends its time on, and
// the alternative is a rule that holds for reads and not for writes.
func emitAdmit(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath) {
	for _, v := range s.Sorted() {
		// The tenant is refused outright above, the holder has its own written
		// out, and an entity that is not behind the wall has nothing to check.
		// An entity that names its tenant with a column (`field:`) is refused a
		// hand-written row by the audit layer.
		if v.IsTenant || v.IsGlobal || len(v.Via) == 0 || v.Own == pdpb.Own_OWN_HOLDER {
			continue
		}

		hop := v.Via[0]
		e, ok := edge(v.Entity, hop)
		if !ok {
			continue
		}

		to, ok := s.of(e.Target().FullName())
		if !ok {
			continue
		}

		name := v.GoName()
		g.P("type gate", name, " struct {")
		g.P("	Gate")
		g.P("	", root.Ident(name+"ServiceServer"))
		g.P("}")
		g.P("")
		g.P("func (s Gate) ", name, "() ", root.Ident(name+"ServiceServer"), " {")
		g.P("	return gate", name, "{s, s.Next().", name, "()}")
		g.P("}")
		g.P("")

		g.P("// Add refuses a ", name, " put into a ", to.GoName(), " this caller cannot see.")
		g.P("//")
		g.P("// The wall is a predicate and an Add has no query, so without this the")
		g.P("// identifier in `", hop, "` becomes a foreign key with nothing consulted.")
		g.P("// The row is then invisible to whoever planted it and visible to whoever")
		g.P("// holds that ", to.GoName(), ", which is the shape of the bug rather than a")
		g.P("// mitigation of it.")
		g.P("//")
		g.P("// NotFound rather than a refusal, for the reason on `gateHolder.Add`:")
		g.P("// that a row exists is itself something a caller who may not see it")
		g.P("// should not be told.")
		g.P("func (s gate", name, ") Add(ctx ", pkgCtx.Ident("Context"), ", req *", root.Ident(name+"AddRequest"),
			") (*", root.Ident(name), ", error) {")
		seen(g, root, hop, to)

		// And the second axis, when the app declared one. It is an isolation
		// boundary the same way the tenant is -- payday says so by reading
		// field 3 as one -- so an Add into a set the caller cannot see is the
		// same bug one level down. An ordinary edge is **not** checked: that a
		// row points at another row somebody else holds is referential and not
		// tenancy, and asking it would be a read per edge on every write.
		if v.Set != "" && s.Set != nil {
			seen(g, root, v.Set, s.Set)
		}

		g.P("	return s.", name, "ServiceServer.Add(ctx, req)")
		g.P("}")
		g.P("")
	}
}

// seen writes the read that asks whether the caller may see what an edge names.
//
// Through the layer's own `Next()`, so it travels every scope the app installed
// -- the wall, and the second axis if there is one -- rather than comparing
// against one of them and missing the other.
func seen(g *protogen.GeneratedFile, root protogen.GoImportPath, edge string, to *Entity) {
	g.P("	if ref := req.Get", pascal(edge), "(); ref != nil {")
	g.P("		if _, err := s.Gate.Next().", to.GoName(), "().Get(ctx, ",
		root.Ident(to.GoName()+"GetRequest_builder"), "{")
	g.P("			Ref: ref,")
	g.P("		}.Build()); err != nil {")
	g.P("			if ", pkgStatus.Ident("Code"), "(err) == ", pkgCodes.Ident("NotFound"), " {")
	g.P("				return nil, ", pkgGate.Ident("ErrNotFound"), "(", strconv.Quote(to.GoName()), ")")
	g.P("			}")
	g.P("")
	g.P("			return nil, err")
	g.P("		}")
	g.P("	}")
	g.P("")
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
	g.P("// The RPCs exist because the trail is an entity like any other and a test is")
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
		") (*", pkgEmpty.Ident("Empty"), ", error) {")
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
	g.P("// servers make.")
	g.P("//")
	g.P("// It is not a layer and it does not override an RPC. The servers call it")
	g.P("// from inside the transaction that makes the write, so the row and the")
	g.P("// record of it hold or fall together -- and so every RPC that changes")
	g.P("// anything is on the trail without anybody having listed them.")
	g.P("//")
	g.P("//	sink, err := bare.NewServer(db, bare.WithRecorder(pd.Recorder()))")
	g.P("func Recorder() ", p.Bare.Ident("Recorder"), " { return recorder{} }")
	g.P("")
	g.P("type recorder struct{}")
	g.P("")
	g.P("var _ ", p.Bare.Ident("Recorder"), " = recorder{}")
	g.P("")
	g.P("// Record writes what happened. It runs inside the write's transaction, so an")
	g.P("// error here takes the write with it -- which is the answer wanted: a write")
	g.P("// that could not be accounted for did not happen.")
	g.P("//")
	g.P("// It writes through the server it was handed, which runs on that transaction")
	g.P("// and does not record; a recorder that recorded its own writes would not")
	g.P("// stop.")
	g.P("func (recorder) Record(ctx ", pkgCtx.Ident("Context"), ", s ", p.Bare.Ident("Server"),
		", c ", p.Bare.Ident("Change"), ") error {")
	g.P("	var patch ", pkgProto.Ident("Message"))
	g.P("	if c.Patch != nil {")
	g.P("		patch = c.Patch")
	g.P("	}")
	g.P("")
	g.P("	v, err := ", pkgAudit.Ident("Of"), "(ctx, c.Method, c.Key, patch)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("")
	g.P("	// The row this happened to, which is where the object's tenant and the")
	g.P("	// state after the write both come from. Absent for an Erase that took")
	g.P("	// the row with it; see [subject].")
	g.P("	tenant, value, err := subject(ctx, s, v.Object)")
	g.P("	if err != nil {")
	g.P("		return err")
	g.P("	}")
	g.P("	if tenant == ", pkgUuid.Ident("Nil"), " {")
	g.P("		// Nothing to file it under but the actor's own, which is what the")
	g.P("		// trail said for every row before this. It is the honest fallback")
	g.P("		// rather than a zero: a record nobody can read is not a record.")
	g.P("		tenant = ", pkgUuid.Ident("UUID"), "(v.Tenant)")
	g.P("	}")
	g.P("")
	g.P("	_, err = s.Audit().Add(ctx, ", root.Ident("AuditAddRequest_builder"), "{")
	g.P("		TenantId:      tenant[:],")
	g.P("		ActorTenantId: v.Tenant.Bytes(),")
	g.P("		ActorId:       v.Actor.Bytes(),")
	g.P("		TraceId:       v.Trace,")
	g.P("		Action:        v.Action,")
	g.P("		ObjectId:      v.Object.Bytes(),")
	g.P("		Patch:         v.Patch,")
	g.P("		Value:         value,")
	// Zero unless a layer said otherwise, which is nearly every write. It is
	// the other tenant this one was about, and the wall on the trail counts
	// it; see `audit.Concerning`.
	g.P("		CounterpartTenantId: v.Counterpart.Bytes(),")
	g.P("	}.Build())")
	g.P("")
	g.P("	return err")
	g.P("}")
	g.P("")

	emitSubject(g, s, p, root)

	// Referenced only so that the import is kept when a schema has no patch.
	g.P("var _ *", pkgPatchpb.Ident("Patch"))
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
// One read per write, and a second for an entity that reaches its tenant
// through another row. That is the price of a trail two parties can read, and
// it is paid on the write path rather than on the read path.
//
// # The Erase that took the row with it
//
// An entity erased hard has no row left by the time this runs -- the recorder
// is called inside the transaction, after the delete. Then this answers with
// the nil identifier and the caller falls back to the actor's tenant, which is
// what the trail said for everything before this existed. An entity erased
// softly has its row and answers normally, which is one more reason for soft to
// be what an entity does unless it says otherwise.
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
	g.P("			return ", pkgUuid.Ident("Nil"), ", b, nil")
	g.P("		}")
	g.P("")
}

func emitSubject(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath) {
	g.P("// subject is the tenant of the row `key` names and the row itself, and the")
	g.P("// nil identifier when there is no such row any more.")
	g.P("func subject(ctx ", pkgCtx.Ident("Context"), ", s ", p.Bare.Ident("Server"),
		", key ", pkgPdid.Ident("Id"), ") (", pkgUuid.Ident("UUID"), ", []byte, error) {")
	g.P("	switch key.Domain() {")

	for _, v := range s.Sorted() {
		if v.IsGlobal {
			// Not behind the wall, so there is no tenant to file it under.
			continue
		}

		g.P("	case ", v.GoName(), "Domain:")
		g.P("		row, err := s.", v.GoName(), "().Get(ctx, ", root.Ident(v.GoName()+"GetRequest_builder"), "{")
		g.P("			Ref: ", root.Ident(v.GoName()+"Ref_builder"), "{Id: key.Bytes()}.Build(),")
		g.P("		}.Build())")
		g.P("		if err != nil {")
		g.P("			if ", pkgStatus.Ident("Code"), "(err) == ", pkgCodes.Ident("NotFound"), " {")
		// Empty rather than nil: the trail's `value` is NOT NULL, and a nil
		// `[]byte` is SQL NULL to pgx while the SQLite driver makes it an empty
		// blob. A row that is already gone is the one case that reaches here,
		// and it is exactly the case no SQLite test can fail on.
		g.P("				return ", pkgUuid.Ident("Nil"), ", []byte{}, nil")
		g.P("			}")
		g.P("")
		g.P("			return ", pkgUuid.Ident("Nil"), ", nil, err")
		g.P("		}")
		g.P("")
		if len(v.Secrets) > 0 {
			// What the entity declared it never answers with, cleared before
			// the trail holds a copy.
			//
			// The layer that does this on the way out is in front of the sink
			// and this is behind it -- the recorder reads the bare server on
			// purpose, so that a row is recorded as it was written rather than
			// as somebody was allowed to see it. Which is right for every
			// column but these: a verifier written into `value` is a second
			// copy of it, in the one table nothing erases, reachable by anybody
			// who may read the trail.
			//
			// Found by writing it: an argon2id hash sat in the trail of a
			// deployment whose `CredentialService` is unregistered and closed
			// precisely so that it could not be read.
			//
			// The row is the one `Get` just answered with and nothing else
			// holds it, so clearing in place costs no copy.
			g.P("		hide", v.GoName(), "(row)")
			g.P("")
		}

		g.P("		b, err := ", pkgProto.Ident("Marshal"), "(row)")
		g.P("		if err != nil {")
		g.P("			return ", pkgUuid.Ident("Nil"), ", nil, err")
		g.P("		}")
		g.P("")

		switch {
		case v.IsTenant:
			// A tenant is its own, which is what a tenant being a wall comes
			// down to.
			g.P("		k, err := ", pkgUuid.Ident("FromBytes"), "(row.GetId())")

		case len(v.Columns) > 0:
			// A row that names its tenant with a column. The trail is the only
			// one, and it is never the subject of a write anybody records.
			g.P("		k, err := ", pkgUuid.Ident("FromBytes"), "(row.Get", camel(v.Columns[0]), "())")

		case len(v.Via) == 1:
			emitViaAbsent(g, v.Via[0])
			g.P("		k, err := ", pkgUuid.Ident("FromBytes"), "(row.Get", camel(v.Via[0]), "().GetId())")

		default:
			// It reaches the tenant through another row, so the walk is a
			// second read -- and a third, if the schema ever declares one that
			// far away.
			emitViaAbsent(g, v.Via[0])
			g.P("		up, err := ", pkgPdid.Ident("From"), "(row.Get", camel(v.Via[0]), "().GetId())")
			g.P("		if err != nil {")
			g.P("			return ", pkgUuid.Ident("Nil"), ", nil, err")
			g.P("		}")
			g.P("")
			g.P("		k, _, err := subject(ctx, s, up)")
			g.P("		if err != nil {")
			g.P("			return ", pkgUuid.Ident("Nil"), ", nil, err")
			g.P("		}")
			g.P("")
			g.P("		return k, b, nil")
		}

		if len(v.Via) <= 1 {
			g.P("		if err != nil {")
			g.P("			return ", pkgUuid.Ident("Nil"), ", nil, err")
			g.P("		}")
			g.P("")
			g.P("		return k, b, nil")
		}

		g.P("")
	}

	g.P("	}")
	g.P("")
	g.P("	// A domain nothing registered, which is an identifier from somewhere")
	g.P("	// else. Nothing to read and nothing to say about it.")
	g.P("	return ", pkgUuid.Ident("Nil"), ", nil, nil")
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
		if len(v.Secrets) > 0 {
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
	g.P("// `List`. The write side is untouched, because writing is the half that")
	g.P("// works.")
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

	// The four that answer with the entity.
	for _, m := range []struct{ rpc, req string }{
		{"Add", name + "AddRequest"},
		{"Get", name + "GetRequest"},
		{"Patch", name + "PatchRequest"},
		{"Apply", name + "ApplyRequest"},
	} {
		g.P("func (s ", lower, ") ", m.rpc, "(ctx ", pkgCtx.Ident("Context"),
			", req *", root.Ident(m.req), ") (*", root.Ident(name), ", error) {")
		g.P("	v, err := s.", name, "ServiceServer.", m.rpc, "(ctx, req)")
		g.P("")
		g.P("	return hide", name, "(v), err")
		g.P("}")
		g.P("")
	}

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

	g.P("// hide", name, " clears what this entity declared it never answers with.")
	g.P("//")
	g.P("// A nil row passes through, because an error is answered with one and the")
	g.P("// caller of this is handing both on.")
	g.P("func hide", name, "(v *", root.Ident(name), ") *", root.Ident(name), " {")
	g.P("	if v == nil {")
	g.P("		return nil")
	g.P("	}")
	g.P("")
	for _, f := range e.Secrets {
		g.P("	v.Set", camel(f), "(nil)")
	}
	g.P("")
	g.P("	return v")
	g.P("}")
	g.P("")
}
