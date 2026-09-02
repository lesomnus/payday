package grpcx

import (
	"context"
	"log/slog"
	"runtime/debug"

	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Recover keeps a panicking handler from ending the process. gRPC does not
// recover on its own, so without it a single bad request takes every other
// connection down with it.
func Recover() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(RecoverUnary()),
		grpc.ChainStreamInterceptor(RecoverStream()),
	}
}

func RecoverUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (v any, err error) {
		defer func() {
			if p := recover(); p != nil {
				v, err = nil, recovered(ctx, info.FullMethod, p)
			}
		}()

		return handler(ctx, req)
	}
}

func RecoverStream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = recovered(ss.Context(), info.FullMethod, p)
			}
		}()

		return handler(srv, ss)
	}
}

// recovered reports the panic and returns what is sent to the client, which
// tells nothing about it; the stack is for the log only.
func recovered(ctx context.Context, method string, p any) error {
	log.From(ctx).ErrorContext(ctx, "panic",
		slog.String("grpc.method", method),
		slog.Any("panic", p),
		slog.String("stack", string(debug.Stack())),
	)

	return status.Error(codes.Internal, "internal error")
}
