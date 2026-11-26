package cmd_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
)

// A token may carry a word of the app's own -- its scope -- and the app's
// handler reads it off the frame. payday carries it and reads none of it, so
// what is shown here is only that it arrives, and that it is the token's: two
// tokens of one person answer each its own, one after the other and at once.

// keys is the identity store in front of this app, as far as a token goes: it
// mints a token for a holder with a scope, and answers `Introspect` for it.
// The rows are a map, and the answer is what a store's own is.
type keys struct {
	pdpb.UnimplementedTokenServiceServer

	held *auth.MemTokenStore
}

func newKeys() *keys { return &keys{held: auth.NewMemTokenStore()} }

// mint is a token for `b`'s holder, narrowed to nothing, carrying `scope`.
func (k *keys) mint(b *built, scope string) string {
	token := "rk_" + rand.Text()
	k.held.Add(token, auth.Identity{
		Id:       b.Holder.String(),
		TenantId: b.Tenant.String(),
		Grant:    frame.Whole(),
		Scope:    scope,
	}, time.Time{})

	return token
}

func (k *keys) Introspect(ctx context.Context, req *pdpb.TokenIntrospectRequest) (*pdpb.TokenIntrospectResponse, error) {
	id, err := k.held.Lookup(ctx, req.GetToken())
	if err != nil {
		return nil, status.Error(codes.NotFound, "no such token")
	}

	return auth.Introspection(id)
}

func TestATokensScopeIsWhatItsHandlerReads(t *testing.T) {
	scopes := []string{"cameras:01", "cameras:02", ""}

	t.Run("in process", func(t *testing.T) {
		b, ctx := build(t)
		ref := scopedRobot(t, b, ctx)
		call := func(scope string) (string, error) {
			v, err := b.Walled.Robot().TokenScope(frame.Into(ctx, frame.Must(b.as(ctx)).WithTokenScope(scope)), ref)
			return v.GetScope(), err
		}

		eachItsOwn(t, scopes, func(scope string) func() (string, error) {
			return func() (string, error) { return call(scope) }
		})
	})

	stores := map[string]func(t *testing.T, k *keys) auth.TokenStore{
		// The store in this process, as an app that keeps its own tokens has.
		"over the wire, from a store in process": func(t *testing.T, k *keys) auth.TokenStore {
			return k.held
		},
		// And in another, asked by `Introspect`: what an app in front of an
		// identity store has.
		"over the wire, from a store asked by Introspect": func(t *testing.T, k *keys) auth.TokenStore {
			g := grpc.NewServer()
			pdpb.RegisterTokenServiceServer(g, k)

			return auth.Remote(pdpb.NewTokenServiceClient(pdtest.Serve(t, g)))
		},
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			b, ctx := build(t)
			k := newKeys()
			b.Auth = auth.Bearer(store(t, k))

			ref := scopedRobot(t, b, ctx)
			c := app.NewClient(b.dialed(t, ctx)).Robot()

			eachItsOwn(t, scopes, func(scope string) func() (string, error) {
				// A token of its own for each scope, all of them the one
				// holder's.
				as := auth.BearerProvider(k.mint(b, scope)).Provide(ctx)
				return func() (string, error) {
					v, err := c.TokenScope(as, ref)
					return v.GetScope(), err
				}
			})
		})
	}
}

// scopedRobot is a robot for a TokenScope call to name.
func scopedRobot(t *testing.T, b *built, ctx context.Context) *app.RobotRef {
	t.Helper()

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	require.NoError(t, err)

	return app.RobotRef_builder{Id: v.GetId()}.Build()
}

// eachItsOwn makes a caller for each scope and holds each to its own answer:
// one after the other, twice round so that the second of a person's tokens is
// asked after the first was, and then all of them at once.
func eachItsOwn(t *testing.T, scopes []string, caller func(scope string) func() (string, error)) {
	t.Helper()

	calls := make([]func() (string, error), len(scopes))
	for i, scope := range scopes {
		calls[i] = caller(scope)
	}

	for range 2 {
		for i, scope := range scopes {
			got, err := calls[i]()
			require.NoError(t, err)
			require.Equal(t, scope, got)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8*len(scopes))
	for range 8 {
		for i, scope := range scopes {
			wg.Go(func() {
				got, err := calls[i]()
				if err == nil && got != scope {
					err = fmt.Errorf("answered %q, and the token's own is %q", got, scope)
				}
				errs <- err
			})
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
