package cmd_test

import (
	"testing"

	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
)

// Listing tenants, and listing the people in one.
//
// Both were absent rather than declined -- nothing wrote down a reason, and the
// asymmetry showed up the first time somebody built an operator console: they
// could open a tenant whose name they already knew and could not find out which
// ones there were, and could not ask who was in one.
//
// What makes them safe is the wall, and that is what these check. Neither is a
// new right; both are the scope that was already there, enumerated.

// TestATenantListIsNarrowedLikeAnyOtherRead is the whole of why `list:` on the
// entity that **is** the wall is not alarming.
func TestATenantListIsNarrowedLikeAnyOtherRead(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)

	// A second organisation on the same deployment.
	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)

	// Somebody in acme lists tenants. There are two.
	vs, err := b.Walled.Tenant().List(b.as(ctx), app.TenantListRequest_builder{}.Build())
	x.NoError(err)

	got := []string{}
	for _, v := range vs.GetItems() {
		got = append(got, v.GetAlias())
	}
	x.Equal([]string{"acme"}, got, "the scope they already had, enumerated")

	// And the deployment, which is outside every tenant, sees both.
	all, err := b.Ungated.Tenant().List(ctx, app.TenantListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(all.GetItems(), 2)
	x.NotNil(other)
}

// TestHoldersCanBeListedByTenant is the question an operator asks first.
func TestHoldersCanBeListedByTenant(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)

	beta, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)

	_, err = b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: beta.GetId()}.Build(),
		Alias:  "bob",
	}.Build())
	x.NoError(err)

	as := func(tenant []byte) *app.HolderListRequest {
		return app.HolderListRequest_builder{
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{
					Tenant: app.TenantRef_builder{Id: tenant}.Build(),
				}.Build(),
			},
		}.Build()
	}

	// The deployment, from outside every tenant, asks who is in beta.
	vs, err := b.Ungated.Holder().List(ctx, as(beta.GetId()))
	x.NoError(err)

	got := []string{}
	for _, v := range vs.GetItems() {
		got = append(got, v.GetAlias())
	}
	x.Equal([]string{"bob"}, got)

	// And the filter grants nothing: somebody in acme naming beta is answered
	// with nothing rather than with beta's people. The wall ran first.
	vs, err = b.Walled.Holder().List(b.as(ctx), as(beta.GetId()))
	x.NoError(err)
	x.Empty(vs.GetItems(), "a filter can only cut the scope down, never widen it")

	// Naming their own tenant answers with their own people.
	vs, err = b.Walled.Holder().List(b.as(ctx), as(b.Tenant.Bytes()))
	x.NoError(err)
	x.NotEmpty(vs.GetItems())
}
