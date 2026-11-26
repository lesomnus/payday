package auth_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdpb"
)

// A token's scope is the app's own word about the token, and payday's whole
// part in it is carrying it: from what the store answered, to the frame a
// handler reads. These hold it to that -- that it arrives, that it is the
// token's and not the person's, and that nothing else puts one there.

// scoped is one person's two tokens and a third that carries none.
func scoped() map[string]auth.Identity {
	at := func(scope string) auth.Identity {
		v := admin()
		v.Grant = frame.Whole()
		v.Scope = scope
		return v
	}

	return map[string]auth.Identity{
		"rk_cameras": at("cameras:01"),
		"rk_doors":   at("doors:07"),
		"rk_plain":   at(""),
	}
}

// scopeOf is the scope the handler of one call read, through `h`.
func scopeOf(t *testing.T, h auth.Handler, r auth.Resolver, token string) string {
	t.Helper()

	ctx, err := serve(h, r, nil, incoming("Bearer "+token), getMethod)
	require.NoError(t, err)

	f, ok := frame.From(ctx)
	require.True(t, ok)

	return f.TokenScope()
}

func TestATokenScopeReachesTheHandler(t *testing.T) {
	stores := map[string]func(t *testing.T) auth.Handler{
		"in process": func(t *testing.T) auth.Handler {
			s := auth.NewMemTokenStore()
			for k, v := range scoped() {
				s.Add(k, v, time.Time{})
			}
			return auth.Bearer(s)
		},
		"over the wire": func(t *testing.T) auth.Handler {
			return auth.Bearer(auth.Remote(dial(t, &tokens{vs: scoped()})))
		},
	}

	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			// `known` answers the same frame for every call of one person, which
			// is what a resolver that keeps frames by actor does -- the cache a
			// token's scope must not be kept in.
			said := known()

			t.Run("each token answers its own, one after the other", func(t *testing.T) {
				x := require.New(t)
				h := store(t)

				x.Equal("cameras:01", scopeOf(t, h, said, "rk_cameras"))
				x.Equal("doors:07", scopeOf(t, h, said, "rk_doors"))
				x.Equal("", scopeOf(t, h, said, "rk_plain"))
				x.Equal("cameras:01", scopeOf(t, h, said, "rk_cameras"))
			})

			t.Run("and at once", func(t *testing.T) {
				h := store(t)

				want := map[string]string{"rk_cameras": "cameras:01", "rk_doors": "doors:07", "rk_plain": ""}
				var wg sync.WaitGroup
				for range 16 {
					for token, scope := range want {
						wg.Go(func() {
							ctx, err := serve(h, said, nil, incoming("Bearer "+token), getMethod)
							if !assertNoError(t, err) {
								return
							}
							f, _ := frame.From(ctx)
							if f.TokenScope() != scope {
								t.Errorf("%s: answered %q, and its own is %q", token, f.TokenScope(), scope)
							}
						})
					}
				}
				wg.Wait()
			})

			t.Run("and a stream reads it as a call does", func(t *testing.T) {
				x := require.New(t)
				h := store(t)

				var got string
				err := auth.InterceptorStream(h, said, nil)(
					nil,
					fakeStream{ctx: incoming("Bearer rk_doors")},
					&grpc.StreamServerInfo{FullMethod: getMethod},
					func(_ any, ss grpc.ServerStream) error {
						got = frame.Must(ss.Context()).TokenScope()
						return nil
					},
				)
				x.NoError(err)
				x.Equal("doors:07", got)
			})

			t.Run("and the frame the resolver keeps is left without one", func(t *testing.T) {
				x := require.New(t)
				h := store(t)

				r := resolverOf(map[string]*frame.Frame{"@acme/admin": frame.New(adminId, acme, frame.Whole())})
				x.Equal("cameras:01", scopeOf(t, h, r, "rk_cameras"))

				f, err := r.Resolve(t.Context(), admin())
				x.NoError(err)
				x.Empty(f.TokenScope())
			})
		})
	}
}

// What the frame carries is what the credential said, and only that: a
// resolver that puts a scope on the frame it answers is not a token, and a
// header or a certificate has nowhere to carry one.
func TestATokenScopeIsOnlyTheCredentials(t *testing.T) {
	x := require.New(t)

	forged := frame.New(adminId, acme, frame.Whole()).WithTokenScope("everything")
	r := resolverOf(map[string]*frame.Frame{"@acme/admin": forged})

	ctx, err := serve(auth.Plain(), r, nil, incoming("Plain @acme/admin"), getMethod)
	x.NoError(err)
	x.Empty(frame.Must(ctx).TokenScope())

	// A store that answers none -- every one written before there was such a
	// thing -- answers an empty one.
	s := auth.TokenStoreFunc(func(context.Context, string) (auth.Identity, error) {
		v := admin()
		v.Grant = frame.Whole()
		return v, nil
	})
	x.Equal("", scopeOf(t, auth.Bearer(s), r, "rk_anything"))
}

func TestIntrospectionCarriesTheScope(t *testing.T) {
	x := require.New(t)

	for _, scope := range []string{"", "cameras:01", "  a word, with spaces  ", "명령"} {
		v := admin()
		v.Grant = frame.Whole()
		v.Scope = scope

		res, err := auth.Introspection(v)
		x.NoError(err)
		x.Equal(scope, res.GetScope())

		got, err := auth.IdentityFrom(res)
		x.NoError(err)
		x.Equal(scope, got.Scope, "answered back exactly as it was given")
	}

	// And a store that fills the message itself, without the encoder.
	got, err := auth.IdentityFrom(pdpb.TokenIntrospectResponse_builder{Alias: "admin", Tenant: "acme", Scope: "row:3"}.Build())
	x.NoError(err)
	x.Equal("row:3", got.Scope)
}

func assertNoError(t *testing.T, err error) bool {
	t.Helper()
	if err != nil {
		t.Error(err)
		return false
	}
	return true
}
