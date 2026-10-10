package trail

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
)

// Swept is how often the policy is applied when nothing says otherwise.
//
// Daily, and it is the one sweep of this shape whose period is not about the
// thing it collects. A queued event or an expired attempt is chased on the
// clock of what it is chasing. A trail row is not stale and never becomes
// stale -- it is **old**, on a scale of months -- so what this period decides
// is only how far past the window a row may sit, and a day is invisible against
// ninety of them.
const Swept = 24 * time.Hour

// Keep is the two clocks, for one kind of thing.
//
// [Keep.Retain] is how long a row stays in the database: an operational
// choice, about what a console can show and what a query costs. [Keep.Destroy]
// is how long the record exists at all, which is the obligation, and it is
// normally much the longer of the two. Between them the row lives in the
// archive.
//
// One number would have been the wrong shape even for one kind. The window
// somebody wants in the hot table is months and the window they must be able to
// produce a record over is years; a single number is either a database nobody
// can afford or a record that is gone too early.
//
// Both are **empty by default**, and empty is forever. That is the only honest
// default for a trail: a deployment upgrading into a version with opinions
// about how long its evidence lasts would discover them by not having the
// evidence.
type Keep struct {
	// Retain is how long a row stays in the database. Empty is forever.
	Retain time.Duration

	// Discard says these rows are removed with no copy kept.
	//
	// Its own field rather than an empty archive, because those are two
	// different states that look alike: *I have not configured where* and *I
	// do not want one*. A blank field that defaults to destruction is the
	// configuration mistake that is discovered by an auditor.
	Discard bool

	// Destroy is how long a row exists at all, counted from when it was
	// written: it leaves the database at Retain and the archive at Destroy.
	// Empty is forever.
	Destroy time.Duration

	// Note is where the numbers came from, when they came from a [Profile].
	Note string
}

// On answers whether either clock is running.
func (k Keep) On() bool { return k.Retain > 0 || k.Destroy > 0 }

// String is one kind's policy as a line worth logging.
func (k Keep) String() string {
	if !k.On() {
		return "forever"
	}

	out := []string{}
	if k.Retain > 0 {
		where := "the archive"
		if k.Discard {
			where = "nowhere"
		}

		out = append(out, fmt.Sprintf("%s in the database, then to %s", k.Retain, where))
	}
	if k.Destroy > 0 {
		out = append(out, fmt.Sprintf("destroyed after %s", k.Destroy))
	}

	v := strings.Join(out, ", ")
	if k.Note != "" {
		v += " (" + k.Note + ")"
	}

	return v
}

// life is how long a row exists at all under this, and zero for forever.
func (k Keep) life() time.Duration {
	switch {
	case k.Retain <= 0:
		// It never leaves the database.
		return 0
	case k.Discard:
		return k.Retain
	default:
		return k.Destroy
	}
}

// contradiction is what is wrong with a Keep whatever the archive is: a window
// that destroys a row before the row could have got there.
func (k Keep) contradiction() string {
	if k.Destroy > 0 && k.Discard {
		return "discard keeps no archive and destroy says how long to keep one"
	}
	if k.Destroy > 0 && k.Retain > 0 && k.Destroy < k.Retain {
		return "destroy is shorter than retain, so a row would be destroyed before it left the database"
	}

	return ""
}

// Floor is the least a deployment keeps whatever a tenant answers: its own
// obligations.
//
// An access log the deployment must keep for a year is the deployment's
// obligation and not a customer's, and no contract a customer signs shortens
// it. So a tenant's answer that keeps less than this is **raised** to it, and
// the raise is logged -- not refused, because a refusal would leave that
// tenant's rows on no clock at all, which is the one outcome worse than the
// floor.
//
// Empty is no floor, on either clock. That is the opposite of [Keep], whose
// empty is forever, and it is why this is its own type: a floor nobody wrote
// down is not a floor of forever.
type Floor struct {
	// Retain is the least time a row stays in the database.
	Retain time.Duration

	// Destroy is the least time a row exists at all, from when it was written.
	Destroy time.Duration

	// Note is where the numbers came from, when they came from a [Profile].
	Note string
}

// On answers whether it says anything.
func (f Floor) On() bool { return f.Retain > 0 || f.Destroy > 0 }

// String is the floor as a line worth logging.
func (f Floor) String() string {
	if !f.On() {
		return "none"
	}

	out := []string{}
	if f.Retain > 0 {
		out = append(out, fmt.Sprintf("%s in the database", f.Retain))
	}
	if f.Destroy > 0 {
		out = append(out, fmt.Sprintf("%s in all", f.Destroy))
	}

	v := "at least " + strings.Join(out, " and ")
	if f.Note != "" {
		v += " (" + f.Note + ")"
	}

	return v
}

// higher is the two floors at once, which is the higher of each clock.
func higher(a, b Floor) Floor {
	out := Floor{Retain: max(a.Retain, b.Retain), Destroy: max(a.Destroy, b.Destroy)}
	out.Note = strings.Trim(a.Note+"; "+b.Note, "; ")

	return out
}

// raise is `k`, kept at least as long as the floor says, and kept somewhere
// there is: with no archive, a row that would have lived out its window there
// lives it out in the database. It answers whether the floor raised anything.
func (f Floor) raise(k Keep, archive bool) (Keep, bool) {
	r, l := k.Retain, k.life()

	raised := false
	if f.Retain > 0 && r > 0 && r < f.Retain {
		r = f.Retain
		raised = true
	}
	if f.Destroy > 0 && l > 0 && l < f.Destroy {
		l = f.Destroy
		raised = true
	}
	if !raised && (archive || k.Discard || k.Retain <= 0) {
		return k, false
	}

	return shape(r, l, archive), raised
}

// shape is the Keep that holds a row in the database for `r` and lets it exist
// for `l` in all, zero being forever for either.
func shape(r, l time.Duration, archive bool) Keep {
	switch {
	case r <= 0:
		return Keep{}
	case l > 0 && l <= r:
		return Keep{Retain: r, Discard: true}
	case !archive && l <= 0:
		// Kept forever and there is nowhere to keep it but here.
		return Keep{}
	case !archive:
		return Keep{Retain: l, Discard: true}
	default:
		return Keep{Retain: r, Destroy: l}
	}
}

// longest is how long a row lasts that more than one tenant may read: as long
// as the longest of them keeps it, on each clock, and forever beats any
// duration.
//
// Not the shortest, and not whichever it is filed under. A row a transfer
// wrote is the evidence of both sides, and the side that keeps its evidence
// longer is not answered by the other side's contract. What this decides is
// only how long a row **lasts**; who may read it is the wall's, and no tenant
// reads another's rows because of it.
func longest(archive bool, ks ...Keep) Keep {
	if len(ks) == 0 {
		return Keep{}
	}

	same := true
	for _, k := range ks[1:] {
		if k.Retain != ks[0].Retain || k.Discard != ks[0].Discard || k.Destroy != ks[0].Destroy {
			same = false
		}
	}
	if same {
		return ks[0]
	}

	var r, l time.Duration
	rf, lf := false, false
	for _, k := range ks {
		if k.Retain <= 0 {
			rf = true
		} else {
			r = max(r, k.Retain)
		}

		if v := k.life(); v <= 0 {
			lf = true
		} else {
			l = max(l, v)
		}
	}
	if rf {
		r = 0
	}
	if lf {
		l = 0
	}

	return shape(r, l, archive)
}

// Tenant is what one tenant keeps, as the app answers for it: the same two
// things the deployment's own policy says for everybody.
//
// # Why it is the app's to answer
//
// What a tenant keeps follows from its contract -- a plan, a regulated
// customer, an agreement with an end date -- and a contract is the app's data.
// payday stores no plans and should not: the moment a plan changes, its
// window changes with it, and a second copy of the window here would be a copy
// that is wrong until somebody remembers it. So the deployment hands in
// [Policy.Tenants], and a pass asks it.
//
// Where the app keeps the answer is a guide of its own,
// `docs/guide/operator.md`: an entity the operator owns, one per tenant, which
// the tenant may read and only the operator's path may write.
//
// # How it is read
//
// The most specific thing anybody said wins, a kind before a blanket and the
// tenant before the deployment:
//
//   - what the tenant says of this kind, in [Tenant.By];
//   - what the deployment says of it, in [Policy.By];
//   - what the tenant says of everything, [Tenant.Keep];
//   - what the deployment says of everything, [Policy.Keep].
//
// So the zero value is *the deployment's, whatever it is*, and an answer that
// names one kind leaves the rest to the deployment. A kind the deployment named
// stays the deployment's until a tenant names it too: a deployment that keeps
// what its machines did forever does not lose that to a plan written about
// people.
//
// Then the floor, [Policy.Min], raises whatever came out of that.
type Tenant struct {
	// Keep is this tenant's answer for every kind nobody named. Nil is the
	// deployment's.
	Keep *Keep

	// By is the kinds that differ for this tenant.
	By map[pdid.Domain]Keep
}

// Policy is what a deployment keeps, per kind of thing and, when the app
// answers for them, per tenant.
//
// # Why one clock over the table was the wrong shape
//
// A deployment's obligations are not uniform across its entities, and the two
// ends of the range pull in opposite directions. What was done to a person is
// under a privacy regime: it has a stated limit and eventually has to stop
// existing. What a machine did is an operating record -- who drove that robot,
// which route it took, when the fault was logged -- and the requirement there
// is usually the opposite one, that it never be lost.
//
// A single clock forces the shorter of the two onto everything, and there is no
// global answer that is honest for both. So the policy names kinds, and a kind
// with nothing said about it gets [Policy.Keep].
//
// The kind is `Audit.domain`, which is a column for exactly this reason -- see
// the note on it in payday's audit.proto. Names are the ones the schema
// registered with `pdid`, so a deployment writes `holder` and `robot` rather
// than 2 and 17.
//
// # And why one policy over the deployment was too
//
// The same argument, one level up. A deployment whose customers keep their
// history for different lengths of time has to set every kind to the
// **shortest** window it owes anybody, and throw away the history of the
// customers paying to keep theirs. [Policy.Tenants] is the answer per tenant;
// see [Tenant].
type Policy struct {
	// Archive is where rows go when they leave the database. Nil keeps no copy
	// of anything, which is refused for any kind whose `Discard` does not say
	// so.
	//
	// `config.AuditConfig` makes one on a disk from `audit.archive`. Anything
	// else flob reaches -- S3, a store over HTTP -- is handed in by the app.
	Archive flob.Stores

	// Every is how often the policy is applied. Empty is [Swept].
	Every time.Duration

	// Keep is what a kind nothing was said about gets.
	Keep Keep

	// By is the kinds that differ, by domain.
	By map[pdid.Domain]Keep

	// Min is the deployment's own floor, for every kind: the least it keeps
	// whatever a tenant answers. See [Floor].
	Min Floor

	// MinBy is a kind's floor, by domain, on top of [Policy.Min].
	MinBy map[pdid.Domain]Floor

	// Tenants answers what one tenant keeps. Nil is every tenant keeping the
	// deployment's, which is what a deployment that says nothing gets.
	//
	// A pass asks it once per tenant, about every tenant rows are filed under
	// and every one a row it reaches names. A tenant whose answer fails keeps
	// everything for that pass -- nothing of theirs is moved or destroyed, and
	// the failure is logged -- because the alternative is falling back to a
	// window nobody chose for them.
	Tenants func(ctx context.Context, tenant pdid.Id) (Tenant, error)
}

// For is the deployment's own policy for one kind of thing: what a tenant
// that answers nothing keeps.
func (p Policy) For(d pdid.Domain) Keep {
	if v, ok := p.By[d]; ok {
		return v
	}

	return p.Keep
}

// ForTenant is what a tenant that answered `t` keeps of one kind: the answer,
// read as [Tenant] says, and raised to the floor.
func (p Policy) ForTenant(t Tenant, d pdid.Domain) Keep {
	k, _ := p.floor(d).raise(p.resolve(t, d), p.Archive != nil)

	return k
}

func (p Policy) resolve(t Tenant, d pdid.Domain) Keep {
	if v, ok := t.By[d]; ok {
		return v
	}
	if v, ok := p.By[d]; ok {
		return v
	}

	return p.rest(t)
}

// rest is a tenant's answer for every kind nobody named.
func (p Policy) rest(t Tenant) Keep {
	if t.Keep != nil {
		return *t.Keep
	}

	return p.Keep
}

// floor is one kind's floor, which is the deployment's and the kind's at once.
func (p Policy) floor(d pdid.Domain) Floor {
	if v, ok := p.MinBy[d]; ok {
		return higher(p.Min, v)
	}

	return p.Min
}

// Named is every kind this policy says something about, in domain order.
func (p Policy) Named() []pdid.Domain {
	return sorted(slices.Collect(maps.Keys(p.By)), slices.Collect(maps.Keys(p.MinBy)))
}

// named is every kind a pass over one tenant has to treat on its own.
func (p Policy) named(t Tenant) []pdid.Domain {
	return sorted(p.Named(), slices.Collect(maps.Keys(t.By)))
}

func sorted(vs ...[]pdid.Domain) []pdid.Domain {
	out := slices.Concat(vs...)
	slices.Sort(out)

	return slices.Compact(out)
}

// On answers whether there is anything to do.
func (p Policy) On() bool {
	if p.Keep.On() || p.Tenants != nil {
		return true
	}
	for _, v := range p.By {
		if v.On() {
			return true
		}
	}

	return false
}

// Valid refuses a policy that would destroy something nobody asked it to, or
// keep less than its own floor.
//
// Meant to be read where the process comes up rather than at the first sweep,
// which is what makes it worth having: a deployment that has named a window and
// no archive learns about it while somebody is watching, and not a day later
// when the first pass has already run.
//
// A tenant's answer is not here, since it is not known until a pass asks.
// One that contradicts itself is refused then, for that tenant; see
// [Policy.Tenants].
func (p Policy) Valid() error {
	if p.Archive != nil {
		if _, ok := flob.AsNamespacer(p.Archive); !ok {
			return fmt.Errorf("audit.archive: %w", errUnwalkable)
		}
		if _, ok := flob.AsWalker(p.Archive.Use(ReceiptNamespace)); !ok {
			return fmt.Errorf("audit.archive: %w", errUnwalkable)
		}
	}

	if err := p.valid("audit", "", p.Keep, p.Min); err != nil {
		return err
	}
	for _, d := range p.Named() {
		// A kind with a floor of its own and no window of its own is held to
		// it by the window everything else gets.
		at, of := "audit.by."+nameOf(d), ""
		if _, ok := p.By[d]; !ok {
			at, of = "audit", " for "+nameOf(d)
		}
		if err := p.valid(at, of, p.For(d), p.floor(d)); err != nil {
			return err
		}
	}

	return nil
}

func (p Policy) valid(at string, of string, k Keep, f Floor) error {
	if k.Retain > 0 && p.Archive == nil && !k.Discard {
		return fmt.Errorf("%s.retain names a window and audit.archive names nowhere to put what leaves it; "+
			"set audit.archive, or %s.discard: true to say the rows are meant to go", at, at)
	}
	if k.Destroy > 0 && p.Archive == nil {
		return fmt.Errorf("%s.destroy is how long the archive is kept and audit.archive names none", at)
	}
	if v := k.contradiction(); v != "" {
		return fmt.Errorf("%s: %s", at, v)
	}

	// Refused rather than raised, unlike a tenant's answer. This one is in the
	// deployment's own file, beside the floor it is under, and the person who
	// can fix it is the one reading the error.
	if f.Retain > 0 && k.Retain > 0 && k.Retain < f.Retain {
		return fmt.Errorf("%s.retain is %s and the floor%s is %s in the database", at, k.Retain, of, f.Retain)
	}
	if l := k.life(); f.Destroy > 0 && l > 0 && l < f.Destroy {
		return fmt.Errorf("%s keeps a row for %s in all and the floor%s is %s", at, l, of, f.Destroy)
	}

	return nil
}

// String is the policy as the line a process logs as it comes up.
func (p Policy) String() string {
	out := []string{"anything else: " + p.Keep.String()}
	for _, d := range p.Named() {
		if v, ok := p.By[d]; ok {
			out = append(out, nameOf(d)+": "+v.String())
		}
	}
	if p.Min.On() {
		out = append(out, "every kind: "+p.Min.String())
	}
	for _, d := range p.Named() {
		if v, ok := p.MinBy[d]; ok {
			out = append(out, nameOf(d)+": "+v.String())
		}
	}
	if p.Tenants != nil {
		out = append(out, "and each tenant's own, as the app answers")
	}

	return strings.Join(out, "; ")
}

func (p Policy) every() time.Duration {
	if p.Every <= 0 {
		return Swept
	}

	return p.Every
}

// Sweep applies the policy on a clock.
//
// It is not the same kind of loop as the ones that collect expired rows. An
// expired attempt is refused the moment it is presented, so a sweep that
// collects one is about disk. **Nothing else applies a retention window**, so a
// deployment whose sweep has been failing for a month is one that has been
// keeping records it said it would not -- which is why every pass that fails
// says so rather than being counted.
//
// It takes no lock, and neither does the generated drain, whose comment says
// so. Two replicas each apply the window: what that costs is duplicate rows in
// the archive, which [Read] drops and a merge folds, and a `Forget` that finds
// nothing, which is not an error. What it must not cost is a chunk half
// written, which is why a chunk is written whole.
func Sweep(s Store, p Policy) spin.Func {
	return spin.Every(p.every(), func(ctx context.Context) error {
		p.Pass(ctx, s)

		return nil
	})
}

// Pass applies the policy once, which is what a tick of [Sweep] does and what
// an operator running the command with no window of their own is asking for.
//
// Exported for that second caller, and it matters: a command that took its own
// cutoff and nothing else would let `roster trail prune` destroy the very kind
// the configuration says to keep forever, which is a footgun pointed at the one
// thing this package exists to protect.
//
// # The database
//
// With nobody answering per tenant, one pass per kind that was named and one
// for everything else -- which is why a [Scope] takes [Kinds] rather than a
// domain: the default pass is *everything but these*, and a database answers
// that in one query where a loop over the domains it has never heard of
// cannot.
//
// With [Policy.Tenants], the same thing tenant by tenant: every tenant rows
// are filed under, each with its own answer. A row that names more than one
// tenant is reached once, under the one it is filed under, and lasts as long as
// the longest of them keeps it; one that is not due yet is passed over, which
// is what the [Cursor] is for.
//
// # The archive
//
// Every namespace, each on its own tenant's windows: a chunk whose every row
// is past its window is destroyed, and the chunks of a month that can receive
// no more rows are folded into one. A [Receipt] records what went.
//
// It logs rather than fails: a database that blinked is a thing to try again,
// and taking the process down would be a retention policy that is also an
// outage.
func (p Policy) Pass(ctx context.Context, s Store) {
	x, err := p.start(s)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "trail: a pass", "err", err)

		return
	}

	x.adopt(ctx)

	if p.Tenants == nil {
		named := p.Named()
		for _, d := range named {
			x.drain(ctx, Scope{Kinds: Only(d)}, p.For(d), nameOf(d))
		}

		x.drain(ctx, Scope{Kinds: Except(named...)}, p.Keep, "*")
	} else {
		x.tenant(ctx, pdid.Nil)

		after := pdid.Nil
		for {
			vs, err := s.Tenants(ctx, after, Batch)
			if err != nil {
				log.From(ctx).WarnContext(ctx, "trail: the tenants of the trail", "err", err, "after", after)

				break
			}
			for _, v := range vs {
				x.tenant(ctx, v)
			}
			if len(vs) < Batch {
				break
			}

			after = vs[len(vs)-1]
		}
	}

	if p.Archive != nil {
		x.archive(ctx)
	}

	if err := x.r.write(ctx, p.Archive); err != nil {
		log.From(ctx).WarnContext(ctx, "trail: the receipts of a pass", "err", err)
	}
}

// pass is one application of a policy: one instant, one run, and every
// tenant's answer asked once.
type pass struct {
	p       Policy
	s       Store
	now     time.Time
	run     string
	answers map[pdid.Id]answer
	told    map[string]bool
	r       receipts
}

type answer struct {
	t   Tenant
	err error
}

func (p Policy) start(s Store) (*pass, error) {
	run, err := newRun()
	if err != nil {
		return nil, err
	}

	return &pass{
		p:       p,
		s:       s,
		now:     time.Now(),
		run:     run,
		answers: map[pdid.Id]answer{},
		told:    map[string]bool{},
	}, nil
}

// once answers true the first time it is asked about `k` in this pass, so that
// something worth a line in the log is not a line per row.
func (x *pass) once(k string) bool {
	if x.told[k] {
		return false
	}

	x.told[k] = true

	return true
}

// adopt takes in the files a version before this one left in the archive's
// directory, so that a deployment upgrading has nothing to run.
func (x *pass) adopt(ctx context.Context) {
	var root string
	switch v := x.p.Archive.(type) {
	case flob.OsStores:
		root = v.Root()
	case *flob.OsStores:
		root = v.Root()
	default:
		return
	}

	n, err := Adopt(ctx, x.p.Archive, root)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "trail: the files of a version before", "err", err, "adopted", n)
	} else if n > 0 {
		log.From(ctx).InfoContext(ctx, "trail: the files of a version before", "adopted", n, "into", LegacyNamespace)
	}
}

// answer is one tenant's answer, asked once a pass. Nobody -- the zero tenant
// -- is the deployment, and is never asked.
func (x *pass) answer(ctx context.Context, id pdid.Id) (Tenant, bool) {
	if id.IsZero() || x.p.Tenants == nil {
		return Tenant{}, true
	}
	if v, ok := x.answers[id]; ok {
		return v.t, v.err == nil
	}

	t, err := x.p.Tenants(ctx, id)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "trail: a tenant has no answer, so nothing of theirs is moved or destroyed this pass",
			"tenant", id, "err", err)
	}

	x.answers[id] = answer{t: t, err: err}

	return t, err == nil
}

// keep is what one tenant keeps of one kind, and false when it has no answer
// this pass. `named` false is everything nobody named.
func (x *pass) keep(ctx context.Context, id pdid.Id, d pdid.Domain, named bool) (Keep, bool) {
	t, ok := x.answer(ctx, id)
	if !ok {
		return Keep{}, false
	}

	k, f := x.p.rest(t), x.p.Min
	if named {
		k, f = x.p.resolve(t, d), x.p.floor(d)
	}

	kind := "*"
	if named {
		kind = nameOf(d)
	}
	if v := k.contradiction(); v != "" {
		if x.once("contradiction " + id.String() + kind) {
			log.From(ctx).WarnContext(ctx, "trail: a tenant's answer contradicts itself, so nothing of theirs of this kind is moved or destroyed this pass",
				"tenant", id, "kind", kind, "err", v)
		}

		return Keep{}, false
	}

	out, raised := f.raise(k, x.p.Archive != nil)
	if raised && x.once("raised "+id.String()+kind) {
		log.From(ctx).InfoContext(ctx, "trail: a tenant's answer is below the deployment's floor, and is raised to it",
			"tenant", id, "kind", kind, "answered", k.String(), "floor", f.String(), "keeps", out.String())
	}

	return out, true
}

// row is how long a row lasts, from every tenant it names; see [longest].
func (x *pass) row(ctx context.Context, v Row) (Keep, bool) {
	if len(v.Tenants) == 0 {
		return x.keep(ctx, pdid.Nil, v.Domain, true)
	}

	ks := make([]Keep, 0, len(v.Tenants))
	for _, id := range v.Tenants {
		k, ok := x.keep(ctx, id, v.Domain, true)
		if !ok {
			return Keep{}, false
		}

		ks = append(ks, k)
	}

	return longest(x.p.Archive != nil, ks...), true
}

// alone answers whether a row is the tenant's and nobody else's, which is the
// row whose window is the tenant's own and needs nothing more asked.
func alone(v Row, tenant pdid.Id) bool {
	if tenant.IsZero() {
		return len(v.Tenants) == 0
	}

	return len(v.Tenants) == 1 && v.Tenants[0] == tenant
}

// tenant is one tenant's rows of the database, kind by kind.
func (x *pass) tenant(ctx context.Context, id pdid.Id) {
	t, ok := x.answer(ctx, id)
	if !ok {
		return
	}

	named := x.p.named(t)
	for _, d := range named {
		k, ok := x.keep(ctx, id, d, true)
		if ok {
			x.drain(ctx, Scope{Kinds: Only(d), Whose: FiledUnder, Tenant: id}, k, nameOf(d))
		}
	}

	if k, ok := x.keep(ctx, id, pdid.Unknown, false); ok {
		x.drain(ctx, Scope{Kinds: Except(named...), Whose: FiledUnder, Tenant: id}, k, "*")
	}
}

// drain is one scope's rows out of the database, on one window. A row that
// names somebody else as well is on the longest of their windows, and stays
// when that one has not run out.
func (x *pass) drain(ctx context.Context, of Scope, k Keep, kind string) {
	if k.Retain <= 0 {
		return
	}

	before := x.now.Add(-k.Retain)
	archive := x.p.Archive != nil
	decide := func(v Row) fate {
		w := k
		if x.p.Tenants != nil && !alone(v, of.Tenant) {
			var ok bool
			if w, ok = x.row(ctx, v); !ok || w.Retain <= 0 || !v.Created.Before(x.now.Add(-w.Retain)) {
				return stays
			}
		}
		if w.Discard || !archive {
			return discarded
		}

		return archived
	}

	moved, gone, err := drain(ctx, x.s, of, before, x.p.Archive, x.run, decide)

	whose := ""
	if of.Whose != Anyone {
		whose = of.Tenant.String()
		if of.Tenant.IsZero() {
			whose = DeploymentNamespace
		}
	}

	if err != nil {
		log.From(ctx).WarnContext(ctx, "trail: the retention window",
			"kind", kind, "tenant", whose, "err", err, "before", before, "moved", moved, "discarded", gone)
	} else if moved+gone > 0 {
		log.From(ctx).InfoContext(ctx, "trail: the retention window",
			"kind", kind, "tenant", whose, "before", before, "moved", moved, "discarded", gone)
	}
	if gone > 0 {
		x.r.add(Receipt{
			Act:    "discard",
			Where:  "database",
			Tenant: whose,
			Kind:   kind,
			Before: before.UTC(),
			Rows:   gone,
		})
	}
}

// kindOf reads a chunk's kind back into a domain, and answers false for a
// chunk that named none -- everything nobody named.
func kindOf(name string) (pdid.Domain, bool) {
	if name == "" || name == "*" {
		return pdid.Unknown, false
	}

	return domainOf(name)
}

// judge is how a namespace's chunks are decided, and nil for a namespace this
// did not write.
func (x *pass) judge(ctx context.Context, ns string) func(Chunk) (Keep, bool) {
	switch ns {
	case LegacyNamespace, DeploymentNamespace:
		return func(c Chunk) (Keep, bool) {
			d, ok := kindOf(c.Kind)
			if !ok {
				return x.p.Keep, true
			}

			return x.p.For(d), true
		}

	case SharedNamespace:
		return func(c Chunk) (Keep, bool) {
			d, named := kindOf(c.Kind)

			ks := make([]Keep, 0, len(c.Tenants))
			for _, id := range c.Tenants {
				k, ok := x.keep(ctx, id, d, named)
				if !ok {
					return Keep{}, false
				}

				ks = append(ks, k)
			}

			return longest(true, ks...), len(ks) > 0
		}

	default:
		id, ok := tenantOf(ns)
		if !ok {
			if x.once("namespace " + ns) {
				log.From(ctx).WarnContext(ctx, "trail: a namespace in the archive that is not a tenant's, left alone", "namespace", ns)
			}

			return nil
		}

		return func(c Chunk) (Keep, bool) {
			d, named := kindOf(c.Kind)

			return x.keep(ctx, id, d, named)
		}
	}
}

// archive is the second clock, namespace by namespace: what is past its window
// is destroyed, and the month that can receive no more is folded into one
// chunk.
func (x *pass) archive(ctx context.Context) {
	a := x.p.Archive

	nss, err := namespaces(ctx, a)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "trail: the archive", "err", err)

		return
	}

	for _, ns := range nss {
		if ns == ReceiptNamespace {
			continue
		}

		judge := x.judge(ctx, ns)
		if judge == nil {
			continue
		}

		cs, err := chunksIn(ctx, a, ns)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "trail: the archive", "namespace", ns, "err", err)

			continue
		}

		type key struct{ kind, month, tenants string }
		groups := map[key][]Chunk{}
		destroyed := 0
		for _, c := range cs {
			k, ok := judge(c)
			if !ok {
				continue
			}

			if before := x.now.Add(-k.Destroy); k.Destroy > 0 && c.Before(before) {
				if err := a.Use(ns).Erase(ctx, c.Digest); err != nil {
					log.From(ctx).WarnContext(ctx, "trail: the archive", "namespace", ns, "chunk", c.String(), "err", err)

					continue
				}

				x.r.chunk("destroy", c, before)
				destroyed++

				continue
			}

			if ns != LegacyNamespace {
				g := key{c.Kind, Month(c.Month), joined(c.Tenants)}
				groups[g] = append(groups[g], c)
			}
		}
		if destroyed > 0 {
			log.From(ctx).InfoContext(ctx, "trail: the archive", "namespace", ns, "destroyed", destroyed)
		}

		gs := slices.Collect(maps.Keys(groups))
		sort.Slice(gs, func(i, j int) bool {
			return gs[i].kind+gs[i].month+gs[i].tenants < gs[j].kind+gs[j].month+gs[j].tenants
		})
		for _, g := range gs {
			vs := groups[g]
			if len(vs) < 2 {
				continue
			}

			k, _ := judge(vs[0])
			if !x.closed(vs[0], k) {
				continue
			}

			if _, err := merge(ctx, a, x.run, vs); err != nil {
				log.From(ctx).WarnContext(ctx, "trail: folding a month of the archive",
					"namespace", ns, "kind", g.kind, "month", g.month, "err", err)
			}
		}
	}
}

// closed answers whether a chunk's month can receive no more rows: every row
// written in it has already left the database.
//
// A row written late -- a replica behind, a clock that stepped -- can still
// arrive after, and is a chunk of its own until the next fold. That is the
// direction to be wrong in: a month folded too early is folded twice, and one
// folded never is a blob a day forever.
func (x *pass) closed(c Chunk, k Keep) bool {
	if k.Retain <= 0 {
		return true
	}

	return !c.Month.AddDate(0, 1, 0).After(x.now.Add(-k.Retain))
}

// Removal is how many rows of one kind one pass would take.
type Removal struct {
	// Archived is the rows that would leave the database and be kept in the
	// archive.
	Archived int

	// Discarded is the rows that would leave the database and stop existing:
	// the ones with no copy kept, and the ones already past their whole
	// window.
	Discarded int

	// Destroyed is the archived rows that would be destroyed.
	Destroyed int
}

// Preview is what one pass would take out of one tenant's trail, if the app
// answered `t` for it, by the name of each kind.
//
// For a warning before a plan changes and for a dashboard after: *shortening
// this to a year removes this much, of these kinds*. It changes nothing, and it
// is the whole of what a pass would do under that answer, not the difference
// from what it does now -- preview the answer it gives now for that.
//
// Exact for the rows that are the tenant's alone, which is nearly all of them,
// and for the ones it shares, which it reads one by one with the other
// tenants' answers as they are now. A chunk holds rows of a month, and goes
// when the newest of them does, so a row past its window can outlast it by
// the rest of its month; a preview counts it as gone.
func (p Policy) Preview(ctx context.Context, s Store, tenant pdid.Id, t Tenant) (map[string]Removal, error) {
	if tenant.IsZero() {
		return nil, fmt.Errorf("no tenant")
	}

	x, err := p.start(s)
	if err != nil {
		return nil, err
	}

	x.answers[tenant] = answer{t: t}

	out := map[string]Removal{}

	// The database, kind by kind, for the rows that are its alone.
	ds := slices.Collect(maps.Keys(pdid.Domains()))
	if !slices.Contains(ds, pdid.Unknown) {
		ds = append(ds, pdid.Unknown)
	}
	slices.Sort(ds)

	type bucket struct {
		kind string
		of   Kinds
		k    Keep
	}

	bs := []bucket{}
	for _, d := range ds {
		k, ok := x.keep(ctx, tenant, d, true)
		if !ok {
			return nil, fmt.Errorf("%s: the answer contradicts itself: %s", nameOf(d), x.p.resolve(t, d).contradiction())
		}

		bs = append(bs, bucket{nameOf(d), Only(d), k})
	}

	// And whatever kinds the rows hold that the schema no longer registers.
	if k, ok := x.keep(ctx, tenant, pdid.Unknown, false); ok {
		bs = append(bs, bucket{"*", Except(ds...), k})
	}

	for _, b := range bs {
		k := b.k
		if k.Retain <= 0 {
			continue
		}

		of := Scope{Kinds: b.of, Whose: Alone, Tenant: tenant}
		n, err := s.Count(ctx, of, x.now.Add(-k.Retain))
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}

		gone := 0
		switch {
		case k.Discard || p.Archive == nil:
			gone = n
		case k.Destroy > 0:
			if gone, err = s.Count(ctx, of, x.now.Add(-k.Destroy)); err != nil {
				return nil, err
			}
		}

		v := out[b.kind]
		v.Discarded += gone
		v.Archived += n - gone
		out[b.kind] = v
	}

	// The rows it shares, one by one.
	at := Cursor{}
	for {
		vs, err := s.Older(ctx, Scope{Whose: Together, Tenant: tenant}, x.now, at, Batch)
		if err != nil {
			return nil, err
		}

		for _, v := range vs {
			at = Cursor{Created: v.Created, Key: v.Key}

			k, ok := x.row(ctx, v)
			if !ok || k.Retain <= 0 || !v.Created.Before(x.now.Add(-k.Retain)) {
				continue
			}

			r := out[nameOf(v.Domain)]
			if k.Discard || p.Archive == nil || (k.Destroy > 0 && v.Created.Before(x.now.Add(-k.Destroy))) {
				r.Discarded++
			} else {
				r.Archived++
			}
			out[nameOf(v.Domain)] = r
		}
		if len(vs) < Batch {
			break
		}
	}

	if p.Archive == nil {
		return out, nil
	}

	// The archive: its own namespace, and what it shares.
	for _, ns := range []string{tenant.String(), SharedNamespace} {
		judge := x.judge(ctx, ns)

		cs, err := chunksIn(ctx, p.Archive, ns)
		if err != nil {
			return nil, err
		}

		for _, c := range cs {
			if ns == SharedNamespace && !slices.Contains(c.Tenants, tenant) {
				continue
			}

			k, ok := judge(c)
			if !ok || k.Destroy <= 0 || !c.Before(x.now.Add(-k.Destroy)) {
				continue
			}

			kind := c.Kind
			if kind == "" {
				kind = "*"
			}

			r := out[kind]
			r.Destroyed += c.Rows
			out[kind] = r
		}
	}

	return out, nil
}

// Profile is a starting point with its arithmetic written down.
//
// # What this is and what it is not
//
// It is **not** a compliance guarantee and cannot be. What a deployment is
// obliged to keep depends on what it processes, for whom, and where -- none of
// which payday knows, and some of which is decided by a regulator reading the
// facts of a particular business. Two deployments of the same app can be under
// different rules, and so can two **entities** of one deployment, which is what
// [Policy] is per-kind for.
//
// What it is: the number somebody would otherwise look up, beside the sentence
// it comes from, so that the value in a configuration file is arguable rather
// than arbitrary. `61320h` is unreadable; *seven years, because 17 CFR 210.2-06
// says seven years* is a thing a reviewer can disagree with.
//
// A profile fills in only what a deployment left blank. Writing `destroy:`
// beside a profile wins, deliberately: the deployment knows something this
// table does not, and a framework overriding it would be the table pretending
// to the authority it just disclaimed.
//
// Most of these are, read closely, **floors** -- *at least a year* -- and a
// profile names one as well as a window; see [Profile.OverFloor].
type Profile struct {
	// Retain and Destroy are what the profile suggests.
	Retain  time.Duration
	Destroy time.Duration

	// Why is where the numbers come from, in one line.
	Why string
}

const (
	day  = 24 * time.Hour
	year = 365 * day
)

// Profiles is what a deployment may name in `profile:`, on the whole trail or
// on one kind of thing.
//
// The retention half is ninety days throughout, which is not a citation -- it
// is the ordinary operational answer to *how much should a query have to scan*,
// and PCI is the one regime here that puts a number on the hot half. The
// destruction half is where the regimes actually differ, and each one is the
// figure its own rule gives.
var Profiles = map[string]Profile{
	"pci": {
		Retain:  90 * day,
		Destroy: 1 * year,
		Why:     "PCI-DSS 10.5.1: one year of audit history, the last three months immediately available",
	},
	"hipaa": {
		Retain:  90 * day,
		Destroy: 6 * year,
		Why:     "HIpAA 45 CFR 164.316(b)(2)(i): documentation retained six years",
	},
	"sox": {
		Retain:  90 * day,
		Destroy: 7 * year,
		Why:     "SOX, via 17 CFR 210.2-06: audit records retained seven years",
	},
	"pipa": {
		Retain:  90 * day,
		Destroy: 1 * year,
		Why:     "개인정보의 안전성 확보조치 기준: access records kept at least one year",
	},
	"pipa-sensitive": {
		Retain:  90 * day,
		Destroy: 2 * year,
		Why:     "개인정보의 안전성 확보조치 기준: two years for unique identifiers, sensitive data, or a larger processor",
	},
	"gdpr": {
		Retain:  90 * day,
		Destroy: 2 * year,
		Why: "GDPR names no figure -- Article 5(1)(e) asks for a stated limit rather than a particular one, " +
			"and two years is a convention rather than a citation. Argue with it",
	},
	"forever": {
		Why: "kept, on purpose: an operating record of what a machine did is not " +
			"personal data and usually has the opposite requirement",
	},
}

// NamedProfile answers a profile, and refuses one that is not there.
//
// Refused rather than ignored: a deployment that meant `pci` and wrote `pci-dss`
// has configured a retention policy that silently does nothing, which is the
// failure this whole package exists to make loud.
func NamedProfile(v string) (Profile, error) {
	p, ok := Profiles[strings.ToLower(strings.TrimSpace(v))]
	if ok {
		return p, nil
	}

	names := make([]string, 0, len(Profiles))
	for k := range Profiles {
		names = append(names, k)
	}
	sort.Strings(names)

	return Profile{}, fmt.Errorf("profile: %q is not one this knows; it has %s",
		v, strings.Join(names, ", "))
}

// Over fills a kind's blanks from this profile and leaves what was written
// alone.
func (v Profile) Over(k Keep) Keep {
	if k.Retain == 0 {
		k.Retain = v.Retain
	}
	if k.Destroy == 0 {
		k.Destroy = v.Destroy
	}

	k.Note = v.Why

	return k
}

// OverFloor fills a floor's blanks from this profile, and refuses a profile
// that is no floor at all.
//
// `forever` is the one that is not: it says nothing on either clock, which as a
// window is forever and as a floor is nothing. A deployment that means *never
// destroy this kind* says so as the kind's window, where it means that.
func (v Profile) OverFloor(f Floor) (Floor, error) {
	if v.Retain == 0 && v.Destroy == 0 {
		return Floor{}, fmt.Errorf("profile: this one is a window of forever and no floor; " +
			"to keep a kind forever, say so in its own block rather than under min")
	}

	if f.Retain == 0 {
		f.Retain = v.Retain
	}
	if f.Destroy == 0 {
		f.Destroy = v.Destroy
	}

	f.Note = v.Why

	return f, nil
}
