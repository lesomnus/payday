package trail

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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
	// Its own field rather than an empty archive directory, because those are
	// two different states that look alike: *I have not configured where* and
	// *I do not want one*. A blank field that defaults to destruction is the
	// configuration mistake that is discovered by an auditor.
	Discard bool

	// Destroy is how long the archive holds them once they leave the database.
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

// Policy is what a deployment keeps, and it is **per kind of thing**.
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
type Policy struct {
	// Archive is the one directory this deployment writes to. Empty keeps no
	// copy of anything, which is refused for any kind whose `Discard` does not
	// say so.
	Archive string

	// Every is how often the policy is applied. Empty is [Swept].
	Every time.Duration

	// Keep is what a kind nothing was said about gets.
	Keep Keep

	// By is the kinds that differ, by domain.
	By map[pdid.Domain]Keep
}

// For is the policy that applies to one kind of thing.
func (p Policy) For(d pdid.Domain) Keep {
	if v, ok := p.By[d]; ok {
		return v
	}

	return p.Keep
}

// Named is every kind this policy says something about, in domain order.
func (p Policy) Named() []pdid.Domain {
	out := make([]pdid.Domain, 0, len(p.By))
	for d := range p.By {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}

// On answers whether there is anything to do.
func (p Policy) On() bool {
	if p.Keep.On() {
		return true
	}
	for _, v := range p.By {
		if v.On() {
			return true
		}
	}

	return false
}

// Valid refuses a policy that would destroy something nobody asked it to.
//
// Meant to be read where the process comes up rather than at the first sweep,
// which is what makes it worth having: a deployment that has named a window and
// no archive learns about it while somebody is watching, and not a day later
// when the first pass has already run.
func (p Policy) Valid() error {
	if err := p.valid("audit", p.Keep); err != nil {
		return err
	}
	for _, d := range p.Named() {
		if err := p.valid("audit.by."+nameOf(d), p.By[d]); err != nil {
			return err
		}
	}

	return nil
}

func (p Policy) valid(at string, k Keep) error {
	if k.Retain > 0 && p.Archive == "" && !k.Discard {
		return fmt.Errorf("%s.retain names a window and audit.archive names nowhere to put what leaves it; "+
			"set audit.archive, or %s.discard: true to say the rows are meant to go", at, at)
	}
	if k.Destroy > 0 && p.Archive == "" {
		return fmt.Errorf("%s.destroy is how long the archive is kept and audit.archive names none", at)
	}
	if k.Destroy > 0 && k.Discard {
		return fmt.Errorf("%s.discard keeps no archive and %s.destroy says how long to keep one", at, at)
	}
	if k.Destroy > 0 && k.Retain > 0 && k.Destroy < k.Retain {
		return fmt.Errorf("%s.destroy is shorter than %s.retain, so a row would be destroyed before it left the database", at, at)
	}

	return nil
}

// String is the policy as the line a process logs as it comes up.
func (p Policy) String() string {
	out := []string{"anything else: " + p.Keep.String()}
	for _, d := range p.Named() {
		out = append(out, nameOf(d)+": "+p.By[d].String())
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
// the archive, which [Read] drops, and a `Forget` that finds nothing, which is
// not an error. What it must not cost is two writers in one file, which is what
// the run in an archive's name is for.
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
// One pass per kind that was named, and one for everything else. Which is why
// [Store.Older] takes a [Kinds] rather than a domain: the default pass is
// *everything but these*, and a database answers that in one query where a loop
// over the domains it has never heard of cannot.
func (p Policy) Pass(ctx context.Context, s Store) {
	named := p.Named()

	for _, d := range named {
		p.pass(ctx, s, Only(d), p.By[d], nameOf(d))
	}

	p.pass(ctx, s, Except(named...), p.Keep, "anything else")
}

// pass is one kind's window and one kind's archive, and it logs rather than
// fails: a database that blinked is a thing to try again, and taking the
// process down would be a retention policy that is also an outage.
func (p Policy) pass(ctx context.Context, s Store, of Kinds, k Keep, name string) {
	if k.Retain > 0 {
		before := time.Now().Add(-k.Retain)

		dir := p.Archive
		if k.Discard {
			dir = ""
		}

		n, err := Leave(ctx, s, of, before, dir)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "trail: the retention window", "kind", name, "err", err, "before", before)
		} else if n > 0 {
			log.From(ctx).InfoContext(ctx, "trail: the retention window",
				"kind", name, "moved", n, "before", before, "kept", dir != "")
		}
	}
	if k.Destroy > 0 {
		before := time.Now().Add(-k.Destroy)

		vs, err := Purge(ctx, p.Archive, of.cut(before))
		if err != nil {
			log.From(ctx).WarnContext(ctx, "trail: the archive", "kind", name, "err", err, "before", before)
		} else if len(vs) > 0 {
			log.From(ctx).InfoContext(ctx, "trail: the archive", "kind", name, "destroyed", len(vs), "before", before)
		}
	}
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
		Why:     "HIPAA 45 CFR 164.316(b)(2)(i): documentation retained six years",
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
