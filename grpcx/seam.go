package grpcx

import (
	"context"

	"google.golang.org/grpc"
)

// The seam between two layers of a generated stack, which is the third caller
// of the same interceptors [Chain] exists for.
//
// A layer is an interface with a typed method per RPC of every entity, and Go
// has no way to make one at run time -- no dynamic proxy, and a type parameter
// declares no methods. So the wrappers are generated, one per RPC, and what is
// generated is a line: everything that does not depend on the two message
// types is here, written once and shared by every RPC of every app.
//
// What that leaves in the generated file is the part that cannot be anywhere
// else -- the signature, which is the whole reason a wrapper exists.

// RunUnary runs `icp` around one call between two layers.
//
// `I` and `O` are the request and answer as the method takes and returns them,
// which is to say pointers. That is not a detail of taste: Go stencils a
// generic function per GC shape, every pointer is one shape, and so all of an
// app's RPCs share one instantiation of this. Taking the messages by value
// instead would compile just as well and would put a copy of this in the
// binary for each of them.
//
// A nil `icp` is the ordinary case rather than a mistake -- a stack given only
// stream interceptors has one -- and it costs a comparison, no boxing and no
// closure.
func RunUnary[I, O any](
	ctx context.Context,
	icp grpc.UnaryServerInterceptor,
	srv any,
	method string,
	req I,
	next func(context.Context, I) (O, error),
) (O, error) {
	if icp == nil {
		return next(ctx, req)
	}

	v, err := icp(ctx, req, &grpc.UnaryServerInfo{Server: srv, FullMethod: method},
		func(ctx context.Context, req any) (any, error) {
			return next(ctx, req.(I))
		})
	if err != nil {
		var zero O

		return zero, err
	}

	// An interceptor may answer nil for both -- a cache that decided there was
	// nothing to say -- so this does not assert. A wrong type answered with no
	// error is the one case that reaches the caller as a nil row, and it is a
	// thing the interceptor did rather than something to panic in generated
	// code about.
	w, _ := v.(O)

	return w, nil
}

// RunStream is [RunUnary] for a server stream.
//
// The stream goes out as a [grpc.ServerStream] and comes back as one, because
// that is what an interceptor is written against and putting another in the
// way is most of what the kind is for. So what reaches the handler is rebuilt
// around whatever came back rather than asserted to the typed stream it may no
// longer be -- the same adaptation grpc-go's own generated code does.
//
// Here `I` and `O` are the messages rather than pointers to them, which is
// what [grpc.ServerStreamingServer] and [grpc.GenericServerStream] are written
// in terms of. So this does stencil per stream, and a schema has as many of
// those as it declared `watch:`.
func RunStream[I, O any](
	icp grpc.StreamServerInterceptor,
	srv any,
	method string,
	req *I,
	out grpc.ServerStreamingServer[O],
	next func(*I, grpc.ServerStreamingServer[O]) error,
) error {
	if icp == nil {
		return next(req, out)
	}

	return icp(srv, out, &grpc.StreamServerInfo{FullMethod: method, IsServerStream: true},
		func(srv any, ss grpc.ServerStream) error {
			return next(req, &grpc.GenericServerStream[I, O]{ServerStream: ss})
		})
}

// ChainUnary folds interceptors into one, outermost first, and answers nil for
// none so that a caller can test for it rather than call through something
// that does nothing.
//
// It is the fold grpc-go does behind [grpc.ChainUnaryInterceptor], which it
// does not export: a server option is no use to a caller that is not making a
// server.
func ChainUnary(vs []grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	switch len(vs) {
	case 0:
		return nil
	case 1:
		return vs[0]
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		next := handler
		for i := len(vs) - 1; i >= 0; i-- {
			v, inner := vs[i], next
			next = func(ctx context.Context, req any) (any, error) {
				return v(ctx, req, info, inner)
			}
		}

		return next(ctx, req)
	}
}

// ChainStream is [ChainUnary] for the other kind.
func ChainStream(vs []grpc.StreamServerInterceptor) grpc.StreamServerInterceptor {
	switch len(vs) {
	case 0:
		return nil
	case 1:
		return vs[0]
	}

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		next := handler
		for i := len(vs) - 1; i >= 0; i-- {
			v, inner := vs[i], next
			next = func(srv any, ss grpc.ServerStream) error {
				return v(srv, ss, info, inner)
			}
		}

		return next(srv, ss)
	}
}
