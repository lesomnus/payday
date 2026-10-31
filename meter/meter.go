// Package meter measures what each tenant holds -- its assets, its members,
// the bytes it stores -- and hands the readings to a sink, which is the
// billing service's client (payday#186).
//
// # Where the numbers come from
//
// From the app's own tables, measured by the app. The two other ways both fail
// without a word. A billing service that reads the app's database is tied to
// its schema: rename a column or add a status, and it counts the wrong rows
// while nothing fails. Monitoring metrics are not a billing source: a sample
// lost cannot be told from one doubled, and nothing can compute them again.
//
// So an app declares what it is paid by in its schema (`payday.Meter`), and
// `pd gen` holds each declaration to the entity it names and writes the query
// that measures it -- `pd.Meters`, a [Meter] for each. What a declaration
// cannot say is a [Meter] the app writes: a function from a tenant to a value.
// A [Reader] measures them all, tenant by tenant, on a schedule, and the app
// asks it to measure one tenant now ([Reader.Read]) before it destroys that
// tenant, so that the last period is measured while its source is still there.
//
// Nothing outside the app reads its tables, and the app writes no rows for
// metering: a reading is handed over and kept by whoever it is handed to.
//
// # A reading counts once
//
// A [Reading] is a tenant, a meter, the moment it was measured and the value.
// The first three are what it is: a sink keeps one reading of each, so a
// reading handed over twice -- a retry of a call that did reach it, a pass run
// again around a failover -- counts once. The value goes with the moment, so
// the same three never come with two values.
//
// # What it does not measure
//
// What happens over time rather than what is held: bytes sent, calls made.
// There is no row to count, so a flow is counted where it happens, as an
// OpenTelemetry Sum that is monotonic and cumulative, named for its meter key,
// with the tenant as [TenantAttribute] -- exported to the billing service's own
// receiver, and never through the monitoring pipeline. A cumulative point sent
// twice changes nothing and one lost is covered by the next; what a process
// counted after its last export is lost if it dies, which bills less and never
// more. `docs/guide/meters.md` has the whole convention.
package meter

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/lesomnus/payday/pdid"
)

// Meter is one thing a tenant holds: what a reading of it is filed under, what
// it counts in, and how it is measured.
//
// `pd.Meters` answers with one for each meter the schema declares. One the app
// writes is a value of this, for what a declaration cannot say: a retention
// window read off the tenant and the date a shorter one takes effect, the bytes
// a storage inventory says a tenant holds.
type Meter struct {
	// Key is what a reading is filed under, which is what a billing service
	// prices: lower case words joined by dots, the app's name first --
	// `acme.robots.running`. See [CheckKey].
	Key string

	// Unit is what a value counts in, as OpenTelemetry writes one (UCUM):
	// `By`, `{robot}`, `d`. See [CheckUnit].
	Unit string

	// Measure is how much of it `tenant` holds now.
	//
	// It is called for every tenant on every pass, and from [Reader.Read]
	// whenever an app asks, so it is safe to call from two goroutines at
	// once. An error is this meter of this tenant not measured this time; the
	// others are.
	Measure func(ctx context.Context, tenant pdid.Id) (int64, error)
}

// Reading is what one tenant held of one meter, at one moment.
type Reading struct {
	// Tenant, Meter and MeasuredAt are what the reading is: a sink keeps one of
	// each, and one handed over twice counts once. See the package comment.
	Tenant pdid.Id
	Meter  string

	// MeasuredAt is when it was measured: UTC, to the microsecond, which is
	// what PostgreSQL keeps of a time -- so a sink that keeps it there keeps
	// exactly what it was handed, and a reading handed over again is the same
	// reading to it. Every reading of one tenant from one measuring has the
	// same one.
	//
	// It is the moment it was read and nothing rounder. A reading is not of a
	// day or a month: which period it falls in is the billing service's to
	// say, in whatever zone the bill is in.
	MeasuredAt time.Time

	Value int64

	// Unit is the meter's, carried so that a sink can refuse a reading whose
	// unit is not the one it prices the meter in. It is not part of what the
	// reading is: a meter does not change its unit under one key.
	Unit string
}

// Sink takes readings: the billing service's client, which the app wires.
type Sink interface {
	// Send hands over one tenant's readings, all from one measuring. An error
	// is the sink not having them. A [Reader] hands the same readings over
	// again later, so a sink keeps one of each ([Reading]) and takes a second
	// one as the first.
	//
	// It is given the reader's context, which ends when the app stops; a sink
	// that can wait on the network sets a deadline of its own.
	Send(ctx context.Context, rs []Reading) error
}

// SinkFunc is a [Sink] made of a function.
type SinkFunc func(ctx context.Context, rs []Reading) error

func (f SinkFunc) Send(ctx context.Context, rs []Reading) error { return f(ctx, rs) }

// TenantAttribute is the attribute a flow's OpenTelemetry Sum carries the
// tenant in, as the canonical text of its identifier: what a billing service's
// receiver reads a point's tenant off. See the package comment.
const TenantAttribute = "payday.tenant"

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// CheckKey refuses what is not a meter's key: lower case words of letters,
// digits and `_`, each beginning with a letter, joined by dots, at least two of
// them, and at most 255 bytes.
//
// Two words at least, because the first is the app's: one billing service is
// handed the readings of several apps, and a key is the one name they share.
// And the shape is one an OpenTelemetry instrument may be named, so that a
// flow's Sum is named by its meter's key as it is.
func CheckKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("meter: a meter has no key")
	case len(key) > 255:
		return fmt.Errorf("meter: %q is longer than the 255 bytes a key may be", key)
	case !keyPattern.MatchString(key):
		return fmt.Errorf("meter: %q is not a key: lower case words of letters, digits and `_`, joined by dots, "+
			"the app's name first -- `acme.robots.running`", key)
	}

	return nil
}

// CheckUnit refuses what is not a meter's unit: 1 to 63 bytes of printable
// ASCII with no space, which is what OpenTelemetry allows a unit and what UCUM
// writes one in.
func CheckUnit(unit string) error {
	switch {
	case unit == "":
		return fmt.Errorf("meter: says no unit, and a number with no unit is one nobody can price -- `By`, `{robot}`, `d`")
	case len(unit) > 63:
		return fmt.Errorf("meter: unit %q is longer than the 63 bytes OpenTelemetry allows one", unit)
	}
	for _, c := range []byte(unit) {
		if c < '!' || c > '~' {
			return fmt.Errorf("meter: unit %q is not printable ASCII without spaces, as UCUM writes one", unit)
		}
	}

	return nil
}
