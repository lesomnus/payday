package trail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/pdid"
)

// RetireTenant takes a tenant's trail out as [Policy.PurgeTenant] does, but for
// what a floor of the deployment's is still on: those rows stay, in the
// database and in the archive, for as long as their floors have to run, and
// the tenant's key stays with them. The passes destroy them as each floor
// passes, and the key goes with the last of them.
//
// # Which of the two
//
// Is the operator's, by the contract or by the law the tenant is under, and
// not payday's. A floor -- [Policy.Min], [Policy.MinBy] -- is what the
// deployment owes whoever it answers to: the access record a processor under
// Korea's PIPA keeps for a year, whoever's data it was. A tenant purged takes
// that record with it; a tenant retired leaves it for the year and no longer.
// What it leaves is the deployment's to keep, and it keeps nothing else: the
// rest goes now, as a purge's does, and a kind under no floor keeps nothing.
//
// # What it keeps, and until when
//
// A row of a kind under a floor, written recently enough that the floor is
// still on it: the floor's longer clock, from when the row was written. Its
// rows in the database, its chunks in the archive whose newest row is under
// one, its rows in the files from before, and the contents of what it shares
// -- all as they are. [TenantPurge.Until] is when the last of them can be no
// longer under a floor, by the floors as they are now.
//
// From then on the passes treat the tenant as one that keeps nothing of its
// own, which the floors raise to what they are on: a kind's rows go once its
// floor has passed, whatever the tenant's contract said. Once the last floor
// has, a pass purges what is left -- a chunk that held rows of both sides of a
// floor, the contents of what the tenant shared -- and its key, as
// [Policy.PurgeTenant] would have at once; and a deployment that lowers a floor
// meanwhile has it purged sooner. A hold on the tenant meanwhile is a hold all
// the same: the passes destroy nothing it is on, and the purge at the end waits
// for it.
//
// # The record that it left
//
// A blob in [RetiredNamespace], written through the archive's manifest and
// signed as everything there is, so that a pass knows the tenant has left
// whether or not the app still answers for it -- its contract may have left
// with it -- and nobody can make a pass think a tenant has that has not. So a
// tenant retires into the archive: with floors and no archive it is refused,
// and the tenant is purged. With no floors at all it keeps nothing, and is a
// purge.
//
// # And what it refuses
//
// What a purge refuses: a tenant under a hold, [ErrHeld], and one the app has
// no answer for. It writes receipts, `retire-tenant`, saying what went and what
// was kept until when, and [Policy.PlanTenantRetire] is the same, counted and
// not done.
func (p Policy) RetireTenant(ctx context.Context, s Store, tenant pdid.Id) (TenantPurge, error) {
	return p.leave(ctx, s, tenant, false, true)
}

// PlanTenantRetire is what [Policy.RetireTenant] would do, and does nothing.
func (p Policy) PlanTenantRetire(ctx context.Context, s Store, tenant pdid.Id) (TenantPurge, error) {
	return p.leave(ctx, s, tenant, true, true)
}

// longestFloor is the longest clock of any floor the deployment keeps: how long
// after a tenant retires the last of what it kept can still be under one.
func (p Policy) longestFloor() time.Duration {
	out := max(p.Min.Retain, p.Min.Destroy)
	for _, f := range p.MinBy {
		out = max(out, f.Retain, f.Destroy)
	}

	return out
}

// floored answers whether a row of kind `d` written at `at` is still under a
// floor at `now`: the floor's longer clock -- how long a row stays in the
// database, or exists at all -- from when it was written.
func (p Policy) floored(d pdid.Domain, at, now time.Time) bool {
	f := p.floor(d)
	l := max(f.Retain, f.Destroy)

	return l > 0 && at.Add(l).After(now)
}

// newest is when a chunk's newest row was written, as late as it can have been:
// a file adopted from a directory knows only its month.
func newest(c Chunk) time.Time {
	if !c.Last.IsZero() {
		return c.Last
	}

	return c.Month.AddDate(0, 1, 0)
}

// retiring is what a pass takes a retired tenant to answer: that it keeps
// nothing of its own, of any kind, which the floors raise to what they are on
// -- and the hold the app answers for it, which is a hold all the same.
//
// Every kind the deployment names is named here too, so that the deployment's
// own window for one -- longer than its floor -- is not what keeps it.
func (p Policy) retiring(h *Hold) Tenant {
	none := Keep{Retain: time.Nanosecond, Discard: true}

	t := Tenant{Keep: &none, By: map[pdid.Domain]Keep{}, Hold: h}
	for _, d := range p.Named() {
		t.By[d] = none
	}

	return t
}

// The labels the record of a retirement carries, beside [labelFormat] and
// [labelAt].
const (
	formatRetired = "retired"
	labelTenant   = "Tenant"
)

// retire records that a tenant has retired, now, through the manifest.
func (x *pass) retire(ctx context.Context, tenant pdid.Id) error {
	at := x.now.UTC().Format(time.RFC3339Nano)

	l := flob.Labels{}
	l.Set(labelFormat, formatRetired)
	l.Set(labelTenant, tenant.String())
	l.Set(labelAt, at)

	b := []byte(tenant.String() + " retired at " + at + "\n")
	if _, err := x.p.Archive.Use(RetiredNamespace).Add(ctx, flob.Meta{Labels: l}, bytes.NewReader(b)); err != nil && !errors.Is(err, flob.ErrAlreadyExists) {
		return fmt.Errorf("the record that %s retired: %w", tenant, err)
	}

	return nil
}

// retirement is one record of a tenant's retirement, by the labels the
// manifest recorded for it.
type retirement struct {
	tenant pdid.Id
	at     time.Time
	digest flob.Digest
}

// retirements is every record of a retirement the manifest accounts for:
// one nobody wrote through it is no record, and says so in the log.
func (x *pass) retirements(ctx context.Context) ([]retirement, error) {
	if x.m == nil {
		return nil, nil
	}

	w, ok := flob.AsWalker(x.raw().Use(RetiredNamespace))
	if !ok {
		return nil, errUnwalkable
	}

	out := []retirement{}
	for v, err := range w.Walk(ctx) {
		if err != nil {
			return out, err
		}

		l, ok := x.m.trusts(blobKey{RetiredNamespace, v.Digest()})
		if !ok {
			if x.once("retired " + string(v.Digest())) {
				log.From(ctx).WarnContext(ctx, "trail: a record of a retirement the manifest does not account for, left alone",
					"digest", v.Digest(), "see", "`trail verify`")
			}

			continue
		}
		if l[labelFormat] != formatRetired {
			continue
		}

		id, ok := tenantOf(l[labelTenant])
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, l[labelAt])
		if err != nil {
			continue
		}

		out = append(out, retirement{tenant: id, at: at, digest: v.Digest()})
	}

	return out, nil
}

// retired is the tenants that have retired, by when each last did.
func (x *pass) retired(ctx context.Context) (map[pdid.Id]time.Time, error) {
	vs, err := x.retirements(ctx)
	if err != nil {
		return nil, err
	}

	out := map[pdid.Id]time.Time{}
	for _, v := range vs {
		if at, ok := out[v.tenant]; !ok || v.at.After(at) {
			out[v.tenant] = v.at
		}
	}

	return out, nil
}

// unretire erases the records of a tenant's retirement, once nothing is kept
// of it: its purge, at once or at the end of its floors.
func (x *pass) unretire(ctx context.Context, tenant pdid.Id) error {
	if x.m == nil {
		return nil
	}
	if err := x.trust(ctx); err != nil {
		return err
	}

	vs, err := x.retirements(ctx)
	if err != nil {
		return err
	}
	for _, v := range vs {
		if v.tenant != tenant {
			continue
		}
		if err := x.p.Archive.Use(RetiredNamespace).Erase(ctx, v.digest); err != nil && !errors.Is(err, flob.ErrNotExist) {
			return fmt.Errorf("the record that %s retired: %w", tenant, err)
		}
	}

	return nil
}
