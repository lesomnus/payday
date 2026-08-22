package config

import (
	"fmt"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
)

// AuditConfig is how long the trail is kept, and where what leaves it goes.
//
// `schema/payday/audit.proto` asks for this in as many words -- *an app with an
// obligation to destroy data has to reckon with the trail, and the answer is a
// retention policy rather than an empty column* -- and until there was one, the
// answer every payday app gave was **forever**, arrived at by not deciding, on
// the one table that never stops growing.
//
// # Two clocks, per kind of thing
//
// `retain` is operational: how much of the trail the database carries, which is
// what a console can show and what a query costs. `destroy` is the obligation,
// and it is normally years the longer. `archive` is where a row lives out the
// difference.
//
// And neither is uniform across a deployment's entities, which is why `by:` is
// here from the start. What was done to a person is under a privacy regime and
// eventually has to stop existing; what a machine did is an operating record
// with the opposite requirement. One clock over the whole table forces the
// shorter of the two onto everything.
//
//	audit:
//	  profile: pipa
//	  archive: /var/lib/roster/audit
//	  by:
//	    robot:
//	      profile: forever
//	    holder:
//	      profile: gdpr
//
// Empty is forever, which is what a deployment that has not thought about it
// gets, and is the only honest default: a version upgrade is not the right
// thing to decide how long somebody's evidence lasts.
type AuditConfig struct {
	// Profile fills the two clocks in from a named regime -- `pci`, `hipaa`,
	// `sox`, `pipa`, `pipa-sensitive`, `gdpr`, `forever`. See [trail.Profiles],
	// which carries the sentence each number comes from.
	//
	// It is a **starting point and not a guarantee**, for the reason written
	// there: what a deployment is obliged to keep depends on what it processes
	// and for whom, and two deployments of one app can be under different
	// rules. What a profile buys is that the number in the file is arguable
	// rather than arbitrary.
	//
	// Anything written beside it wins. A deployment that names a profile and
	// then sets `destroy:` knows something the table does not.
	Profile string `yaml:"profile"`

	// Retain is how long a row stays in the database, e.g. `2160h` for ninety
	// days. Empty takes the profile's, and then forever.
	Retain time.Duration `yaml:"retain"`

	// Archive is the one directory rows are written to on their way out of the
	// database, for every kind. Empty keeps no copy, which is refused unless
	// `discard` says the deployment means it.
	Archive string `yaml:"archive"`

	// Discard is a deployment saying it means to keep nothing.
	//
	// Its own setting rather than an empty `archive`, because those are two
	// different states that look alike: *I have not configured where* and *I do
	// not want one*. A blank field that defaults to destruction is the
	// configuration mistake that gets discovered by an auditor.
	Discard bool `yaml:"discard"`

	// Destroy is how long an archive file is kept once it is written. Empty
	// takes the profile's, and then forever.
	Destroy time.Duration `yaml:"destroy"`

	// Every is how often the policy is applied, for every kind. Empty is
	// [trail.Swept], a day.
	Every time.Duration `yaml:"every"`

	// By is the kinds that are not like the rest, keyed by the name the schema
	// registered -- `holder`, `robot`, `audit`. A kind named here that this app
	// does not have is refused rather than ignored.
	By map[string]AuditKeepConfig `yaml:"by"`
}

// AuditKeepConfig is one kind's two clocks.
//
// It has no `archive` and no `every`: where the files go and how often the
// policy runs are the deployment's, and one directory read by one loop is what
// makes an archive readable as a whole. What differs per kind is how long.
type AuditKeepConfig struct {
	// Profile is a named regime for this kind alone. `forever` is one of them,
	// which is the usual answer for a record of what a machine did.
	Profile string `yaml:"profile"`

	// Retain is how long a row of this kind stays in the database.
	Retain time.Duration `yaml:"retain"`

	// Discard removes this kind with no copy kept, whatever `audit.archive`
	// says.
	Discard bool `yaml:"discard"`

	// Destroy is how long the archive holds this kind.
	Destroy time.Duration `yaml:"destroy"`
}

// Policy is this block as the thing that applies it, resolved and checked.
//
// Both here rather than in a `Valid` somebody has to remember to call, which is
// the idiom every other block in this package already has: a refusal lives
// inside the call that makes the object. What it refuses is a policy that would
// destroy something nobody asked it to, and a kind this app does not have --
// see [trail.Policy.Valid]. Read it where the process comes up rather than at
// the first sweep a day later.
func (c AuditConfig) Policy() (trail.Policy, error) {
	keep, err := keepOf(c.Profile, trail.Keep{
		Retain:  c.Retain,
		Discard: c.Discard,
		Destroy: c.Destroy,
	})
	if err != nil {
		return trail.Policy{}, fmt.Errorf("audit.%w", err)
	}

	p := trail.Policy{
		Archive: c.Archive,
		Every:   c.Every,
		Keep:    keep,
	}

	if len(c.By) > 0 {
		p.By = map[pdid.Domain]trail.Keep{}
	}
	for name, v := range c.By {
		d, err := trail.DomainOf(name)
		if err != nil {
			return trail.Policy{}, fmt.Errorf("audit.by: %w", err)
		}

		k, err := keepOf(v.Profile, trail.Keep{
			Retain:  v.Retain,
			Discard: v.Discard,
			Destroy: v.Destroy,
		})
		if err != nil {
			return trail.Policy{}, fmt.Errorf("audit.by.%s.%w", name, err)
		}

		p.By[d] = k
	}

	if err := p.Valid(); err != nil {
		return trail.Policy{}, err
	}

	return p, nil
}

func keepOf(profile string, k trail.Keep) (trail.Keep, error) {
	if profile == "" {
		return k, nil
	}

	v, err := trail.NamedProfile(profile)
	if err != nil {
		return trail.Keep{}, err
	}

	return v.Over(k), nil
}
