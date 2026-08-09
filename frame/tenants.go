package frame

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
)

// Tenants is a set of tenants, or all of them.
//
// A set, and not the one tenant a caller belongs to beside a flag saying
// whether the wall applies to them. There are more answers than two: a
// credential carries an attenuation ([Grant]) and what a caller may see is the
// meet of the two, a policy may answer with several, and a deployment that lets
// a resource be shared with another tenant adds another. Each of those is a
// longer list and nothing else.
type Tenants struct {
	all bool
	ids []pdid.Id
}

// Everything is every tenant there is.
//
// Nothing reaches it by asking. It is what a deployment's own work runs as --
// through a server the wall was never installed on -- or what a policy answers
// when a deployment has said so. There is no identifier compared against a
// well-known one to arrive here: a privilege granted by being a particular row
// cannot be revoked, cannot be narrowed, and belongs to whoever finds the row.
var Everything = Tenants{all: true}

// Nothing is the scope of a caller who may see none, which is what the meet of
// two disjoint scopes is. It narrows a query to no rows at all rather than to
// all of them; see [Tenants.Uuids].
var Nothing = Tenants{}

// Only is the scope of a caller who may see the given tenants and no others.
func Only(ids ...pdid.Id) Tenants {
	return Tenants{ids: slices.Clip(slices.Clone(ids))}
}

// All reports whether this is every tenant there is, in which case there is
// nothing to narrow by and [Tenants.Uuids] says nothing.
func (t Tenants) All() bool { return t.all }

// Ids is what this scope holds.
func (t Tenants) Ids() []pdid.Id { return t.ids }

// Uuids is what a query narrows by.
//
// An empty answer is not the same as no narrowing, and the difference is the
// whole safety of this: `IDIn()` with nothing renders as `WHERE FALSE`, so a
// caller who may see no tenant sees no rows. Read the other way round it would
// be a scope that opened up as it ran out.
func (t Tenants) Uuids() []uuid.UUID {
	vs := make([]uuid.UUID, len(t.ids))
	for i, v := range t.ids {
		vs[i] = v.Uuid()
	}

	return vs
}

// Meet answers with what is in both this scope and `g`, which is how a
// credential narrows what its holder may see.
//
// It only ever narrows. A grant that names tenants its holder cannot see does
// not reach them: the meet of "my own" and "every tenant there is" is my own.
func (t Tenants) Meet(g Grant) Tenants {
	if g.AnyTenant() {
		return t
	}
	if t.all {
		return Only(g.TenantIds()...)
	}

	vs := make([]pdid.Id, 0, min(len(t.ids), len(g.TenantIds())))
	for _, v := range t.ids {
		if slices.Contains(g.TenantIds(), v) {
			vs = append(vs, v)
		}
	}

	return Tenants{ids: vs}
}

// Scope is which tenants the caller of ctx may see.
//
// A request with no frame is **refused** rather than served as anybody, and
// there is no scope that means "everything, because nobody asked". Some calls
// do have to go around the wall -- working out who is calling happens before
// there is a frame to be walled by, and a deployment puts its first rows there
// before anybody exists -- and they go around it by being handed a server the
// wall was never installed on. That is a wiring decision somebody can read; a
// scope that opened up whenever the frame was missing would be the same
// decision made silently, everywhere, including wherever a frame goes missing
// by mistake.
func Scope(ctx context.Context) (Tenants, error) {
	f, ok := From(ctx)
	if !ok {
		return Nothing, status.Error(codes.Unauthenticated, "who is asking?")
	}

	return f.Scope, nil
}

// Narrow is what generated walls ask: the tenants to narrow a query by, and
// whether there is anything to narrow by at all.
//
// The two answers are kept apart on purpose. An empty list means "no tenants",
// which is a query that matches nothing; `all` means "do not narrow", which is
// a query with no such predicate. Collapsing them would make the safe answer
// and the open one the same value.
func Narrow(ctx context.Context) (vs []uuid.UUID, all bool, err error) {
	s, err := Scope(ctx)
	if err != nil || s.All() {
		return nil, true, err
	}

	return s.Uuids(), false, nil
}

// Sets is what answers which sets a caller may see, for a schema that declared
// one at field 3.
//
// It is a function rather than a field of a [Frame] because it is not settled
// the way a tenant is. A caller's tenant is one row and comes back with them; a
// caller's sets are a membership table an app owns and queries how it likes,
// and often does not need at all -- so payday takes the answer where it is
// asked for rather than making every frame carry one.
type Sets func(ctx context.Context) (vs []uuid.UUID, all bool, err error)

// NarrowSet is what generated set scopes ask, and it is [Narrow]'s shape for
// the second axis: the sets to narrow a query by, and whether there is anything
// to narrow by at all.
//
// It is the meet of two answers -- what `of` says the caller may see, and what
// the credential allows of that -- and either may be missing:
//
//	of is nil          the app has no rule; the credential still narrows
//	of says all        every set, narrowed by the credential
//	the grant is whole what `of` said, unnarrowed
//
// # Why generated code calls this rather than `of` directly
//
// Because the attenuation would otherwise be the app's to remember. `of` is
// written to answer "which sets may this caller see", which is a question about
// the actor -- and a credential narrowed to one site is not about the actor at
// all. An app that answers the first question correctly and never thinks about
// the second hands out site-scoped keys that reach every site, and nothing
// anywhere says so.
//
// That is the same reason the predicate is generated in the first place: not
// that the code is hard, but that the failure is silent.
func NarrowSet(ctx context.Context, of Sets) (vs []uuid.UUID, all bool, err error) {
	f, ok := From(ctx)
	if !ok {
		// The same refusal [Scope] makes, for the same reason: a request with
		// no frame is one nobody vouched for, and there is no scope that means
		// "everything, because nobody asked".
		return nil, false, status.Error(codes.Unauthenticated, "who is asking?")
	}

	given, all := []uuid.UUID(nil), true
	if of != nil {
		if given, all, err = of(ctx); err != nil {
			return nil, false, err
		}
	}

	if f.Grant.AnySet() {
		return given, all, nil
	}

	held := make([]uuid.UUID, len(f.Grant.SetIds()))
	for i, v := range f.Grant.SetIds() {
		held[i] = v.Uuid()
	}

	if all {
		// Every set the app knows of, narrowed to the ones the credential was
		// made for. An empty list here is a credential that allows no set,
		// which renders as `WHERE FALSE`; see [Tenants.Uuids].
		return held, false, nil
	}

	vs = make([]uuid.UUID, 0, min(len(given), len(held)))
	for _, v := range given {
		if slices.Contains(held, v) {
			vs = append(vs, v)
		}
	}

	return vs, false, nil
}
