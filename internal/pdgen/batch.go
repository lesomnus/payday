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
	g.P("	// What an operation costs is the schema's to say -- an AddMany is as many")
	g.P("	// calls as its items -- and nothing the configuration knows. So a guard")
	g.P("	// that says nothing of it is given this app's ([Cost]).")
	g.P("	if guard.Cost == nil {")
	g.P("		guard.Cost = Cost")
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

	// What each operation asks for, read before any of it is let through:
	// its cost is read off its request, and a batch that writes more rows than
	// a transaction may is refused before anything is charged.
	g.P("	// What each operation asks for, as the message its method takes, and what")
	g.P("	// it costs -- an AddMany is as many rows as its items. A batch that asks")
	g.P("	// to write more rows than one transaction may is refused here, before")
	g.P("	// anything is charged or written: it is one transaction however many")
	g.P("	// operations it is said in.")
	g.P("	in := make([]", pkgProto.Ident("Message"), ", len(ops))")
	g.P("	costs := make([]int, len(ops))")
	g.P("	rows := 0")
	g.P("	for i, op := range ops {")
	g.P("		v, err := unpack(op)")
	g.P("		if err != nil {")
	g.P("			return nil, ", pkgBatch.Ident("Failed"), "(i, op.GetMethod(), err)")
	g.P("		}")
	g.P("")
	g.P("		in[i], costs[i] = v, b.guard.CostOf(op.GetMethod(), v)")
	g.P("		rows += costs[i]")
	g.P("	}")
	g.P("	if err := b.guard.CheckRows(rows); err != nil {")
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
	g.P("	// happened. Each is charged what it costs, as a call of it would be.")
	g.P("	cs := make([]", pkgCtx.Ident("Context"), ", len(ops))")
	g.P("	for i, op := range ops {")
	g.P("		c, err := b.guard.Allow(ctx, op.GetMethod(), costs[i])")
	g.P("		if err != nil {")
	g.P("			return nil, ", pkgBatch.Ident("Failed"), "(i, op.GetMethod(), err)")
	g.P("		}")
	g.P("")
	g.P("		cs[i] = c")
	g.P("	}")
	g.P("")
	g.P("	drv, tx, err := ", pkgDialect.Ident("BeginTx"), "(ctx, b.drv)")
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
	g.P("		v, err := dispatch(cs[i], s, op.GetMethod(), in[i])")
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
// single request and no single answer and so are not an operation, and the
// records payday keeps -- the trail, the outbox, the archive's account, the
// access record: what writes them is the servers, and what reads one is
// served as an RPC of its own or not at all, never as an operation a batch
// reaches by a name its app did not serve.
func batchOps(s *Schema, files []*protogen.File) []batchOp {
	// The entities, by the name of the service they are served under, so that a
	// service which is not one of theirs is skipped: `Server` has an accessor
	// per entity and nothing else.
	by := map[string]*Entity{}
	for _, v := range s.Sorted() {
		if v.Records() {
			continue
		}
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
	g.P("// unpack is the request an operation carries, as exactly the message its")
	g.P("// method takes.")
	g.P("//")
	g.P("// An `Any` carrying anything else is refused rather than coerced -- a")
	g.P("// request that decoded into a different message would be a write the")
	g.P("// caller did not ask for, and `Any` is checked by type Url so there is a")
	g.P("// right answer.")
	g.P("func unpack(op *", pkgPdpb.Ident("Op"), ") (", pkgProto.Ident("Message"), ", error) {")
	g.P("	m := op.GetMethod()")
	g.P("")
	g.P("	var v ", pkgProto.Ident("Message"))
	g.P("	switch m {")
	for _, op := range ops {
		g.P("	case ", root.Ident(op.Const), ":")
		g.P("		v = &", root.Ident(op.In), "{}")
	}
	g.P("	default:")
	g.P("		// Including every Rpc written by hand: those live on a service of")
	g.P("		// their own, which `Server` has no accessor for.")
	g.P("		return nil, ", pkgBatch.Ident("ErrNoMethod"), "(m)")
	g.P("	}")
	g.P("")
	g.P("	if err := op.GetRequest().UnmarshalTo(v); err != nil {")
	g.P("		return nil, ", pkgBatch.Ident("ErrRequest"), "(m, err)")
	g.P("	}")
	g.P("")
	g.P("	return v, nil")
	g.P("}")
	g.P("")
	g.P("// dispatch runs one operation, of method `m` asking `v`, against the stack")
	g.P("// the transaction is on. `v` is what [unpack] read for it.")
	g.P("func dispatch(ctx ", pkgCtx.Ident("Context"), ", s ", root.Ident("Server"),
		", m string, v ", pkgProto.Ident("Message"), ") (*", pkgAnypb.Ident("Any"), ", error) {")
	g.P("	// Under the operation's own name. The recorder fills in what the")
	g.P("	// caller asked for by asking gRPC, and in here gRPC answers with the")
	g.P("	// envelope -- `BatchService/Do`, for every operation of every batch --")
	g.P("	// so without this the trail says a hundred different writes were all")
	g.P("	// the same call.")
	g.P("	ctx = ", pkgBatch.Ident("AsOp"), "(ctx, m)")
	g.P("")
	g.P("	var (")
	g.P("		res ", pkgProto.Ident("Message"))
	g.P("		err error")
	g.P("	)")
	g.P("	switch m {")
	for _, op := range ops {
		g.P("	case ", root.Ident(op.Const), ":")
		g.P("		res, err = s.", op.Entity, "().", op.Rpc, "(ctx, v.(*", root.Ident(op.In), "))")
	}
	g.P("	default:")
	g.P("		return nil, ", pkgBatch.Ident("ErrNoMethod"), "(m)")
	g.P("	}")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return ", pkgAnypb.Ident("New"), "(res)")
	g.P("}")
	g.P("")
}

// EmitCost writes how many calls a request of this app counts as: one, but an
// AddMany is one for each of its items.
//
// An AddMany of a thousand rows is a thousand Adds' worth of work sent as one
// call, and the limit and a batch counted it as one -- a thousand calls' worth
// for the price of one, which is what counting a batch's operations one by one
// was there to stop. Which RPCs write many rows is the schema's to say, so the
// answer is generated: an RPC an entity asks for is weighed without anybody
// listing it.
func EmitCost(g *protogen.GeneratedFile, s *Schema, root protogen.GoImportPath) {
	g.P("// Cost is how many calls a request counts as: an AddMany is one for each of")
	g.P("// its items -- the Add each stands for -- and every other call is one.")
	g.P("//")
	g.P("// It is what the limit charges a call, and an operation of a batch, and")
	g.P("// what the rows a batch asks to write are counted by. [Batch] is given it")
	g.P("// when its guard says nothing; the interceptor is handed it:")
	g.P("//")
	g.P("//	WithUnary(grpcx.LimitUnary(c.Server.Limiter(), gate.ByTenant(), pd.Cost))")
	g.P("//")
	g.P("// An app that weighs RPCs of its own writes a Cost that asks this one for")
	g.P("// the rest.")
	g.P("func Cost(method string, req any) int {")
	many := []*Entity{}
	for _, v := range s.Sorted() {
		if v.Rpcs().HasAddMany() {
			many = append(many, v)
		}
	}
	if len(many) > 0 {
		g.P("	switch method {")
		for _, v := range many {
			name := v.GoName()
			g.P("	case ", root.Ident(name+"Service_AddMany_FullMethodName"), ":")
			g.P("		if v, ok := req.(*", root.Ident(name+"AddManyRequest"), "); ok {")
			g.P("			return max(len(v.GetItems()), 1)")
			g.P("		}")
		}
		g.P("	}")
		g.P("")
	}
	g.P("	return 1")
	g.P("}")
	g.P("")
	g.P("var _ ", pkgGrpcx.Ident("Cost"), " = Cost")
	g.P("")
}
