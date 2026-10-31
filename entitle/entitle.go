// Package entitle keeps a tenant's copy of what it is entitled to, in the
// app's own database, and reads it inside the transaction of the write it
// decides (payday#185).
//
// # Why a copy, read in the transaction
//
// A billing service decides what a tenant may have -- a feature, a number of
// something -- and the app enforces it. There are two obvious ways to check,
// and neither works. Asking the service on every call makes every write depend
// on it, and is slow. Checking a cached answer outside the transaction lets two
// concurrent writes both pass a limit of one: each reads that there is room,
// and each takes it.
//
// What is both fast and exact is a copy in the app's own database, read in the
// same transaction as the write that uses it -- and, for a limit, locked there,
// so that the second write waits for the first and then counts what the first
// wrote. [Limit] takes that lock; [Has], whose answer a write does not change,
// does not.
//
// # What fills it
//
// A source of grants, speaking `payday.GrantService` (`proto/payday/grant.proto`):
// a billing service, or anything else that decides. A grant is one thing a
// tenant was given -- a key, a value, a window -- and every message about one
// is all of it at a version, so the copy applies what it hears idempotently,
// never an older version over a newer. [Follower] is the loop that keeps it:
// it watches the source's stream of changes, and compares the copy with the
// whole of the source's list when the stream opens and every so often after.
// payday depends on no billing service; the contract is payday's.
//
// # When the source cannot be reached
//
// The copy goes on answering, and nothing is taken out of it for want of
// hearing. Every grant says when it ends, so a copy that stops hearing from its
// source does not keep a grant forever: once it ends it is in force no longer,
// and what the app does then -- refuse, or let a tenant on for a while -- is
// the app's policy, read off [All] and the grants it shows. The copy takes
// away only what the source said has ended, or what has ended and the source
// no longer lists.
//
// # What it answers
//
// A key no grant in force gives is not entitled -- [Has] is false, [Limit] is
// [None] -- and that is never an error: a tenant on a plan without a feature
// is the ordinary case. A grant gives a switch, on or off; a limit, a whole
// number; or [Unlimited], which is kept apart from none. When more than one
// grant of a key is in force at once -- this period's, still in its grace,
// and the next one's, begun -- the one that began last decides ([Decide]).
//
// Reads answer for the tenant the context is framed as, and refuse a context
// framed as none ([ErrNoTenant]).
//
//	drv, tx, err := dialect.BeginTx(ctx, s.drv)
//	...
//	limit, err := entitle.Limit(ctx, pd.Entitlements(drv), "acme.robots")
//	held, err := countRobots(ctx, drv)  // after the lock, so a write that won the race is counted
//	if !limit.Allows(held + 1) {
//		return status.Error(codes.FailedPrecondition, "this plan holds no more robots")
//	}
//
// # It is the tenant's
//
// The copy's rows are behind the wall by their edge to the tenant, and go
// with it: an app's offboarding takes them as it takes every row of the
// tenant's, and the trail's tenant purge their trail. A grant of a tenant the
// app does not have is applied to nothing, so a tenant destroyed is not given
// its copy back by the next grant the source says of it.
//
// Each write of the copy is on the trail as the act it is, [Applied] or
// [Forgot], by the deployment and filed under the tenant: a row nothing on the
// trail accounts for -- a tenant entitled by a hand in the database -- is what
// a check of the rows finds (`pd.Digests`).
//
// # What it is not
//
// A quota that is spent: reserving an amount per request and committing or
// releasing it, for a metered resource that must stop exactly at its limit.
// What is here is what a tenant may **hold**, counted by the app in the
// transaction that adds to it.
package entitle

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/protobuf-orm/ent/dialect"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// The acts a write of the copy is on the trail as.
const (
	// Applied is a grant written into the copy: one the copy did not have, or
	// a newer version of one it did.
	Applied = "payday.entitle.apply"

	// Forgot is a row taken out: a grant that has ended, and that the source
	// lists no more.
	Forgot = "payday.entitle.forget"
)

var (
	// ErrNoTenant is a read asked for on a context framed as no tenant's, or
	// framed as none: whose entitlements to read is the frame's to say.
	ErrNoTenant = errors.New("entitle: the context is framed as no tenant, and an entitlement is a tenant's")

	// ErrNotInTx is a limit read outside a transaction. Read so, the lock it
	// takes is let go as soon as it is taken, and the write it decides is
	// decided by what was true a statement ago -- the race the copy exists to
	// close.
	ErrNotInTx = errors.New("entitle: a limit is read in the transaction of the write it decides, and this reader is on none")
)

// Kind is which of the three a [Value] is, or none.
//
// Its numbers are those of `EntitlementValue`, the enum the copy's rows hold
// it in.
type Kind uint8

const (
	// KindNone is no grant in force.
	KindNone Kind = iota

	// KindOff and KindOn are a switch.
	KindOff
	KindOn

	// KindLimit is a whole number, zero or more.
	KindLimit

	// KindUnlimited is no bound at all.
	KindUnlimited
)

// Value is what a grant gives of its key, and the zero Value is [None]: what
// a tenant has of a key no grant in force gives.
type Value struct {
	kind Kind
	n    int64
}

var (
	// None is no value at all: not entitled.
	None = Value{}

	// Off and On are a switch, which is a feature.
	Off = Value{kind: KindOff}
	On  = Value{kind: KindOn}

	// Unlimited is no bound, which is a value and not the absence of one.
	Unlimited = Value{kind: KindUnlimited}
)

// Limited is a limit of `n`. A negative one is a limit of nothing.
func Limited(n int64) Value { return Value{kind: KindLimit, n: max(n, 0)} }

// ValueOf is the value of that kind, with `n` for a limit: what a row of the
// copy holds, read back. A kind this does not know is [None].
func ValueOf(k Kind, n int64) Value {
	switch k {
	case KindOff:
		return Off
	case KindOn:
		return On
	case KindLimit:
		return Limited(n)
	case KindUnlimited:
		return Unlimited
	}

	return None
}

// Kind is which value this is.
func (v Value) Kind() Kind { return v.kind }

// IsNone is whether this is no value at all.
func (v Value) IsNone() bool { return v.kind == KindNone }

// IsSwitch is whether this is on or off.
func (v Value) IsSwitch() bool { return v.kind == KindOn || v.kind == KindOff }

// Number is the limit, and false for a value that is none.
func (v Value) Number() (int64, bool) { return v.n, v.kind == KindLimit }

// Has is whether this entitles to anything: on, a limit above zero, or
// unlimited.
func (v Value) Has() bool {
	switch v.kind {
	case KindOn, KindUnlimited:
		return true
	case KindLimit:
		return v.n > 0
	}

	return false
}

// Allows is whether holding `n` is within this value: anything within
// [Unlimited], up to the number of a limit, and nothing above zero of none --
// nor of a switch, which is not an amount; a switch is read with [Value.Has].
func (v Value) Allows(n int64) bool {
	switch v.kind {
	case KindUnlimited:
		return true
	case KindLimit:
		return n <= v.n
	}

	return n <= 0
}

func (v Value) String() string {
	switch v.kind {
	case KindOff:
		return "off"
	case KindOn:
		return "on"
	case KindLimit:
		return strconv.FormatInt(v.n, 10)
	case KindUnlimited:
		return "unlimited"
	}

	return "none"
}

// Row is one row of the copy: one grant, as it was last applied.
type Row struct {
	// Id is the row's, and zero for one not written yet.
	Id pdid.Id

	Tenant pdid.Id
	Key    string
	Value  Value

	// From and Until are the grant's window: in force from the one, and no
	// longer from the other.
	From, Until time.Time

	// Grant and Version are the grant this copies, by the source's
	// identifier, and the version of it.
	Grant   []byte
	Version uint64
}

// In is whether the grant is in force at `at`.
func (r Row) In(at time.Time) bool { return !at.Before(r.From) && at.Before(r.Until) }

// Ended is whether the grant is over at `at`: in force no longer, and never
// again. One that has not begun has not ended.
func (r Row) Ended(at time.Time) bool { return !at.Before(r.Until) }

// Entitlement is what a tenant has of one key at one moment: the value of the
// grant that decides it, and that grant.
type Entitlement struct {
	Key   string
	Value Value

	// Grant is the row of the grant that decides it, and nil when no grant of
	// the key is in force -- when Value is [None].
	Grant *Row
}

// Decide is what `rows` give of `key` at `at`: of the rows of that key in
// force, the one that began last, and of two that began at once the one whose
// grant's identifier is the greater.
//
// The one that began last, because that is the source saying what comes next:
// a plan changed at the end of a period is the next period's grant begun while
// this one's grace runs, and the change is what holds from then -- a downgrade
// included, which the most generous of the two would put off until the grace
// was over. More than one in force is otherwise the source's to avoid: a grant
// is the whole of what a tenant has of its key while it decides, and an add-on
// is a new total rather than a second grant beside the first.
func Decide(key string, rows []Row, at time.Time) Entitlement {
	var by *Row
	for i := range rows {
		r := &rows[i]
		if r.Key != key || !r.In(at) {
			continue
		}
		if by == nil || r.From.After(by.From) || (r.From.Equal(by.From) && bytes.Compare(r.Grant, by.Grant) > 0) {
			by = r
		}
	}
	if by == nil {
		return Entitlement{Key: key}
	}

	v := *by
	return Entitlement{Key: key, Value: v.Value, Grant: &v}
}

// Reader is the copy as a read sees it, on the driver of the transaction it
// reads in: `pd.Entitlements(drv)`.
type Reader interface {
	// Driver is what it reads on.
	Driver() dialect.Driver

	// Rows are the copy's rows of `key` that `tenant` holds -- of every key,
	// when `key` is empty -- in the order of their identifiers. With `lock`
	// they are locked until the transaction ends, on a database that locks a
	// row; SQLite takes one writer at a time, so a transaction that writes
	// already holds what a lock would take.
	Rows(ctx context.Context, tenant pdid.Id, key string, lock bool) ([]Row, error)
}

// Has answers whether the tenant `ctx` is framed as is entitled to `key` now:
// whether a grant of it in force is on, a limit above zero, or unlimited. A
// key no grant in force gives is false, and not an error.
//
// It takes no lock. A switch is not used up, so a write it lets through does
// not change what the next one is told, and two of them racing are both
// right; a limit is [Limit]'s.
func Has(ctx context.Context, r Reader, key string) (bool, error) {
	v, err := Get(ctx, r, key)
	if err != nil {
		return false, err
	}

	return v.Value.Has(), nil
}

// Limit answers how much of `key` the tenant `ctx` is framed as may hold now:
// [Limited], [Unlimited], or [None] when no grant of it is in force -- whose
// [Value.Allows] is nothing above zero, and which is not an error.
//
// It locks the rows of the key until the transaction ends, so that a second
// write deciding by the same limit waits for this one and counts what it
// wrote; read the count after it. Outside a transaction it refuses
// ([ErrNotInTx]), since a lock let go at once is the race it is there to
// close.
//
// A key granted as a switch is an error: the source and the app disagree on
// what it is, and no amount read off a switch is the right one.
func Limit(ctx context.Context, r Reader, key string) (Value, error) {
	tenant, err := tenantOf(ctx, key)
	if err != nil {
		return None, err
	}
	if !dialect.InTx(r.Driver()) {
		return None, ErrNotInTx
	}

	rows, err := r.Rows(ctx, tenant, key, true)
	if err != nil {
		return None, fmt.Errorf("entitle: %s: %w", key, err)
	}

	v := Decide(key, rows, time.Now()).Value
	if v.IsSwitch() {
		return None, fmt.Errorf("entitle: %s is granted as a switch, on or off, and read as a limit: "+
			"the source and the app disagree on what it is", key)
	}

	return v, nil
}

// Get answers what the tenant `ctx` is framed as has of `key` now, and the
// grant that says so: for showing it -- `3 of 5` -- and not for deciding a
// write by a number, which is [Limit]'s. It takes no lock.
func Get(ctx context.Context, r Reader, key string) (Entitlement, error) {
	tenant, err := tenantOf(ctx, key)
	if err != nil {
		return Entitlement{Key: key}, err
	}

	rows, err := r.Rows(ctx, tenant, key, false)
	if err != nil {
		return Entitlement{Key: key}, fmt.Errorf("entitle: %s: %w", key, err)
	}

	return Decide(key, rows, time.Now()), nil
}

// All answers every grant the copy holds of the tenant `ctx` is framed as --
// in force, ended, and not yet begun -- by key and then by when each begins:
// what a page showing a tenant its plan reads, and what an app's own policy
// for a grant that ended while the source could not be heard from looks at.
// [Decide] says which of them is in force at a moment.
func All(ctx context.Context, r Reader) ([]Row, error) {
	tenant, err := tenantOf(ctx, "")
	if err != nil {
		return nil, err
	}

	rows, err := r.Rows(ctx, tenant, "", false)
	if err != nil {
		return nil, fmt.Errorf("entitle: %w", err)
	}
	slices.SortStableFunc(rows, func(a, b Row) int {
		if c := cmp.Compare(a.Key, b.Key); c != 0 {
			return c
		}

		return a.From.Compare(b.From)
	})

	return rows, nil
}

// tenantOf is the tenant `ctx` is framed as, and refuses a key that is no key:
// one the source could never have granted, read as none every time, which is a
// typo nothing else would point at.
func tenantOf(ctx context.Context, key string) (pdid.Id, error) {
	if key != "" {
		if err := CheckKey(key); err != nil {
			return pdid.Nil, err
		}
	}

	f, ok := frame.From(ctx)
	if !ok || f.Tenant.IsZero() {
		return pdid.Nil, ErrNoTenant
	}

	return f.Tenant, nil
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// CheckKey refuses what is not a key: lower case words of letters, digits and
// `_`, each beginning with a letter, joined by dots, at least two of them, and
// at most 255 bytes -- the app's name first, as a meter's key is written:
// `acme.export`, `acme.robots`.
//
// Two words at least, because the first is the app's: a source sells for
// several apps, and a key is the one name they share.
func CheckKey(key string) error {
	switch {
	case key == "":
		return errors.New("entitle: a grant has no key")
	case len(key) > 255:
		return fmt.Errorf("entitle: %q is longer than the 255 bytes a key may be", key)
	case !keyPattern.MatchString(key):
		return fmt.Errorf("entitle: %q is not a key: lower case words of letters, digits and `_`, joined by dots, "+
			"the app's name first -- `acme.export`", key)
	}

	return nil
}
