package frame

import (
	"slices"

	"github.com/lesomnus/payday/pdid"
)

// Grant is what a credential allows, which is at most what the actor it names
// allows.
//
// It is an attenuation and never a widening: a credential cannot let its bearer
// do what the actor could not. Whatever decides what the actor may do -- the
// wall, and whatever a deployment puts above it -- runs as it always did, and
// this narrows the answer afterwards. A token that says "every tenant" held by
// somebody who may see one still sees one.
//
// Two axes, each narrowed or not, which is the shape of a fine-grained token: a
// set of resources and a set of things that may be done to them. It is
// deliberately not a map of one to the other -- "write here, read there" -- and
// the tokens people are used to do not do that either. A permission set that
// varies per resource is a policy, and a policy is not something a credential
// should be carrying around.
//
// **The zero value allows nothing.** A store that answers with a Grant it
// forgot to fill in hands out a credential that can do nothing, which somebody
// notices immediately; the other way round it hands out one that can do
// everything, which nobody notices at all.
type Grant struct {
	anyTenant bool
	anyAction bool

	tenants []pdid.Id
	actions []string
}

// Whole is a credential that narrows nothing: it allows whatever the actor it
// names allows. A header and a certificate are always this, since neither has
// anywhere to carry an attenuation.
func Whole() Grant {
	return Grant{anyTenant: true, anyAction: true}
}

// In narrows a Grant to the given tenants. Naming none allows none, which is
// what a credential that was given an empty list asked for.
func (g Grant) In(vs ...pdid.Id) Grant {
	g.anyTenant = false
	g.tenants = slices.Clip(slices.Clone(vs))

	return g
}

// To narrows a Grant to the given methods, by the name gRPC knows them by --
// "/app.RobotService/Get". Naming none allows none.
func (g Grant) To(vs ...string) Grant {
	g.anyAction = false
	g.actions = slices.Clip(slices.Clone(vs))

	return g
}

// IsWhole reports whether this narrows nothing at all.
func (g Grant) IsWhole() bool { return g.anyTenant && g.anyAction }

// AnyTenant reports whether this narrows no tenant, in which case
// [Grant.TenantIds] says nothing.
func (g Grant) AnyTenant() bool { return g.anyTenant }

// TenantIds is what this narrows to.
func (g Grant) TenantIds() []pdid.Id { return g.tenants }

// Allows reports whether the given method is one this credential may be used
// for.
func (g Grant) Allows(method string) bool {
	if g.anyAction {
		return true
	}

	return slices.Contains(g.actions, method)
}
