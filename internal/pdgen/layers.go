package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"
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

// Own is the full name of an entity payday ships, for the emitters that only
// have something to say when an app took it.
const (
	OwnTenant = "payday.Tenant"
	OwnHolder = "payday.Holder"
	OwnAudit  = "payday.Audit"
)

// Has reports whether the schema holds the entity payday ships under that name.
func (s *Schema) Has(name string) bool {
	for _, v := range s.Entities {
		if string(v.FullName()) == name {
			return true
		}
	}

	return false
}

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
	if !s.Has(OwnTenant) || !s.Has(OwnHolder) {
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
	if !s.Has(OwnAudit) {
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

	emitRecorder(g, p, root)
}

// emitRecorder writes what turns a write into a line of the trail.
func emitRecorder(g *protogen.GeneratedFile, p Paths, root protogen.GoImportPath) {
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
	g.P("	_, err = s.Audit().Add(ctx, ", root.Ident("AuditAddRequest_builder"), "{")
	g.P("		TenantId: v.Tenant.Bytes(),")
	g.P("		ActorId:  v.Actor.Bytes(),")
	g.P("		TraceId:  v.Trace,")
	g.P("		Action:   v.Action,")
	g.P("		ObjectId: v.Object.Bytes(),")
	g.P("		Patch:    v.Patch,")
	g.P("	}.Build())")
	g.P("")
	g.P("	return err")
	g.P("}")
	g.P("")

	// Referenced only so that the import is kept when a schema has no patch.
	g.P("var _ *", pkgPatchpb.Ident("Patch"))
	g.P("")
}
