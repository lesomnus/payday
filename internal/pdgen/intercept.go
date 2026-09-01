package pdgen

import (
	"google.golang.org/protobuf/compiler/protogen"
)

// interceptRpc is one RPC the layer wraps.
type interceptRpc struct {
	// Entity is the accessor on `Server`, e.g. "Robot".
	Entity string
	// Rpc is the method, e.g. "Add".
	Rpc string
	// Const is the generated full-method constant, e.g.
	// "RobotService_Add_FullMethodName".
	Const string
	// In and Out are the request and answer messages' Go names. For a stream,
	// Out is what the stream sends.
	In, Out string
	// Stream says which of the two interceptors runs it.
	Stream bool
}

// interceptRpcs is every RPC of every entity service, taken from the contracts
// rather than from a list, so an RPC an overlay adds is wrapped without
// anybody coming back here.
//
// This is [batchOps] over again with the streaming ones kept: a batch has no
// shape for a stream, and a layer does.
func interceptRpcs(s *Schema, files []*protogen.File) []interceptRpc {
	by := map[string]*Entity{}
	for _, v := range s.Sorted() {
		by[v.GoName()+"Service"] = v
	}

	var vs []interceptRpc
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
				// A client stream has no single request to hand an
				// interceptor and payday generates none; skipping it here
				// rather than emitting something that would not compile is
				// what says so.
				if m.Desc.IsStreamingClient() {
					continue
				}

				vs = append(vs, interceptRpc{
					Entity: e.GoName(),
					Rpc:    m.GoName,
					Const:  svc.GoName + "_" + m.GoName + "_FullMethodName",
					In:     m.Input.GoIdent.GoName,
					Out:    m.Output.GoIdent.GoName,
					Stream: m.Desc.IsStreamingServer(),
				})
			}
		}
	}

	return vs
}

// EmitIntercept writes the layer that runs gRPC interceptors between two
// layers of the stack.
//
// # Why it is generated rather than written
//
// Because a layer is an interface with a method per entity and a method per
// RPC under that, so an interceptor written by hand is one wrapper per RPC of
// every entity, and the RPC nobody wrapped is the one that is not intercepted.
// It is the same reason the wall is generated: what is missing compiles.
//
// # What it sees, which is not what a gRPC interceptor sees
//
// A gRPC interceptor runs once per call that arrived on the wire. This one
// runs once per call **that crosses this seam**, and layers call each other:
// the gate reads a tenant through the wall before it lets an `Add` through,
// and the recorder reads back the row it has just written. Both are ordinary
// method calls on the next server, so a layer stacked beneath either of them
// sees them, and they carry the method name of the read rather than of the
// call that provoked it.
//
// That is the seam behaving as a seam rather than a fault. It is written down
// because an interceptor that counts calls, or authorises them, will count and
// authorise reads no client asked for -- and where it is stacked is what
// decides whether it does. Above the gate it sees what the caller asked for;
// below, it sees what the app did about it.
func EmitIntercept(g *protogen.GeneratedFile, s *Schema, p Paths, root protogen.GoImportPath, files []*protogen.File) {
	vs := interceptRpcs(s, files)
	if len(vs) == 0 {
		return
	}

	g.P("// Intercept is the layer that runs gRPC interceptors between two layers.")
	g.P("//")
	g.P("// They are `grpc.UnaryServerInterceptor` and `grpc.StreamServerInterceptor`,")
	g.P("// the same values a deployment hands `grpc.NewServer`, so one written for")
	g.P("// the wire runs here without being written twice:")
	g.P("//")
	g.P("//	app.Build(walled, core.Build(), pd.AuditBuild(),")
	g.P("//		pd.InterceptBuild([]grpc.UnaryServerInterceptor{mine}, nil),")
	g.P("//		pd.GateBuild())")
	g.P("//")
	g.P("// **What it sees is every call that crosses this seam**, which is not the")
	g.P("// same as every call that arrived on the wire: layers call each other, so")
	g.P("// a layer stacked beneath the gate sees the tenant read the gate does")
	g.P("// before it admits an `Add`, under that read's own method name. Where it")
	g.P("// is stacked is what decides whether it sees what the caller asked for or")
	g.P("// what the app did about it.")
	g.P("//")
	g.P("// `info.Server` is the next server rather than the one gRPC registered,")
	g.P("// because that is what this call is actually being made on. `FullMethod`")
	g.P("// is the same constant the wire uses.")
	g.P("type Intercept struct {")
	g.P("	", root.Ident("Overlay"))
	g.P("")
	g.P("	unary  ", pkgGrpc.Ident("UnaryServerInterceptor"))
	g.P("	stream ", pkgGrpc.Ident("StreamServerInterceptor"))
	g.P("}")
	g.P("")

	g.P("// NewIntercept puts `unary` and `stream` in front of `next`. Either may be")
	g.P("// empty, and a call of the kind nothing was given for goes straight")
	g.P("// through -- no boxing, no chain, nothing to pay for a layer that has")
	g.P("// nothing to say about it.")
	g.P("//")
	g.P("// Several of one kind run outermost-first, the way `grpc.ChainUnaryInterceptor`")
	g.P("// orders them.")
	g.P("func NewIntercept(next ", root.Ident("Server"), ", unary []", pkgGrpc.Ident("UnaryServerInterceptor"),
		", stream []", pkgGrpc.Ident("StreamServerInterceptor"), ") Intercept {")
	g.P("	return Intercept{")
	g.P("		Overlay: ", root.Ident("NewOverlay"), "(next),")
	g.P("		unary:   chainUnary(unary),")
	g.P("		stream:  chainStream(stream),")
	g.P("	}")
	g.P("}")
	g.P("")

	g.P("// InterceptBuild makes a builder of this layer so that it can be stacked.")
	g.P("//")
	g.P("// With nothing to run it builds nothing: the stack is `next` itself, so a")
	g.P("// deployment that assembles its interceptors from configuration and ends")
	g.P("// up with none pays for no layer.")
	g.P("func InterceptBuild(unary []", pkgGrpc.Ident("UnaryServerInterceptor"),
		", stream []", pkgGrpc.Ident("StreamServerInterceptor"), ") ", root.Ident("Builder"), " {")
	g.P("	return ", root.Ident("BuilderFunc"), "(func(next ", root.Ident("Server"), ") (", root.Ident("Server"), ", error) {")
	g.P("		if len(unary) == 0 && len(stream) == 0 {")
	g.P("			return next, nil")
	g.P("		}")
	g.P("")
	g.P("		return NewIntercept(next, unary, stream), nil")
	g.P("	})")
	g.P("}")
	g.P("")

	g.P("var _ ", root.Ident("Server"), " = Intercept{}")
	g.P("")
	g.P("var _ ", pkgEnttx.Ident("Binder"), "[", root.Ident("Server"), "] = Intercept{}")
	g.P("")
	g.P("// WithDriver answers with this stack running on `drv`.")
	g.P("//")
	g.P("// Every layer writes this and none can inherit it: an overlay holds what")
	g.P("// is behind it and cannot make itself again. The interceptors are carried")
	g.P("// across as they are -- a transaction is the same stack on another")
	g.P("// connection, not another stack.")
	g.P("func (s Intercept) WithDriver(drv ", pkgDialect.Ident("Driver"), ") (", root.Ident("Server"), ", error) {")
	g.P("	next, err := ", pkgEnttx.Ident("Rebind"), "(s.Next(), drv)")
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	return Intercept{Overlay: ", root.Ident("NewOverlay"), "(next), unary: s.unary, stream: s.stream}, nil")
	g.P("}")
	g.P("")

	emitChain(g)

	// One wrapper per entity, holding every RPC of its service.
	by := map[string][]interceptRpc{}
	var order []string
	for _, v := range vs {
		if _, ok := by[v.Entity]; !ok {
			order = append(order, v.Entity)
		}

		by[v.Entity] = append(by[v.Entity], v)
	}

	for _, name := range order {
		emitInterceptOf(g, name, by[name], root)
	}
}

// emitChain writes the two folds.
//
// grpc-go chains its own with a server option rather than a function anybody
// can call, so this is that fold: one value, applied outermost-first, and nil
// for none so that the call sites can test it rather than call through a
// wrapper that does nothing.
func emitChain(g *protogen.GeneratedFile) {
	g.P("// chainUnary folds interceptors into one, outermost first, and answers nil")
	g.P("// for none.")
	g.P("func chainUnary(vs []", pkgGrpc.Ident("UnaryServerInterceptor"), ") ", pkgGrpc.Ident("UnaryServerInterceptor"), " {")
	g.P("	switch len(vs) {")
	g.P("	case 0:")
	g.P("		return nil")
	g.P("	case 1:")
	g.P("		return vs[0]")
	g.P("	}")
	g.P("")
	g.P("	return func(ctx ", pkgCtx.Ident("Context"), ", req any, info *", pkgGrpc.Ident("UnaryServerInfo"),
		", handler ", pkgGrpc.Ident("UnaryHandler"), ") (any, error) {")
	g.P("		next := handler")
	g.P("		for i := len(vs) - 1; i >= 0; i-- {")
	g.P("			v, inner := vs[i], next")
	g.P("			next = func(ctx ", pkgCtx.Ident("Context"), ", req any) (any, error) {")
	g.P("				return v(ctx, req, info, inner)")
	g.P("			}")
	g.P("		}")
	g.P("")
	g.P("		return next(ctx, req)")
	g.P("	}")
	g.P("}")
	g.P("")

	g.P("// chainStream is [chainUnary] for the other kind.")
	g.P("func chainStream(vs []", pkgGrpc.Ident("StreamServerInterceptor"), ") ", pkgGrpc.Ident("StreamServerInterceptor"), " {")
	g.P("	switch len(vs) {")
	g.P("	case 0:")
	g.P("		return nil")
	g.P("	case 1:")
	g.P("		return vs[0]")
	g.P("	}")
	g.P("")
	g.P("	return func(srv any, ss ", pkgGrpc.Ident("ServerStream"), ", info *", pkgGrpc.Ident("StreamServerInfo"),
		", handler ", pkgGrpc.Ident("StreamHandler"), ") error {")
	g.P("		next := handler")
	g.P("		for i := len(vs) - 1; i >= 0; i-- {")
	g.P("			v, inner := vs[i], next")
	g.P("			next = func(srv any, ss ", pkgGrpc.Ident("ServerStream"), ") error {")
	g.P("				return v(srv, ss, info, inner)")
	g.P("			}")
	g.P("		}")
	g.P("")
	g.P("		return next(srv, ss)")
	g.P("	}")
	g.P("}")
	g.P("")
}

// emitInterceptOf writes the wrapper for one entity's service.
func emitInterceptOf(g *protogen.GeneratedFile, name string, vs []interceptRpc, root protogen.GoImportPath) {
	lower := "intercept" + name

	g.P("func (s Intercept) ", name, "() ", root.Ident(name+"ServiceServer"), " {")
	g.P("	return ", lower, "{s, s.Next().", name, "()}")
	g.P("}")
	g.P("")
	g.P("type ", lower, " struct {")
	g.P("	Intercept")
	g.P("	", root.Ident(name+"ServiceServer"))
	g.P("}")
	g.P("")

	for _, v := range vs {
		if v.Stream {
			emitInterceptStream(g, lower, name, v, root)
			continue
		}

		emitInterceptUnary(g, lower, name, v, root)
	}
}

// emitInterceptUnary writes one call that goes through the unary interceptor.
func emitInterceptUnary(g *protogen.GeneratedFile, lower, name string, v interceptRpc, root protogen.GoImportPath) {
	g.P("func (s ", lower, ") ", v.Rpc, "(ctx ", pkgCtx.Ident("Context"),
		", req *", root.Ident(v.In), ") (*", root.Ident(v.Out), ", error) {")
	g.P("	if s.unary == nil {")
	g.P("		return s.", name, "ServiceServer.", v.Rpc, "(ctx, req)")
	g.P("	}")
	g.P("")
	g.P("	v, err := s.unary(ctx, req, &", pkgGrpc.Ident("UnaryServerInfo"), "{")
	g.P("		Server:     s.", name, "ServiceServer,")
	g.P("		FullMethod: ", root.Ident(v.Const), ",")
	g.P("	}, func(ctx ", pkgCtx.Ident("Context"), ", req any) (any, error) {")
	g.P("		return s.", name, "ServiceServer.", v.Rpc, "(ctx, req.(*", root.Ident(v.In), "))")
	g.P("	})")
	// An interceptor may answer nil for both -- a cache that decided there was
	// nothing to say -- and the assertion below would panic on it, in generated
	// code, from a line the app did not write.
	g.P("	if err != nil {")
	g.P("		return nil, err")
	g.P("	}")
	g.P("")
	g.P("	w, _ := v.(*", root.Ident(v.Out), ")")
	g.P("")
	g.P("	return w, nil")
	g.P("}")
	g.P("")
}

// emitInterceptStream writes one call that goes through the stream
// interceptor.
//
// The stream is handed on as a [grpc.ServerStream] and taken back as one,
// because that is what an interceptor is written against and it is free to
// wrap it -- which is most of what a stream interceptor is for. So what
// reaches the handler is the wrapped one, adapted the way grpc-go's own
// generated code adapts it rather than asserted back to the typed stream it
// no longer is.
func emitInterceptStream(g *protogen.GeneratedFile, lower, name string, v interceptRpc, root protogen.GoImportPath) {
	g.P("func (s ", lower, ") ", v.Rpc, "(req *", root.Ident(v.In),
		", out ", pkgGrpc.Ident("ServerStreamingServer"), "[", root.Ident(v.Out), "]) error {")
	g.P("	if s.stream == nil {")
	g.P("		return s.", name, "ServiceServer.", v.Rpc, "(req, out)")
	g.P("	}")
	g.P("")
	g.P("	return s.stream(s.", name, "ServiceServer, out, &", pkgGrpc.Ident("StreamServerInfo"), "{")
	g.P("		FullMethod:     ", root.Ident(v.Const), ",")
	g.P("		IsServerStream: true,")
	g.P("	}, func(srv any, ss ", pkgGrpc.Ident("ServerStream"), ") error {")
	g.P("		return s.", name, "ServiceServer.", v.Rpc, "(req, &", pkgGrpc.Ident("GenericServerStream"),
		"[", root.Ident(v.In), ", ", root.Ident(v.Out), "]{ServerStream: ss})")
	g.P("	})")
	g.P("}")
	g.P("")
}
