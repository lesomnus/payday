package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"
)

const (
	pkgBatch = protogen.GoImportPath("github.com/lesomnus/payday/batch")
	pkgPdpb  = protogen.GoImportPath("github.com/lesomnus/payday/pdpb")
	pkgAnypb = protogen.GoImportPath("google.golang.org/protobuf/types/known/anypb")
)

// EmitBatch writes the dispatcher: the switch from a method name to the Rpc of
// this app's servers that answers it.
//
// It is the one part of a batch that has to be generated, and it is the one
// part that is only mechanical. Everything with a judgement in it -- what a
// credential allows, what is closed, how often, what the policy says -- is in
// `payday/batch` as functions, because those are the same for every app and
// because a rule that is generated into an app is a rule an app can end up with
// an old copy of.
//
// What it dispatches is the entity services, which is what `Server` has. An RPC
// somebody wrote by hand lives on a service of its own and is not reachable
// through that interface, so it is not batchable -- said out loud because the
// alternative is somebody finding out from an Unimplemented.
func EmitBatch(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath, files []*protogen.File) {
	ops := batchOps(s, files)
	if len(ops) == 0 {
		return
	}

	g.P("// Batch answers with the server that runs several writes as one")
	g.P("// transaction.")
	g.P("//")
	g.P("// The guard is required and there is no default for it. Everything payday")
	g.P("// enforces by method name is enforced against `BatchService/Do` and not")
	g.P("// against what it carries, so a batch built without one is a way past all")
	g.P("// of it -- a caller whose credential allows one method wraps whatever they")
	g.P("// like in a batch, and the trail records that it was allowed.")
	g.P("//")
	g.P("//	b, err := pd.Batch(s.Walled, s.Drv, c.Server.Guard(policy))")
	g.P("//")
	g.P("// The guard comes from the configuration rather than being written out,")
	g.P("// which is what keeps it saying the same thing the interceptors say. Two")
	g.P("// places deciding what is closed will disagree, and the direction they")
	g.P("// disagree in is the one where the batch allows more.")
	g.P("//")
	g.P("// The driver is passed rather than taken from the server, because the")
	g.P("// transaction is begun on it and a `*ent.Client` does not hand out the")
	g.P("// driver it was built with. The app has one: it is what it built the client")
	g.P("// from.")
	g.P("func Batch(s ", root.Ident("Server"), ", drv ", pkgDialect.Ident("Driver"),
		", guard ", pkgBatch.Ident("Guard"), ") (", pkgPdpb.Ident("BatchServiceServer"), ", error) {")
	g.P("	if guard.IsZero() {")
	g.P("		// Nobody filled it in. It is the zero value and not a judgement about")
	g.P("		// how open the deployment is: `config.ServerConfig.Guard` always sets")
	g.P("		// how a caller is counted, so a deployment that closes nothing and")
	g.P("		// limits nothing still comes through here with something in it.")
	g.P("		//")
	g.P("		// Refused rather than served open, because served open is a privilege")
	g.P("		// escalation and the way it would be found is somebody reading the")
	g.P("		// wiring and noticing what is not there.")
	g.P("		return nil, ", pkgBatch.Ident("ErrNoGuard"))
	g.P("	}")
	g.P("")
	g.P("	return batchServer{s: s, drv: drv, guard: guard}, nil")
	g.P("}")
	g.P("")
	g.P("type batchServer struct {")
	g.P("	", pkgPdpb.Ident("UnimplementedBatchServiceServer"))
	g.P("")
	g.P("	s     ", root.Ident("Server"))
	g.P("	drv   ", pkgDialect.Ident("Driver"))
	g.P("	guard ", pkgBatch.Ident("Guard"))
	g.P("}")
	g.P("")

	g.P("// Do runs every operation in one transaction.")
	g.P("//")
	g.P("// The transaction is opened here and the whole stack is put on it, so the")
	g.P("// layers each operation goes through are the same objects with the same")
	g.P("// rules -- the wall still narrows, the trail is still written, the outbox")
	g.P("// still gets its row. What changes is only which driver they are talking")
	g.P("// to.")
	g.P("//")
	g.P("// Nothing is published until it commits. The watch recorder remembers into")
	g.P("// the context and the interceptor publishes once this handler has answered,")
	g.P("// which is after the commit -- so a batch arrives at a subscriber as one")
	g.P("// event holding every change, which is what it was.")
	g.P("func (b batchServer) Do(ctx ", pkgCtx.Ident("Context"), ", req *", pkgPdpb.Ident("BatchRequest"),
		") (*", pkgPdpb.Ident("BatchResponse"), ", error) {")
	g.P("	ops := req.GetOps()")
	g.P("	if err := b.guard.Check(len(ops)); err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")

	// Every rule is applied before anything is written. A batch whose eighth
	// operation is refused should not have run the first seven, and although
	// the transaction would undo them, the work and the locks are real.
	g.P("	// Every rule for every operation, before anything is written. The")
	g.P("	// transaction would undo the work of a batch refused halfway, but the")
	g.P("	// work and the locks it took are real -- and a rate limiter that had")
	g.P("	// already counted the first seven would charge for a batch that never")
	g.P("	// happened.")
	g.P("	cs := make([]", pkgCtx.Ident("Context"), ", len(ops))")
	g.P("	for i, op := range ops {")
	g.P("		c, err := b.guard.Allow(ctx, op.GetMethod())")
	g.P("		if err != nil {")
	g.P("			return nil, ", pkgBatch.Ident("Failed"), "(i, op.GetMethod(), err)")
	g.P("		}")
	g.P("")
	g.P("		cs[i] = c")
	g.P("	}")
	g.P("")
	g.P("	drv, tx, err := ", pkgEnttx.Ident("Begin"), "(ctx, b.drv)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	s, err := ", pkgEnttx.Ident("Rebind"), "(b.s, drv)")
	g.P("	if err != nil {")
	g.P("		tx.Rollback()")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	vs := make([]*", pkgAnypb.Ident("Any"), ", len(ops))")
	g.P("	for i, op := range ops {")
	g.P("		v, err := dispatch(cs[i], s, op)")
	g.P("		if err != nil {")
	g.P("			tx.Rollback()")
	g.P("			return nil, ", pkgBatch.Ident("Failed"), "(i, op.GetMethod(), err)")
	g.P("		}")
	g.P("")
	g.P("		vs[i] = v")
	g.P("	}")
	g.P("")
	g.P("	if err := tx.Commit(); err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return ", pkgPdpb.Ident("BatchResponse_builder"), "{Results: vs}.Build(), nil")
	g.P("}")
	g.P("")

	emitDispatch(g, ops, p, root)
}

// batchOp is one Rpc a batch can carry.
type batchOp struct {
	// Entity is the accessor on `Server`, e.g. "Robot".
	Entity string
	// RPC is the method, e.g. "Add".
	Rpc string
	// Const is the generated full-method constant, e.g.
	// "RobotService_Add_FullMethodName".
	Const string
	// In is the request message's Go name.
	In string
}

// batchOps is every Rpc of every entity service, taken from the contracts
// rather than from a list.
//
// It reads the service definitions, so an RPC payday adds to a contract -- a
// `List`, and whatever comes after it -- is batchable without anybody having
// come back here. What it leaves out is the streaming ones, which have no
// single request and no single answer and so are not an operation.
func batchOps(s *Schema, files []*protogen.File) []batchOp {
	// The entities, by the name of the service they are served under, so that a
	// service which is not one of theirs is skipped: `Server` has an accessor
	// per entity and nothing else.
	by := map[string]*Entity{}
	for _, v := range s.Sorted() {
		by[v.GoName()+"Service"] = v
	}

	var ops []batchOp
	for _, f := range files {
		if !f.Generate {
			continue
		}

		for _, svc := range f.Services {
			e, ok := by[svc.GoName]
			if !ok {
				continue
			}

			for _, m := range svc.Methods {
				if m.Desc.IsStreamingServer() || m.Desc.IsStreamingClient() {
					continue
				}

				ops = append(ops, batchOp{
					Entity: e.GoName(),
					Rpc:    m.GoName,
					Const:  svc.GoName + "_" + m.GoName + "_FullMethodName",
					In:     m.Input.GoIdent.GoName,
				})
			}
		}
	}

	return ops
}

func emitDispatch(g *protogen.GeneratedFile, ops []batchOp, p Paths, root protogen.GoImportPath) {
	g.P("// dispatch runs one operation against the stack the transaction is on.")
	g.P("//")
	g.P("// The request is unpacked into exactly the message the method takes, and an")
	g.P("// `Any` carrying anything else is refused rather than coerced -- a request")
	g.P("// that decoded into a different message would be a write the caller did not")
	g.P("// ask for, and `Any` is checked by type Url so there is a right answer.")
	g.P("func dispatch(ctx ", pkgCtx.Ident("Context"), ", s ", root.Ident("Server"),
		", op *", pkgPdpb.Ident("Op"), ") (*", pkgAnypb.Ident("Any"), ", error) {")
	g.P("	m := op.GetMethod()")
	g.P("")
	g.P("	// Under the operation's own name. The recorder fills in what the")
	g.P("	// caller asked for by asking gRPC, and in here gRPC answers with the")
	g.P("	// envelope -- `BatchService/Do`, for every operation of every batch --")
	g.P("	// so without this the trail says a hundred different writes were all")
	g.P("	// the same call.")
	g.P("	ctx = ", pkgBatch.Ident("AsOp"), "(ctx, m)")
	g.P("")
	g.P("	switch m {")

	for _, op := range ops {
		g.P("	case ", root.Ident(op.Const), ":")
		g.P("		v := &", root.Ident(op.In), "{}")
		g.P("		if err := op.GetRequest().UnmarshalTo(v); err != nil {")
		g.P("			return nil, ", pkgBatch.Ident("ErrRequest"), "(m, err)")
		g.P("		}")
		g.P("")
		g.P("		res, err := s.", op.Entity, "().", op.Rpc, "(ctx, v)")
		g.P("		if err != nil {")
		g.P("			return nil, err")
		g.P("		}")
		g.P("")
		g.P("		return ", pkgAnypb.Ident("New"), "(res)")
		g.P("")
	}

	g.P("	default:")
	g.P("		// Including every Rpc written by hand: those live on a service of")
	g.P("		// their own and `Server` has no accessor for one, so there is nothing")
	g.P("		// here to call. Unimplemented says exactly that.")
	g.P("		return nil, ", pkgBatch.Ident("ErrNoMethod"), "(m)")
	g.P("	}")
	g.P("}")
	g.P("")
}
