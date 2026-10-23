package past

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// ExpireAction is what a row an expiry took is on the trail as, when
// [Expiry.Act] says nothing.
const ExpireAction = "payday.past.expire"

// Window is how long one tenant keeps its past, as the app answers for it.
//
// # Whose it is
//
// The app's, as the trail's windows are (`trail.Policy.Tenants`): how long a
// tenant keeps its history follows from what it agreed to or said itself -- a
// plan, a contract, a setting of its own -- and that is the app's data, which
// payday stores none of. payday asks, and does the arithmetic.
//
// Answer it and the trail's windows from **one** source. The trail keeps the
// writes that made the past, so two answers are a trail that outlives the past
// it records, holding as values what the past let go, or a past whose making
// the trail has already forgotten.
type Window struct {
	// Keep is how long: what was over before `now` less Keep goes. Zero is
	// forever, and nothing goes.
	Keep time.Duration

	// Holds is what the legal holds on the tenant are for. While there is
	// one nothing goes, whatever Keep says.
	Holds []string
}

// Held says a legal hold is on.
func (w Window) Held() bool { return len(w.Holds) > 0 }

// Expiry is what an app says of its past's expiry beside the window: what no
// window ends, and what is said of what one took. The zero value keeps nothing
// past the window, and records no receipt.
type Expiry struct {
	// Act is what each row taken is on the trail as: [ExpireAction] when it
	// is empty.
	Act string

	// KeepFacts are facts no window ends: a value that is the record of
	// something, which a later value does not make history -- the status that
	// took a thing out of use, the day it was bought.
	//
	// It keeps a fact from going because a later one replaced it, and only
	// that: a fact a correction superseded goes as any other, since what was
	// corrected was never the record.
	KeepFacts []FactKeep

	// KeepKinds are the kinds of event no window ends: the operation that
	// made a thing, the one that disposed of it. A kind no message declares
	// any more is one rows were written under once, and may be kept.
	KeepKinds []string

	// Receipt is the event an expiry records of what it took, and nil records
	// none. It is asked whenever a window applied -- not under a hold, nor for
	// a window of forever -- once the rows went, and an answer of nil records
	// nothing that time: an app that answers only when something went records
	// a receipt for each expiry that took anything.
	//
	// It says how many of what, and never what they said: the rows went, and a
	// receipt that kept them would be where they went. An app whose own rows
	// went first says so in the same message.
	Receipt func(Expired) proto.Message
}

func (x Expiry) act() string {
	if x.Act == "" {
		return ExpireAction
	}

	return x.Act
}

// FactKeep is facts of one field of one kind of resource that no window ends:
// every one of them, or the ones holding one of [FactKeep.Values].
type FactKeep struct {
	// Domain is the kind of resource, and Field which of its fields.
	Domain pdid.Domain
	Field  protoreflect.FieldNumber

	// Key is the entry of a map field, as a fact's `key` holds it -- a string
	// as it is, a number as its decimal -- and empty for any other field.
	Key string

	// Values are the values kept, each as [Encode] writes it -- `"retired"`
	// for a string, `3` for an enum -- and none for every value. A value
	// written any other way is refused: a fact's value is compared as text,
	// and it would match nothing.
	Values []string
}

// Expired is what an expiry took out of one tenant's past, or would take
// ([Preview]): how many, and never what the rows said.
type Expired struct {
	Tenant pdid.Id

	// Before is where the window begins: what was over before it is what
	// went. The zero time for a window of forever, of which nothing goes.
	Before time.Time

	// Held is what the legal holds on the tenant are for, while one kept
	// everything.
	Held []string

	// Facts and Events are how many went.
	Facts  int
	Events int
}

// Total is how many rows went.
func (x Expired) Total() int { return x.Facts + x.Events }

// Going is the rows of one of the app's own tables that name an event and go
// at `before`, by the predicate the app's own expiry deletes them by: what
// [Preview] counts as gone, since it deletes nothing.
//
// It is asked of the table it is given for, so a column it names is that
// table's -- an app's generated predicates are written that way -- and it is
// read under a NOT: a comparison of a nullable column says `IS NOT NULL` too,
// or a row holding NULL is neither gone nor staying.
type Going func(before time.Time) func(*sql.Selector)

// Expire takes out of `tenant`'s past what its window no longer reaches, in
// the transaction the store is on: after the app has taken out its own rows
// that name events, and before it commits.
//
// What goes is what was over before the window began, `now` less
// [Window.Keep] -- rove's design 8.2:
//
//   - **A fact** superseded by then; or, unless [Expiry.KeepFacts] names it,
//     one a later fact of its field had replaced by then -- begun by then,
//     written down by then, and standing still. The state the window begins
//     in stays, so what anything was at a moment inside the window reads as
//     it did, as known now and as known then.
//   - **An event** that happened and was written down before then, of no kind
//     [Expiry.KeepKinds] names, that no row that stays names: no fact it wrote
//     or superseded, no row of the app's whose column says it names an event
//     ([Store.Pointers]), and no event it caused. The facts go first, so an
//     event only they named goes with them -- and an operation's event with
//     the last of its children.
//
// Nothing goes under a legal hold, nor for a window of forever.
//
// Each row is taken by its identifier, a page at a time read in the order an
// index keeps, and put on the trail as taken by [Expiry.Act], in the same
// transaction (`pd.RecordErased`): a row deleted past the servers and not put
// there is one the trail says is a row, which a check of the rows finds. Then
// the receipt, for an app that asks for one.
//
// It is the same work each time it runs: what a pass took is not there to take
// again, so a second pass takes nothing. A dry run is this in a transaction
// the caller rolls back, and what it says is what a run would do, to the row.
// On PostgreSQL it holds the tenant until the transaction ends, so two
// expiries of one tenant take turns rather than both taking the same rows.
//
// The context is framed as the deployment's work in `tenant`, which the trail
// files what went under -- `pd.PastIn` frames it so -- and one framed in
// another tenant is refused.
func Expire(ctx context.Context, s Store, tenant pdid.Id, w Window, now time.Time, x Expiry) (Expired, error) {
	out := Expired{Tenant: tenant}

	drv := s.Driver()
	if !dialect.InTx(drv) {
		return out, ErrNotInTx
	}
	f, err := gate.Actor(ctx)
	if err != nil {
		return out, err
	}
	if f.Tenant != tenant {
		return out, status.Errorf(codes.FailedPrecondition,
			"what an expiry of %s takes is filed on the trail under the tenant its context is framed in, and this one is framed in %s",
			tenant, f.Tenant)
	}
	ps, err := pointers(s)
	if err != nil {
		return out, err
	}
	keep, err := x.facts(s)
	if err != nil {
		return out, err
	}

	if w.Keep <= 0 {
		return out, nil
	}
	out.Before = moment(now.Add(-w.Keep))
	if w.Held() {
		out.Held = slices.Clone(w.Holds)
		return out, nil
	}

	// Before anything is read, so that one waiting reads what the one before
	// it left.
	if err := lock(ctx, drv, tenant); err != nil {
		return out, err
	}

	act := x.act()
	if out.Facts, err = expireFacts(ctx, s, tenant, out.Before, keep, act); err != nil {
		return out, err
	}
	if out.Events, err = expireEvents(ctx, s, tenant, out.Before, x.KeepKinds, ps, act); err != nil {
		return out, err
	}

	if x.Receipt == nil {
		return out, nil
	}
	if v := x.Receipt(out); v != nil {
		if _, _, err := Begin(ctx, s, Event{Value: v}); err != nil {
			return out, fmt.Errorf("the receipt: %w", err)
		}
	}

	return out, nil
}

// expireFacts deletes the tenant's facts that were over by `c`, a page at a
// time, and answers how many went.
func expireFacts(ctx context.Context, s Store, tenant pdid.Id, c time.Time, keep func(*sql.Selector), act string) (int, error) {
	where := []func(*sql.Selector){is(colTenant, tenant), factGone(c, keep)}

	n := 0
	var at *cursor
	for {
		rows, err := s.Facts(ctx, Select{
			Where: append(slices.Clone(where), at.after()...),
			Order: order(colRecorded, colId),
			Limit: page,
		})
		if err != nil || len(rows) == 0 {
			return n, err
		}

		ids := make([]pdid.Id, len(rows))
		for i, r := range rows {
			ids[i] = r.Id
		}
		k, err := take(ctx, s, s.DeleteFacts, act, ids)
		n += k
		if err != nil || len(rows) < page {
			return n, err
		}

		last := rows[len(rows)-1]
		at = &cursor{last.Recorded, last.Id}
	}
}

// expireEvents deletes the tenant's events that were over by `c` and that no
// row of `ps` names now, a page at a time, and answers how many went.
//
// An event a page passed over because a child named it is named by nothing
// once that child goes, which may be a page later. So the events the ones that
// went were part of are asked again, by identifier, until none of them goes:
// as many times as one operation is part of another.
func expireEvents(ctx context.Context, s Store, tenant pdid.Id, c time.Time, kinds []string, ps []Pointer, act string) (int, error) {
	where := append([]func(*sql.Selector){is(colTenant, tenant)}, old(c, kinds)...)
	where = append(where, unnamed(ps))

	n := 0
	var causes []pdid.Id
	gone := func(rows []EventRow) error {
		ids := make([]pdid.Id, len(rows))
		for i, r := range rows {
			ids[i] = r.Id
			if !r.Cause.IsZero() {
				causes = append(causes, r.Cause)
			}
		}

		k, err := take(ctx, s, s.DeleteEvents, act, ids)
		n += k
		return err
	}

	var at *cursor
	for {
		rows, err := s.Events(ctx, Select{
			Where: append(slices.Clone(where), at.after()...),
			Order: order(colRecorded, colId),
			Limit: page,
		})
		if err != nil {
			return n, err
		}
		if len(rows) == 0 {
			break
		}
		if err := gone(rows); err != nil {
			return n, err
		}
		if len(rows) < page {
			break
		}

		last := rows[len(rows)-1]
		at = &cursor{last.Recorded, last.Id}
	}

	for len(causes) > 0 {
		asked := causes
		causes = nil
		for _, ids := range pages(asked) {
			rows, err := s.Events(ctx, Select{Where: append(slices.Clone(where), in(colId, ids))})
			if err != nil {
				return n, err
			}
			if len(rows) == 0 {
				continue
			}
			if err := gone(rows); err != nil {
				return n, err
			}
		}
	}

	return n, nil
}

// take deletes the rows `ids` names with `del` and puts them on the trail as
// taken by `act`, answering how many went.
func take(ctx context.Context, s Store, del func(context.Context, []pdid.Id) (int, error), act string, ids []pdid.Id) (int, error) {
	n, err := del(ctx, ids)
	if err != nil {
		return n, err
	}
	if err := s.Erased(ctx, act, ids); err != nil {
		return n, fmt.Errorf("the trail: %w", err)
	}

	return n, nil
}

// Preview is what [Expire] would take out of `tenant`'s past now, counted
// rather than taken: no row is deleted or held, so it is a read, which the
// tenant's owner may ask before a shorter window is saved as often as they
// like.
//
// It counts by the predicates an expiry deletes by, as the past is now --
// which is what the app's own rows are not, since an expiry deletes them first.
// So `going` is the predicate the app's expiry deletes the rows of each of its
// tables that name events by, by the table's domain: an event only rows that go
// name goes too, and is counted. A table it says nothing of keeps every row,
// and every event one of them names. What an expiry does in a transaction the
// caller rolls back is the same count, by doing it.
//
// Of a tenant the caller of `ctx` may see.
func Preview(ctx context.Context, s Store, tenant pdid.Id, w Window, now time.Time, x Expiry, going map[pdid.Domain]Going) (Expired, error) {
	out := Expired{Tenant: tenant}

	vs, all, err := frame.Narrow(ctx)
	if err != nil {
		return out, err
	}
	if !all && !slices.Contains(vs, tenant.Uuid()) {
		return out, status.Errorf(codes.NotFound, "%s not found", tenant)
	}
	ps, err := pointers(s)
	if err != nil {
		return out, err
	}
	keep, err := x.facts(s)
	if err != nil {
		return out, err
	}

	if w.Keep <= 0 {
		return out, nil
	}
	c := moment(now.Add(-w.Keep))
	out.Before = c
	if w.Held() {
		out.Held = slices.Clone(w.Holds)
		return out, nil
	}

	gone := func(p Pointer) func(*sql.Selector) {
		if p.Own == pdpb.Own_OWN_FACT {
			return factGone(c, keep)
		}
		if g := going[p.Domain]; g != nil {
			return g(c)
		}

		return nil
	}

	if out.Facts, err = s.CountFacts(ctx, []func(*sql.Selector){is(colTenant, tenant), factGone(c, keep)}); err != nil {
		return out, err
	}

	where := append([]func(*sql.Selector){is(colTenant, tenant)}, old(c, x.KeepKinds)...)
	where = append(where, notKept(tenant, c, x.KeepKinds, ps, gone))
	if out.Events, err = s.CountEvents(ctx, where); err != nil {
		return out, err
	}

	return out, nil
}

// pointers is what [Store.Pointers] answers, refused when it says an event
// names an event by a column other than its cause: that chain is followed by
// `cause_id` alone, which is what an expiry reads off every event it takes.
// `pd gen` refuses the schema that would say it.
func pointers(s Store) ([]Pointer, error) {
	ps := s.Pointers()
	for _, p := range ps {
		if p.Own == pdpb.Own_OWN_EVENT && p.Column != colCause {
			return nil, fmt.Errorf("past: %s.%s names an event from an event, and only its cause may", p.Table, p.Column)
		}
	}

	return ps, nil
}

// factGone is a fact that was over by `c`: superseded by then, or -- unless
// `keep` takes it -- one a later fact of its slot had replaced by then, begun
// by then, known by then and standing still.
//
// Known by then, because a read as known at a moment inside the window asks
// what was believed then, and until the later value was written down this one
// was it. And standing still, because a later one superseded since is not what
// the field held when the window began: this one is, as known now.
//
// Of the table the predicate is asked of, which is a fact's under any name: the
// later fact is read from the same table under one of its own.
func factGone(c time.Time, keep func(*sql.Selector)) func(*sql.Selector) {
	replaced := []func(*sql.Selector){func(s *sql.Selector) {
		b := sql.Dialect(s.Dialect())
		g := b.Table(s.TableName()).As("later")
		s.Where(sql.Exists(b.Select(g.C(colId)).From(g).Where(sql.And(
			sql.ColumnsEQ(g.C(colTenant), s.C(colTenant)),
			sql.ColumnsEQ(g.C(colSubject), s.C(colSubject)),
			sql.ColumnsEQ(g.C(colField), s.C(colField)),
			sql.ColumnsEQ(g.C(colKey), s.C(colKey)),
			sql.IsNull(g.C(colSuperseded)),
			sql.LTE(g.C(colRecorded), c),
			sql.ColumnsGT(g.C(colFrom), s.C(colFrom)),
			sql.LTE(g.C(colFrom), c),
		))))
	}}
	if keep != nil {
		replaced = append(replaced, sql.NotPredicates(keep))
	}

	return sql.OrPredicates(
		sql.AndPredicates(sql.FieldNotNull(colSuperseded), sql.FieldLTE(colSuperseded, c)),
		sql.AndPredicates(replaced...),
	)
}

// old is an event that happened and was written down before `c`, of no kind
// of `kinds`: one the window no longer reaches, unless a row names it.
func old(c time.Time, kinds []string) []func(*sql.Selector) {
	where := []func(*sql.Selector){sql.FieldLT(colAt, c), sql.FieldLT(colRecorded, c)}
	if len(kinds) > 0 {
		where = append(where, sql.FieldNotIn(colKind, kinds...))
	}

	return where
}

// unnamed is an event no row of `ps` names, of the rows there are now.
func unnamed(ps []Pointer) func(*sql.Selector) {
	return func(s *sql.Selector) {
		for i, p := range ps {
			s.Where(sql.NotExists(naming(s, i, p, s.C(colId), nil)))
		}
	}
}

// naming is the rows of `p`'s table that name the event `id` -- a column of
// the query it is asked in -- and stay: every one, or those `gone` does not
// take. Each is read under a name of its own, `i`, so that one asked inside
// another is not taken for it.
func naming(s *sql.Selector, i int, p Pointer, id string, gone func(*sql.Selector)) *sql.Selector {
	b := sql.Dialect(s.Dialect())
	t := b.Table(p.Table).As(fmt.Sprintf("pointer%d", i))
	q := b.Select(t.C(p.Column)).From(t).Where(sql.ColumnsEQ(t.C(p.Column), id))
	if gone != nil {
		sql.NotPredicates(gone)(q)
	}

	return q
}

// notKept is an event of the tenant's that an expiry would take, of the ones
// [old] takes -- the closure [expireEvents] finds by taking them, said as one
// statement that takes nothing.
//
// An event stays when a row that stays names it: a fact or a row of the app's
// that `gone` does not take, or an event that is not [old]. And then the event
// it was part of stays, and the one that was part of, up the chain of causes,
// which is what `kept` is -- `WITH RECURSIVE`, from those up. What is old and
// not in it goes.
func notKept(tenant pdid.Id, c time.Time, kinds []string, ps []Pointer, gone func(Pointer) func(*sql.Selector)) func(*sql.Selector) {
	candidate := append([]func(*sql.Selector){is(colTenant, tenant)}, old(c, kinds)...)

	return func(s *sql.Selector) {
		b := sql.Dialect(s.Dialect())
		events := s.TableName()

		// What stays of itself: an old event a row that stays names.
		r := b.Table(events).As("candidate")
		anchor := b.Select(r.C(colId)).From(r)
		for _, p := range candidate {
			p(anchor)
		}
		var named []*sql.Predicate
		for i, p := range ps {
			if p.Own != pdpb.Own_OWN_EVENT {
				named = append(named, sql.Exists(naming(anchor, i, p, anchor.C(colId), gone(p))))
				continue
			}

			// An event naming it that stays of itself, being no candidate.
			// One that is a candidate stays only if something keeps it, which
			// is the recursion below.
			ch := b.Table(p.Table).As(fmt.Sprintf("pointer%d", i))
			q := b.Select(ch.C(p.Column)).From(ch).Where(sql.ColumnsEQ(ch.C(p.Column), anchor.C(colId)))
			sql.NotPredicates(candidate...)(q)
			named = append(named, sql.Exists(q))
		}
		if len(named) == 0 {
			return
		}
		anchor.Where(sql.Or(named...))

		// And the event each of those was part of, up the chain.
		up := b.Table(events).As("cause")
		k := b.Table("kept")
		step := b.Select(up.C(colCause)).From(up).
			Join(k).On(up.C(colId), k.C(colId)).
			Where(sql.NotNull(up.C(colCause)))

		kept := sql.WithRecursive("kept", colId).As(anchor.Union(step))
		s.Where(sql.NotIn(s.C(colId), b.Select(k.C(colId)).From(k).Prefix(kept)))
	}
}

// cursor is the last row a page read, in the order every page here is read
// in: `date_created`, then the key.
type cursor struct {
	at time.Time
	id pdid.Id
}

// after is the rows after the cursor, as one comparison of the pair --
// `(date_created, id) > (a, b)` -- which an index starts its read at, where
// the OR it means is a filter, and every page is read from the first row
// (payday#154). None before the first page.
func (c *cursor) after() []func(*sql.Selector) {
	if c == nil {
		return nil
	}

	return []func(*sql.Selector){func(s *sql.Selector) {
		s.Where(sql.CompositeGT([]string{s.C(colRecorded), s.C(colId)}, c.at, c.id.Uuid()))
	}}
}

// facts is the facts [Expiry.KeepFacts] names, as one predicate -- nil for
// none -- each held to what its domain's message is: a field it has, a key of
// a map of it, values as [Encode] writes them.
func (x Expiry) facts(s Store) (func(*sql.Selector), error) {
	var ps []func(*sql.Selector)
	for i, k := range x.KeepFacts {
		p, err := k.predicate(s)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "past: KeepFacts[%d]: %s", i, err)
		}
		ps = append(ps, p)
	}
	if len(ps) == 0 {
		return nil, nil
	}

	return sql.OrPredicates(ps...), nil
}

// predicate is the facts `k` names.
func (k FactKeep) predicate(s Store) (func(*sql.Selector), error) {
	// An identifier of the kind, which is all a description is read off.
	d, ok := s.Describe(pdid.New(k.Domain))
	if !ok {
		return nil, fmt.Errorf("domain %d is no entity of this app's", k.Domain)
	}
	fd := d.Message.Fields().ByNumber(k.Field)
	if fd == nil {
		return nil, fmt.Errorf("%s has no field %d", d.Message.FullName(), k.Field)
	}

	withheld := d.Withholds(fd)
	switch {
	case withheld && k.Key != "":
		// A change to a map withheld is a fact of the field, with no key:
		// a key can be somebody's as much as a value.
		return nil, fmt.Errorf("%s is withheld, and a fact of it has no key", fd.FullName())

	case fd.IsMap() && !withheld:
		mk, err := decodeKey(fd.MapKey(), k.Key)
		if err != nil {
			return nil, err
		}
		if v, err := encodeKey(fd.MapKey(), mk); err != nil || v != k.Key {
			return nil, fmt.Errorf("%s: the key %q is written %q", fd.FullName(), k.Key, v)
		}

	case !fd.IsMap() && k.Key != "":
		return nil, fmt.Errorf("%s: a key is an entry of a map, and this is not one", fd.FullName())
	}

	vd := fd
	if fd.IsMap() && !withheld {
		vd = fd.MapValue()
	}
	for _, v := range k.Values {
		if err := canonical(vd, v, withheld); err != nil {
			return nil, fmt.Errorf("%s: %q %w", fd.FullName(), v, err)
		}
	}

	where := []func(*sql.Selector){
		sql.FieldEQ(colDomain, uint32(k.Domain)),
		sql.FieldEQ(colField, uint32(k.Field)),
		sql.FieldEQ(colKey, k.Key),
	}
	if len(k.Values) > 0 {
		where = append(where, sql.FieldIn(colValue, k.Values...))
	}

	return sql.AndPredicates(where...), nil
}

// canonical refuses `v` as the value of a fact of `fd` unless it is the text
// [Encode] writes for one, or [Null]: a fact's value is compared as text, so a
// value written any other way matches nothing.
func canonical(fd protoreflect.FieldDescriptor, v string, withheld bool) error {
	switch {
	case v == Null:
		return nil
	case withheld && v == Withheld:
		return nil
	case withheld:
		return errors.New("is a value of a field withheld, whose facts keep none")
	case v == Withheld:
		return errors.New("is a value withheld, which no fact of this field is")
	}

	val, err := Decode(fd, v)
	if err != nil {
		return fmt.Errorf("is no value of it: %w", err)
	}
	got, err := Encode(fd, val)
	if err != nil {
		return err
	}
	if got != v {
		return fmt.Errorf("is written %s", got)
	}

	return nil
}
