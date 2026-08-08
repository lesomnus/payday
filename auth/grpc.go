package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/grpcx"
)

// Public reports whether a method is served without asking who is calling.
type Public func(method string) bool

// PublicDefault is what is answered to anyone: whether the server is up, and
// what it offers. Neither says anything about what is in it.
func PublicDefault(method string) bool {
	return strings.HasPrefix(method, "/grpc.health.v1.Health/") ||
		strings.HasPrefix(method, "/grpc.reflection.")
}

// Interceptor works out who is calling and puts it in the frame of the
// request, so that everything behind it can ask rather than work it out again.
//
// It is the two shapes and the option pair, as [grpcx.Recover] and
// [grpcx.Closed] are: a server takes options, and a [grpcx.Chain] -- what the
// wasm server and a batch of calls inside one call are built from -- takes the
// interceptors themselves.
func Interceptor(h Handler, r Resolver, public Public) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(InterceptorUnary(h, r, public)),
		grpc.ChainStreamInterceptor(InterceptorStream(h, r, public)),
	}
}

func InterceptorUnary(h Handler, r Resolver, public Public) grpc.UnaryServerInterceptor {
	of := authenticate(h, r, public)

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := of(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

func InterceptorStream(h Handler, r Resolver, public Public) grpc.StreamServerInterceptor {
	of := authenticate(h, r, public)

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := of(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}

		return handler(srv, grpcx.StreamWithContext(ss, ctx))
	}
}

// authenticate is what both shapes of [Interceptor] do: the context a call is
// served with, once whoever is calling has been worked out.
//
// A call that already has a frame is left alone. That is a call that did not
// come in over the wire - one server calling another in the same process - and
// it was vouched for when it did come in.
func authenticate(h Handler, r Resolver, public Public) func(ctx context.Context, method string) (context.Context, error) {
	if public == nil {
		public = func(string) bool { return false }
	}

	return func(ctx context.Context, method string) (context.Context, error) {
		if _, ok := frame.From(ctx); ok {
			return ctx, nil
		}

		id, err := h.Handle(ctx)
		if err == nil {
			f, err := r.Resolve(ctx, id)
			if err == nil {
				if f == nil {
					// A resolver that answered with nothing and no error. A
					// nil frame in the context reads as a frame that is there
					// and says nothing, which is worse than none: everything
					// behind it stops asking whether there is one.
					return nil, status.Error(codes.Internal, "the resolver answered with neither a frame nor an error")
				}

				// Who called what, for every RPC there is -- the reads that
				// leave no other trace included. Which RPC is not said here:
				// `grpcx.Log` puts the service and the method on the logger
				// every line of a call is written with, so this one carries
				// them without asking, as does everything a handler writes.
				//
				// The identifiers are the ones that came back, and not the
				// name the caller wrote: what is worth recording is who the
				// request was served as.
				log.From(ctx).DebugContext(ctx, "authenticated",
					slog.String("auth.method", id.Method),
					slog.String("actor.id", f.Actor.String()),
					slog.String("actor.tenant", f.Tenant.String()),
				)

				// What the credential allows is checked here, once, and not
				// by whatever is about to run. It is not a rule about the
				// caller -- the wall holds those, and this narrows whatever
				// it decides -- it is the credential saying it was not made
				// for this, which is a question about the request and not
				// about the row it is going to touch.
				if !id.Grant.Allows(method) {
					return nil, status.Errorf(codes.PermissionDenied,
						"%s: this credential is not for that", method)
				}

				// The grant is put on here rather than taken from the frame
				// the resolver built. What a credential allows was read out
				// of the credential, and a resolver was asked who is calling
				// -- so one that answers [frame.Whole] because that is what
				// its own rows say cannot widen a token, and one that says
				// nothing cannot break a good one either.
				v := *f
				v.Grant = id.Grant

				return frame.Into(ctx, &v), nil
			}

			// Fall through: somebody said something and it did not name anyone
			// who is here, which is a bad credential and not a missing one.
			if !errors.Is(err, ErrNoCredential) {
				return nil, resolveFailed(ctx, err)
			}
		} else if !errors.Is(err, ErrNoCredential) {
			return nil, statusOf(err)
		}

		if public(method) {
			return ctx, nil
		}

		return nil, status.Error(codes.Unauthenticated, "who is asking?")
	}
}

// statusOf answers a handler's refusal with the code that says what the caller
// should do about it.
//
// Unauthenticated means "that credential is no good", and a caller who is told
// it throws the credential away and goes to get another one. A handler that
// could not reach the thing it asks must not say that: it would send every
// caller at once to an issuer that is, by assumption, already having a bad
// day, and each of them would discard a token that was never wrong.
func statusOf(err error) error {
	if s, ok := status.FromError(err); ok {
		return s.Err()
	}
	if errors.Is(err, ErrUnavailable) {
		return status.Error(codes.Unavailable, err.Error())
	}

	return status.Error(codes.Unauthenticated, err.Error())
}

// resolveFailed answers a [Resolver]'s refusal, which is not the same question
// [statusOf] answers.
//
// A handler that refuses is talking about the credential, so Unauthenticated
// is right when it says nothing more. A resolver that refuses is a query that
// went wrong, and the credential may be perfectly good -- so what it does not
// explain is Internal, and the caller is not sent to fetch a token they
// already have. The form this came from handed the error back untouched, which
// made it Unknown and gave the caller whatever the app's query had said.
//
// What is not passed on is written to the log instead, since the only reader
// who can do anything about it is the one running this.
func resolveFailed(ctx context.Context, err error) error {
	if s, ok := status.FromError(err); ok {
		return s.Err()
	}
	if errors.Is(err, ErrUnavailable) {
		return status.Error(codes.Unavailable, err.Error())
	}

	log.From(ctx).ErrorContext(ctx, "could not resolve who is calling", slog.Any("error", err))

	return status.Error(codes.Internal, "could not say who is calling")
}

// Inject says who the caller is on every outgoing call.
func Inject(p Provider) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(func(ctx context.Context, method string, req, res any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoke(p.Provide(ctx), method, req, res, cc, opts...)
		}),
		grpc.WithChainStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, stream grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return stream(p.Provide(ctx), desc, cc, method, opts...)
		}),
	}
}
