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
// Axes, each narrowed or not, which is the shape of a fine-grained token: which
// resources, and what may be done to them. It is deliberately not a map of one
// to the other -- "write here, read there" -- and the tokens people are used to
// do not do that either. A permission set that varies per resource is a policy,
// and a policy is not something a credential should be carrying around.
//
// Which resources is two axes and not one, because a schema may draw the line
// twice: the tenant is the wall, and an app may declare a set inside it -- a
// site, a cell, a region -- at field 3. They narrow independently and both are
// applied, so a token for one site of one tenant is `Whole().In(t).Within(s)`.
// An app that declared no set has nothing to say on the second, and
// [Grant.Within] is what nothing looks like there.
//
// **The zero value allows nothing.** A store that answers with a Grant it
// forgot to fill in hands out a credential that can do nothing, which somebody
// notices immediately; the other way round it hands out one that can do
// everything, which nobody notices at all.
type Grant struct {
	anyTenant bool
	anySet    bool
	anyAction bool

	tenants []pdid.Id
	sets    []pdid.Id
	actions []string
}

// Whole is a credential that narrows nothing: it allows whatever the actor it
// names allows. A header and a certificate are always this, since neither has
// anywhere to carry an attenuation.
func Whole() Grant {
	return Grant{anyTenant: true, anySet: true, anyAction: true}
}

// In narrows a Grant to the given tenants. Naming none allows none, which is
// what a credential that was given an empty list asked for.
func (g Grant) In(vs ...pdid.Id) Grant {
	g.anyTenant = false
	g.tenants = slices.Clip(slices.Clone(vs))

	return g
}

// Within narrows a Grant to the given sets -- whatever the app declared at
// field 3, a site or a cell or a region. Naming none allows none.
//
// It is the axis a key hung off a site needs. Such a key is the ordinary shape
// for a deployment that has sites at all: it is issued where the work is, it
// outlives whoever issued it, and it must not be usable anywhere else -- which
// the tenant axis cannot say, because every site of a tenant is inside it.
//
// Beside [Grant.In] rather than instead of it. Two tenants may name a set each
// and payday does not stop them, so a credential narrowed only by the set is
// one that reaches into the other tenant the moment those identifiers are ever
// guessed or leaked.
func (g Grant) Within(vs ...pdid.Id) Grant {
	g.anySet = false
	g.sets = slices.Clip(slices.Clone(vs))

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
func (g Grant) IsWhole() bool { return g.anyTenant && g.anySet && g.anyAction }

// AnyTenant reports whether this narrows no tenant, in which case
// [Grant.TenantIds] says nothing.
func (g Grant) AnyTenant() bool { return g.anyTenant }

// TenantIds is what this narrows to.
func (g Grant) TenantIds() []pdid.Id { return g.tenants }

// AnySet reports whether this narrows no set, in which case [Grant.SetIds] says
// nothing.
func (g Grant) AnySet() bool { return g.anySet }

// SetIds is what this narrows to.
func (g Grant) SetIds() []pdid.Id { return g.sets }

// Allows reports whether the given method is one this credential may be used
// for.
func (g Grant) Allows(method string) bool {
	if g.anyAction {
		return true
	}

	return slices.Contains(g.actions, method)
}
