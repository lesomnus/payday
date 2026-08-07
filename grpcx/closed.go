package grpcx

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Closed refuses the methods `is` names before they reach a handler.
//
// It is a transport rule and not a layer of the server stack, and the
// difference is the whole point: what it closes is what a *caller* may ask
// for, and everything behind it goes on working. An RPC written by hand can
// still be implemented with the RPC that is closed -- which is exactly what
// the general write is for. See the README, "The general write is not an API".
func Closed(is func(method string) bool) []grpc.ServerOption {
	if is == nil {
		return nil
	}

	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(ClosedUnary(is)),
		grpc.ChainStreamInterceptor(ClosedStream(is)),
	}
}

func ClosedUnary(is func(method string) bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if is(info.FullMethod) {
			return nil, errClosed(info.FullMethod)
		}

		return handler(ctx, req)
	}
}

func ClosedStream(is func(method string) bool) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if is(info.FullMethod) {
			return errClosed(info.FullMethod)
		}

		return handler(srv, ss)
	}
}

// errClosed is Unimplemented rather than PermissionDenied because this is not
// about who is asking. Nobody may, and no credential changes it -- which is
// also what a caller reading the reflection listing should take from finding
// the method there.
func errClosed(method string) error {
	return status.Errorf(codes.Unimplemented, "%s is not served; it is how the servers write, not how a caller asks", method)
}

// GeneralWrite reports whether a method is one of the two general writes every
// generated service has.
//
// `Patch` takes a field per property and `Apply` takes a patch document, and
// between them they can write anything the schema has. That is what makes them
// useful to a server and wrong as an API: what a caller may change, and under
// what conditions, is not something a general write can be told.
func GeneralWrite(method string) bool {
	return strings.HasSuffix(method, "/Patch") || strings.HasSuffix(method, "/Apply")
}
