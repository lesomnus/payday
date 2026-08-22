package auth

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// Remote is a [TokenStore] that asks another server.
//
// It is the other half of `payday.TokenService`, and it is here rather than in
// each app because there is nothing per app in it: a token is a string, the
// answer is an [Identity], and neither mentions an entity. An app wires it
// once:
//
//	conn, err := grpc.NewClient(addr, creds, auth.BearerProvider(key).DialOption())
//	h := auth.Bearer(auth.Remote(pdpb.NewTokenServiceClient(conn)))
//
// The connection carries the **app's own** credential, not the bearer's. That
// is what makes this safe to serve: the identity store answers only apps it
// knows, and an operator decides which of them may ask by what they allow on
// each app's credential.
//
// # It asks on every request
//
// There is no cache here, deliberately. A token that was revoked a second ago
// is a token that stops working now, and that is the whole reason to carry an
// opaque token rather than a signed one. Anything cached is a window in which a
// revoked token still works, and the length of that window is a decision a
// deployment should make on purpose -- so it is not made here by default.
//
// What that costs is a round trip per request, on the path every request takes.
// A deployment that cannot pay it should say so with a store that wraps this
// one, where the window it is accepting is written down.
func Remote(c pdpb.TokenServiceClient) TokenStore {
	return TokenStoreFunc(func(ctx context.Context, token string) (Identity, error) {
		res, err := c.Introspect(ctx, pdpb.TokenIntrospectRequest_builder{
			Token: token,
		}.Build())
		if err != nil {
			// `NotFound` is the token and everything else is not.
			//
			// The temptation is to read every refusal as a bad token, and it is
			// wrong in the direction that hurts most: a `PermissionDenied` here
			// means **this app** may not introspect, and a `Unauthenticated`
			// means this app's own credential has gone bad. Told to the bearer
			// as "no such token", either one is an outage that looks exactly
			// like every user suddenly typing the wrong thing, and the operator
			// has nothing to go on.
			//
			// So the split is by who the refusal is about, and only the store
			// saying "I have no such row" is about the token. It is the same
			// division an in-process store already makes.
			if status.Code(err) == codes.NotFound {
				return Identity{}, fmt.Errorf("introspect: %w", ErrUnknownToken)
			}

			return Identity{}, fmt.Errorf("%w: introspect: %w", ErrUnavailable, err)
		}

		return IdentityFrom(res)
	})
}

// IdentityFrom reads what [TokenService.Introspect] answered.
func IdentityFrom(v *pdpb.TokenIntrospectResponse) (Identity, error) {
	id := Identity{
		Tenant: v.GetTenant(),
		Alias:  v.GetAlias(),
	}

	// Parsed here rather than passed along as bytes, because [Identity.Id] is
	// documented to owe a [Resolver] a string [pdid.Parse] can read. A store
	// that answered with sixteen bytes that are not an identifier is a store
	// this must refuse, and refusing at the edge is what keeps the resolver
	// from having to.
	if b := v.GetId(); len(b) > 0 {
		k, err := pdid.From(b)
		if err != nil {
			return Identity{}, fmt.Errorf("id: %w", err)
		}

		id.Id = k.String()
	}
	if b := v.GetTenantId(); len(b) > 0 {
		k, err := pdid.From(b)
		if err != nil {
			return Identity{}, fmt.Errorf("tenant id: %w", err)
		}

		id.TenantId = k.String()
	}

	g, err := grantFrom(v.GetGrant())
	if err != nil {
		return Identity{}, err
	}

	id.Grant = g
	if u := v.GetExpires(); u != nil {
		id.Expires = u.AsTime()
	}

	return id, nil
}

// Introspection is what a [TokenService] answers with, given what its own store
// worked out.
//
// It is here so that an identity store implementing the service writes its
// answer once: whatever it already builds for its in-process [TokenStore] is
// what crosses, and the rule that a Grant's every axis carries a flag beside
// its list is applied by this function rather than by each store separately.
func Introspection(id Identity) (*pdpb.TokenIntrospectResponse, error) {
	res := pdpb.TokenIntrospectResponse_builder{
		Tenant: id.Tenant,
		Alias:  id.Alias,
		Grant:  grantOf(id.Grant),
	}
	if !id.Expires.IsZero() {
		res.Expires = timestamppb.New(id.Expires)
	}

	if s := id.Id; s != "" {
		k, err := pdid.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("id: %w", err)
		}

		res.Id = k.Bytes()
	}
	if s := id.TenantId; s != "" {
		k, err := pdid.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("tenant id: %w", err)
		}

		res.TenantId = k.Bytes()
	}

	return res.Build(), nil
}

// grantFrom reads a Grant off the wire.
//
// It starts from [frame.Whole] and narrows whichever axis said it was narrowed,
// which is the only way round that works: a Grant is built by narrowing and
// there is no way back to "any" once an axis has been set.
//
// A message that is not there narrows **everything**, which is the zero Grant
// and allows nothing. That is the safe reading of silence, and it means a store
// that forgot to answer with a grant at all hands out a token that can do
// nothing -- noticed at once, rather than one that can do everything.
func grantFrom(v *pdpb.Grant) (frame.Grant, error) {
	if v == nil {
		return frame.Grant{}, nil
	}

	g := frame.Whole()
	if !v.GetAnyTenant() {
		vs, err := idsOf(v.GetTenants())
		if err != nil {
			return frame.Grant{}, fmt.Errorf("grant: tenants: %w", err)
		}

		g = g.In(vs...)
	}
	if !v.GetAnySet() {
		vs, err := idsOf(v.GetSets())
		if err != nil {
			return frame.Grant{}, fmt.Errorf("grant: sets: %w", err)
		}

		g = g.Within(vs...)
	}
	if !v.GetAnyAction() {
		g = g.To(v.GetActions()...)
	}

	return g, nil
}

// grantOf writes a Grant to the wire, flags and all.
func grantOf(g frame.Grant) *pdpb.Grant {
	return pdpb.Grant_builder{
		AnyTenant: g.AnyTenant(),
		Tenants:   bytesOf(g.TenantIds()),
		AnySet:    g.AnySet(),
		Sets:      bytesOf(g.SetIds()),
		AnyAction: g.AnyAction(),
		Actions:   g.Actions(),
	}.Build()
}

// idsOf and bytesOf both answer nil for nothing rather than an empty slice, so
// that a Grant through the wire is the Grant that went in. They mean the same
// thing to everything that reads one -- `Allows` and `Meet` are about contents
// -- and being identical is what lets a test compare the two ends directly
// instead of comparing them loosely enough to miss a real difference.
func idsOf(bs [][]byte) ([]pdid.Id, error) {
	if len(bs) == 0 {
		return nil, nil
	}

	vs := make([]pdid.Id, len(bs))
	for i, b := range bs {
		v, err := pdid.From(b)
		if err != nil {
			return nil, err
		}

		vs[i] = v
	}

	return vs, nil
}

func bytesOf(vs []pdid.Id) [][]byte {
	if len(vs) == 0 {
		return nil
	}

	bs := make([][]byte, len(vs))
	for i, v := range vs {
		bs[i] = v.Bytes()
	}

	return bs
}
