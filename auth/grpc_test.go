package auth_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
)

// The rows the fake resolver below knows about. A real one reads them out of
// the database; what a test needs is that they came from somewhere other than
// the caller.
var (
	acme      = pdid.New(Tenant)
	adminId   = pdid.New(Holder)
	hooli     = pdid.New(Tenant)
	erlichId  = pdid.New(Holder)
	getMethod = "/app.RobotService/Get"
)

// resolverOf answers with the frame of whoever an identity names, out of a
// table written here.
//
// This is the seam: looking an actor up is a query against the app's own
// servers, so payday supplies the interface and the app supplies the query.
// That it is this easy to stand in for is the point of the interface being the
// boundary -- a test of what the interceptor does needs no database, no
// generated server, and no Holder.
func resolverOf(vs map[string]*frame.Frame) auth.Resolver {
	return auth.ResolverFunc(func(_ context.Context, id auth.Identity) (*frame.Frame, error) {
		v, ok := vs[id.Name()]
		if !ok {
			return nil, fmt.Errorf("names nobody who is here: %w", auth.ErrNoCredential)
		}

		return v, nil
	})
}

// known is the resolver every test here uses unless it wants something else.
func known() auth.Resolver {
	return resolverOf(map[string]*frame.Frame{
		"@acme/admin":   frame.New(adminId, acme, frame.Whole()),
		"@hooli/erlich": frame.New(erlichId, hooli, frame.Whole()),
	})
}

// serve runs one call through the interceptor and answers with the context the
// handler was served with, which is what the interceptor is for.
func serve(h auth.Handler, r auth.Resolver, public auth.Public, ctx context.Context, method string) (context.Context, error) {
	var served context.Context
	_, err := auth.InterceptorUnary(h, r, public)(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, func(ctx context.Context, _ any) (any, error) {
		served = ctx
		return nil, nil
	})

	return served, err
}

func TestInterceptor(t *testing.T) {
	t.Run("the frame is what the resolver said, not what the caller said", func(t *testing.T) {
		x := require.New(t)

		ctx, err := serve(auth.Plain(), known(), nil, incoming("Plain @acme/admin"), getMethod)
		x.NoError(err)

		f, ok := frame.From(ctx)
		x.True(ok)
		x.Equal(adminId, f.Actor)
		x.Equal(acme, f.Tenant)
	})

	// A resolver answers who is calling. What that caller's credential allows
	// was read out of the credential, and this is where the two meet -- so a
	// resolver cannot widen a token by answering with the whole of what its
	// own rows say.
	t.Run("the grant on the frame is the credential's and not the resolver's", func(t *testing.T) {
		x := require.New(t)

		v := admin()
		v.Grant = frame.Whole().To(getMethod)

		s := auth.NewMemTokenStore()
		s.Add("read-only", v, time.Time{})

		said := frame.New(adminId, acme, frame.Whole())
		r := resolverOf(map[string]*frame.Frame{"@acme/admin": said})

		ctx, err := serve(auth.Bearer(s), r, nil, incoming("Bearer read-only"), getMethod)
		x.NoError(err)

		f, ok := frame.From(ctx)
		x.True(ok)
		x.False(f.Grant.IsWhole())
		x.False(f.Grant.Allows("/app.RobotService/Erase"))

		// And what the resolver handed over is left as it was, since a
		// resolver may well be answering out of something it keeps.
		x.True(said.Grant.IsWhole())
	})

	// Not a rule about the caller -- the wall holds those -- but the
	// credential saying it was not made for this.
	t.Run("a credential that was not made for this method is refused", func(t *testing.T) {
		x := require.New(t)

		v := admin()
		v.Grant = frame.Whole().To(getMethod)

		s := auth.NewMemTokenStore()
		s.Add("read-only", v, time.Time{})

		_, err := serve(auth.Bearer(s), known(), nil, incoming("Bearer read-only"), "/app.RobotService/Erase")
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("nobody asking is Unauthenticated, unless the method is public", func(t *testing.T) {
		x := require.New(t)

		_, err := serve(auth.Plain(), known(), auth.PublicDefault, incoming(), getMethod)
		x.Equal(codes.Unauthenticated, status.Code(err))

		// Whether the server is up says nothing about what is in it.
		ctx, err := serve(auth.Plain(), known(), auth.PublicDefault, incoming(), "/grpc.health.v1.Health/Check")
		x.NoError(err)
		_, ok := frame.From(ctx)
		x.False(ok, "a public call is served as nobody, not as somebody")
	})

	t.Run("nothing is public when nothing was said to be", func(t *testing.T) {
		x := require.New(t)

		_, err := serve(auth.Plain(), known(), nil, incoming(), "/grpc.health.v1.Health/Check")
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	// A slug refuses with InvalidArgument, which is right where a slug is a
	// field of a request and wrong here: a header that is not a name is a
	// credential that is not a credential, and what the caller has to do about
	// it is go and get one that is.
	t.Run("a name that is not one is Unauthenticated and not InvalidArgument", func(t *testing.T) {
		x := require.New(t)

		for _, v := range []string{"Plain @acme/", "Plain @Acme Corporation", "Plain not-a-name"} {
			_, err := serve(auth.Plain(), known(), nil, incoming(v), getMethod)
			x.Equal(codes.Unauthenticated, status.Code(err), "%q", v)

			// And the field path a slug carries does not come with it. A
			// violation is an instruction to a page to put a line under a box,
			// and there is no box: what was wrong is a header. This is the half
			// that would break silently -- an `errors.New(err.Error())` in
			// `parseSlug` turned into a `%w` reads like an improvement and takes
			// the code with it.
			x.Empty(pderr.Violations(err), "%q", v)
		}
	})

	t.Run("a credential naming nobody who is here is no better than none", func(t *testing.T) {
		x := require.New(t)

		_, err := serve(auth.Plain(), known(), nil, incoming("Plain @acme/nobody"), getMethod)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	// The whole reason for the third answer. Told Unauthenticated, a caller
	// throws away a token that is perfectly good and goes to get another one,
	// from the thing that is already down.
	t.Run("a store that is down is Unavailable and not Unauthenticated", func(t *testing.T) {
		x := require.New(t)

		down := auth.TokenStoreFunc(func(context.Context, string) (auth.Identity, error) {
			return auth.Identity{}, fmt.Errorf("dial tcp: %w", auth.ErrUnavailable)
		})

		_, err := serve(auth.Bearer(down), known(), auth.PublicDefault, incoming("Bearer s3cret"), getMethod)
		x.Equal(codes.Unavailable, status.Code(err))
	})

	// And it does not fall through either: somebody presented a credential,
	// and serving them as whatever the certificate says would answer a
	// question nobody asked, as somebody else.
	t.Run("a credential that is wrong does not become the next one", func(t *testing.T) {
		x := require.New(t)

		h := auth.Seq(auth.Bearer(auth.NewMemTokenStore()), auth.MTls())
		ctx := metadata.NewIncomingContext(verified(certOf("@hooli/erlich")), metadata.Pairs("authorization", "Bearer nope"))

		_, err := serve(h, known(), nil, ctx, getMethod)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	// A credential may say which tenant holds the actor it names -- a device
	// certificate carrying both is the ordinary case -- and this is the only
	// place the claim and the row are both in hand. A handler has read nothing;
	// a resolver has the row but was asked a different question, and one that
	// resolves by identifier never looks at the tenant beside it.
	//
	// So it is the interceptor that disagrees, which means an app gets the check
	// without writing it. That is the point: the issuer of such a certificate
	// meant the tenant to be checked, and a resolver that quietly ignores it is
	// what makes it true that it was not.
	t.Run("a credential that names the wrong tenant is refused", func(t *testing.T) {
		x := require.New(t)

		// Names the actor by identifier, the way a certificate does, and says
		// it is held by a tenant that does not hold it.
		says := func(tenant pdid.Id) auth.Handler {
			return auth.HandlerFunc(func(context.Context) (auth.Identity, error) {
				return auth.Identity{
					Id:       adminId.String(),
					TenantId: tenant.String(),
					Grant:    frame.Whole(),
				}, nil
			})
		}

		by := resolverOf(map[string]*frame.Frame{
			adminId.String(): frame.New(adminId, acme, frame.Whole()),
		})

		_, err := serve(says(hooli), by, nil, context.Background(), getMethod)
		x.Equal(codes.Unauthenticated, status.Code(err))
		x.Contains(status.Convert(err).Message(), "does not hold")

		// And the same credential naming the tenant that does hold them is
		// served, so what is being tested is the disagreement and not the field.
		ctx, err := serve(says(acme), by, nil, context.Background(), getMethod)
		x.NoError(err)

		f, ok := frame.From(ctx)
		x.True(ok)
		x.Equal(acme, f.Tenant)
	})

	// A resolver that fails for a reason of its own is not the caller being
	// wrong, and they are not sent to fetch a credential they already have.
	t.Run("a resolver that went wrong is not the caller's fault", func(t *testing.T) {
		x := require.New(t)

		broken := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
			return nil, fmt.Errorf("dial tcp 10.0.0.1:5432: connect: connection refused")
		})

		_, err := serve(auth.Plain(), broken, nil, incoming("Plain @acme/admin"), getMethod)
		x.Equal(codes.Internal, status.Code(err))
		x.NotContains(status.Convert(err).Message(), "10.0.0.1", "what the query said is for the log")

		// One that says it could not tell is told apart from one that failed,
		// for the same reason a token store is.
		unsure := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
			return nil, fmt.Errorf("the read replica: %w", auth.ErrUnavailable)
		})

		_, err = serve(auth.Plain(), unsure, nil, incoming("Plain @acme/admin"), getMethod)
		x.Equal(codes.Unavailable, status.Code(err))
	})

	// A nil frame in the context reads as a frame that is there and says
	// nothing, which is worse than none.
	t.Run("a resolver that answers with neither is not believed", func(t *testing.T) {
		x := require.New(t)

		empty := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
			return nil, nil
		})

		_, err := serve(auth.Plain(), empty, nil, incoming("Plain @acme/admin"), getMethod)
		x.Equal(codes.Internal, status.Code(err))
	})

	// A call that did not come in over the wire -- one server calling another
	// in the same process -- was vouched for when it did come in.
	t.Run("a call that already has a frame is left alone", func(t *testing.T) {
		x := require.New(t)

		f := frame.New(erlichId, hooli, frame.Whole())
		ctx := frame.Into(incoming("Plain @acme/admin"), f)

		served, err := serve(auth.Plain(), known(), nil, ctx, getMethod)
		x.NoError(err)

		v, ok := frame.From(served)
		x.True(ok)
		x.Same(f, v, "the frame it arrived with, and not one worked out again")
	})

	t.Run("a stream is served the same way a call is", func(t *testing.T) {
		x := require.New(t)

		var served context.Context
		err := auth.InterceptorStream(auth.Plain(), known(), nil)(
			nil,
			fakeStream{ctx: incoming("Plain @acme/admin")},
			&grpc.StreamServerInfo{FullMethod: getMethod},
			func(_ any, ss grpc.ServerStream) error {
				served = ss.Context()
				return nil
			},
		)
		x.NoError(err)

		f, ok := frame.From(served)
		x.True(ok)
		x.Equal(adminId, f.Actor)

		// And refused the same way, before the handler runs.
		err = auth.InterceptorStream(auth.Plain(), known(), nil)(
			nil,
			fakeStream{ctx: incoming()},
			&grpc.StreamServerInfo{FullMethod: getMethod},
			func(any, grpc.ServerStream) error {
				x.Fail("the handler ran for a call nobody vouched for")
				return nil
			},
		)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}

// fakeStream is a [grpc.ServerStream] that is nothing but the context of a
// call, which is the only part of one this package touches.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s fakeStream) Context() context.Context { return s.ctx }

func TestInject(t *testing.T) {
	t.Run("a provider replaces what was said before rather than adding to it", func(t *testing.T) {
		x := require.New(t)

		// Two answers to "who is calling" is a question with no answer, and
		// the one that would win is whichever came first.
		ctx := auth.PlainProvider("@acme/admin").Provide(context.Background())
		ctx = auth.PlainProvider("@hooli/erlich").Provide(ctx)

		md, ok := metadata.FromOutgoingContext(ctx)
		x.True(ok)
		x.Equal([]string{"Plain @hooli/erlich"}, md.Get("authorization"))
	})

	t.Run("what a provider writes is what a handler reads", func(t *testing.T) {
		x := require.New(t)

		out := auth.PlainProvider(admin().Name()).Provide(context.Background())
		md, _ := metadata.FromOutgoingContext(out)

		v, err := auth.Plain().Handle(metadata.NewIncomingContext(context.Background(), md))
		x.NoError(err)
		x.Equal("acme", v.Tenant)
		x.Equal("admin", v.Alias)

		out = auth.BearerProvider("s3cret").Provide(context.Background())
		md, _ = metadata.FromOutgoingContext(out)

		s := auth.NewMemTokenStore()
		s.Add("s3cret", admin(), time.Time{})

		v, err = auth.Bearer(s).Handle(metadata.NewIncomingContext(context.Background(), md))
		x.NoError(err)
		x.Equal(auth.MethodBearer, v.Method)
	})
}

// TestAStoreThatIsNotThereSaysSo is the order of two questions in `statusOf`,
// and it was the wrong way round.
//
// `status.FromError` unwraps -- since grpc-go 1.83 it asks `errors.As` -- so a
// handler that wrapped both an upstream error and `ErrUnavailable` was reported
// with the upstream code. A token store that was down answered `Unimplemented`,
// which reads as "the method you called does not exist" and sends nobody to
// look at the store.
func TestAStoreThatIsNotThereSaysSo(t *testing.T) {
	x := require.New(t)

	// What `auth.Remote` builds when the call to the store fails: the upstream
	// status and ErrUnavailable, in one error.
	up := status.Error(codes.Unimplemented, "unknown method Introspect")
	err := fmt.Errorf("%w: introspect: %w", auth.ErrUnavailable, up)

	h := auth.HandlerFunc(func(ctx context.Context) (auth.Identity, error) {
		return auth.Identity{}, err
	})

	_, got := auth.InterceptorUnary(h, auth.ResolverFunc(
		func(ctx context.Context, id auth.Identity) (*frame.Frame, error) {
			return nil, errors.New("never reached")
		}), nil)(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/app.RobotService/Get"},
		func(ctx context.Context, req any) (any, error) { return nil, nil },
	)

	x.Equal(codes.Unavailable, status.Code(got),
		"the store is down, which is not the caller's credential and not a missing method")
}
