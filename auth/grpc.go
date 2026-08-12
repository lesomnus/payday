package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

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
		// A credential's expiry needs nothing here. Every call carries one and
		// is read again, so an expired one is refused by whatever answers
		// [TokenStore.Lookup] -- which is where an expiry is known and where
		// revocation is too.
		ctx, _, err := of(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// ErrExpired is a stream that outlived the credential it was opened with.
//
// Unauthenticated, and that is the answer that makes the recovery work: a
// client told this goes and gets another credential and opens the stream again,
// and a fresh Watch begins with everything that matches now. So an expiry
// recovers down the same road a disconnection does, which is a road that had to
// exist anyway.
var ErrExpired = errors.New("the credential this stream was opened with has expired")

// InterceptorStream is [InterceptorUnary] for a stream, plus the one thing a
// stream needs and a call does not.
//
// # Why a stream is cut
//
// A call carries its credential every time, so an expiry is enforced by not
// being renewed: the next call is refused. A stream carries one **once**, at
// the handshake, and is then served for as long as it stays open -- which for a
// Watch is until somebody hangs up. Without this, a token with a ten-minute
// life is a stream that runs for a week.
//
// It matters more with a browser than it looks. Over HTTP/2 a credential rides
// in per-call metadata; over a WebSocket the browser puts the cookie on the
// handshake and never again, so **identity is per connection rather than per
// call** and there is no next call to refuse.
//
// Of the two ways to handle that -- cut the connection, or re-check
// periodically -- this cuts. `Watch` is already built so that a client which
// was cut off reconnects and is sent what matches now, so an expiry travels a
// road that exists. Re-checking would be a second road to the same place.
func InterceptorStream(h Handler, r Resolver, public Public) grpc.StreamServerInterceptor {
	of := authenticate(h, r, public)

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, id, err := of(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}

		if !id.Expires.IsZero() {
			// Cancelled with a cause rather than given a deadline, so that a
			// stream cut for this is told apart from one that ran out of the
			// time its caller asked for. The two want different answers: get
			// another credential, or ask for longer.
			var cancel context.CancelCauseFunc
			ctx, cancel = context.WithCancelCause(ctx)
			defer cancel(nil)

			t := time.AfterFunc(time.Until(id.Expires), func() { cancel(ErrExpired) })
			defer t.Stop()
		}

		err = handler(srv, grpcx.StreamWithContext(ss, ctx))
		if errors.Is(context.Cause(ctx), ErrExpired) {
			return status.Error(codes.Unauthenticated, ErrExpired.Error())
		}

		return err
	}
}

// authenticate is what both shapes of [Interceptor] do: the context a call is
// served with, once whoever is calling has been worked out.
//
// A call that already has a frame is left alone. That is a call that did not
// come in over the wire - one server calling another in the same process - and
// it was vouched for when it did come in.
func authenticate(h Handler, r Resolver, public Public) func(ctx context.Context, method string) (context.Context, Identity, error) {
	if public == nil {
		public = func(string) bool { return false }
	}

	return func(ctx context.Context, method string) (context.Context, Identity, error) {
		if _, ok := frame.From(ctx); ok {
			return ctx, Identity{}, nil
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
					return nil, Identity{}, status.Error(codes.Internal, "the resolver answered with neither a frame nor an error")
				}

				// A credential that said which tenant holds the actor is
				// checked against the tenant that does, and this is the only
				// place both are in hand: a handler has the claim and has read
				// nothing, a resolver has the row and was asked a different
				// question. See [Identity.TenantId].
				//
				// Refused rather than narrowed to what the credential said.
				// Disagreeing about who holds somebody is not an attenuation --
				// there is no smaller answer that is still true -- and one of
				// the two is wrong in a way that wants looking at.
				if id.TenantId != "" && id.TenantId != f.Tenant.String() {
					log.From(ctx).WarnContext(ctx, "credential names the wrong tenant",
						slog.String("auth.method", id.Method),
						slog.String("actor.id", f.Actor.String()),
						slog.String("actor.tenant", f.Tenant.String()),
						slog.String("credential.tenant", id.TenantId),
					)

					return nil, Identity{}, status.Error(codes.Unauthenticated,
						"this credential names a tenant that does not hold the actor it names")
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
				//
				// **Info, and it was Debug.** Every way of failing here is
				// already `Warn` or `Error`, so at Debug a deployment kept
				// every refusal and no success -- which is backwards for the
				// question this line exists to answer. What an investigation
				// asks first is who actually got in, and a level most
				// deployments turn off is not where that belongs.
				//
				// It is a log and not an audit row, and the difference is
				// delivery rather than the word: a trail row is bound to the
				// transaction it describes, and there is no transaction here
				// because nothing changed. What this needs instead is to
				// survive production and reach somewhere its subject cannot
				// edit -- neither of which this package can arrange, and both
				// of which a deployment must. See `docs/guide/permissions.md`.
				log.From(ctx).InfoContext(ctx, "authenticated",
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
					return nil, Identity{}, status.Errorf(codes.PermissionDenied,
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

				return frame.Into(ctx, &v), id, nil
			}

			// Fall through: somebody said something and it did not name anyone
			// who is here, which is a bad credential and not a missing one.
			if !errors.Is(err, ErrNoCredential) {
				return nil, Identity{}, resolveFailed(ctx, err)
			}
		} else if !errors.Is(err, ErrNoCredential) {
			return nil, Identity{}, statusOf(err)
		}

		if public(method) {
			return ctx, Identity{}, nil
		}

		return nil, Identity{}, status.Error(codes.Unauthenticated, "who is asking?")
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
