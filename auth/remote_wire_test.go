package auth_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdpb"
)

// tokens is an identity store on the other end of a socket: the half such a
// store implements, with the row lookup replaced by a map.
type tokens struct {
	pdpb.UnimplementedTokenServiceServer

	vs map[string]auth.Identity

	// down is what a store that cannot answer looks like, which is a different
	// answer from "no such token" and has to stay different all the way across.
	down bool
}

func (s *tokens) Introspect(ctx context.Context, req *pdpb.TokenIntrospectRequest) (*pdpb.TokenIntrospectResponse, error) {
	if s.down {
		return nil, status.Error(codes.Unavailable, "the database is not there")
	}

	v, ok := s.vs[req.GetToken()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such token")
	}

	return auth.Introspection(v)
}

// dial runs `s` on a real gRPC server over an in-memory socket and answers with
// a client for it.
//
// A socket rather than the client interface stubbed out, because what is being
// checked is that a Grant survives being marshaled, sent, and unmarshaled --
// and a stub proves nothing about any of those three.
func dial(t *testing.T, s *tokens) pdpb.TokenServiceClient {
	t.Helper()

	l := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	pdpb.RegisterTokenServiceServer(g, s)

	go func() { _ = g.Serve(l) }()
	t.Cleanup(g.Stop)

	c, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return l.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	return pdpb.NewTokenServiceClient(c)
}

// TestRemoteOverTheWire is the whole path: a token arrives at one server, is
// exchanged for a name at another, and what it was narrowed to is enforced back
// at the first.
//
// It is the claim `payday.TokenService` exists to make, and until this ran it
// was three pieces that each worked alone.
func TestRemoteOverTheWire(t *testing.T) {
	const eraseMethod = "/app.RobotService/Erase"

	store := func(t *testing.T, vs map[string]auth.Identity) auth.Handler {
		return auth.Bearer(auth.Remote(dial(t, &tokens{vs: vs})))
	}

	t.Run("a token narrowed to one method is refused for another", func(t *testing.T) {
		x := require.New(t)

		v := admin()
		v.Grant = frame.Whole().To(getMethod)

		h := store(t, map[string]auth.Identity{"rk_read": v})

		ctx, err := serve(h, known(), nil, incoming("Bearer rk_read"), getMethod)
		x.NoError(err)

		f, ok := frame.From(ctx)
		x.True(ok)
		x.Equal(adminId, f.Actor)

		// The narrowing survived the wire, which is the one thing that could
		// have been lost silently: a Grant that arrived as Whole would serve
		// this next call and nothing anywhere would say so.
		x.False(f.Grant.IsWhole())
		x.True(f.Grant.Allows(getMethod))
		x.False(f.Grant.Allows(eraseMethod))

		_, err = serve(h, known(), nil, incoming("Bearer rk_read"), eraseMethod)
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("a token narrowed to nothing is refused for everything", func(t *testing.T) {
		x := require.New(t)

		// The shape an empty list cannot express on its own. A store that
		// meant this and a store that meant "anything" send the same three
		// empty lists, and only the flags beside them differ.
		v := admin()
		v.Grant = frame.Whole().To()

		h := store(t, map[string]auth.Identity{"rk_nothing": v})

		_, err := serve(h, known(), nil, incoming("Bearer rk_nothing"), getMethod)
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("a token narrowed to nothing at all still names who it is", func(t *testing.T) {
		x := require.New(t)

		v := admin()
		v.Grant = frame.Whole()

		h := store(t, map[string]auth.Identity{"rk_whole": v})

		ctx, err := serve(h, known(), nil, incoming("Bearer rk_whole"), eraseMethod)
		x.NoError(err)

		f, ok := frame.From(ctx)
		x.True(ok)
		x.True(f.Grant.IsWhole())
	})

	// An expiry crosses so that a **stream** can be cut by it. A unary call
	// carries its credential every time and is refused at the next one, by the
	// store rather than here -- which is why this asserts the value arrived and
	// then asserts the one thing payday does with it.
	t.Run("an expiry crosses and cuts a stream", func(t *testing.T) {
		x := require.New(t)

		at := time.Now().Add(50 * time.Millisecond)

		v := admin()
		v.Grant = frame.Whole()
		v.Expires = at

		c := dial(t, &tokens{vs: map[string]auth.Identity{"rk_short": v}})

		id, err := auth.Remote(c).Lookup(t.Context(), "rk_short")
		x.NoError(err)
		x.True(at.Equal(id.Expires), "the expiry is what the store said")
		x.True(id.Valid(time.Now()))
		x.False(id.Valid(at.Add(time.Second)))

		// And the stream it was handed to ends by itself.
		err = auth.InterceptorStream(auth.Bearer(auth.Remote(c)), known(), nil)(
			nil,
			fakeStream{ctx: incoming("Bearer rk_short")},
			&grpc.StreamServerInfo{FullMethod: getMethod},
			func(_ any, ss grpc.ServerStream) error {
				<-ss.Context().Done()

				return ss.Context().Err()
			},
		)
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
		x.Contains(err.Error(), "expired")
	})

	t.Run("a token nobody has heard of stops the search", func(t *testing.T) {
		x := require.New(t)

		h := store(t, map[string]auth.Identity{})

		_, err := serve(h, known(), nil, incoming("Bearer rk_nope"), getMethod)
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	// The refusal that is not about the caller at all. Told they are
	// unauthenticated, every service holding a perfectly good token throws it
	// away and its operator goes looking for a problem that is not there.
	t.Run("a store that cannot answer is not the caller's fault", func(t *testing.T) {
		x := require.New(t)

		h := auth.Bearer(auth.Remote(dial(t, &tokens{down: true})))

		_, err := serve(h, known(), nil, incoming("Bearer rk_whatever"), getMethod)
		x.Error(err)
		x.Equal(codes.Unavailable, status.Code(err))
	})

	// A credential that is there and names nobody is not a credential that is
	// missing, so it must not fall through to whatever would have been asked
	// next.
	t.Run("a token that names nobody here does not fall through", func(t *testing.T) {
		x := require.New(t)

		v := auth.Identity{Tenant: "acme", Alias: "nobody", Grant: frame.Whole()}
		h := auth.Seq(store(t, map[string]auth.Identity{"rk_ghost": v}), auth.Plain())

		_, err := serve(h, known(), nil, incoming("Bearer rk_ghost", "Plain @acme/admin"), getMethod)
		x.Error(err)
	})
}
