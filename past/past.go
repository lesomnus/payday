// Package past records what an app's operations did and what its resources
// were, and reads it back (payday#151).
//
// An app that declares a past -- `option (payday.app) = {past: {event: 17,
// fact: 16}}` -- has payday's Event and Fact among its entities. This package
// is what writes and reads them: an **event** for each operation, holding a
// message the app declares for its kind, and **facts** -- "from this moment on,
// this field of that resource was this" -- pointing at the event that wrote
// them. Read back, they say what a resource was at any moment, as it is known
// now or as it was known then, and what happened to it, in order.
//
// # Where the line between this and generated code is
//
// Where it is for the trail and the access record. A write goes through the
// app's generated servers, in the operation's transaction, so that the wall,
// the trail and every recorder meet it as they meet any other write -- and
// payday cannot name those servers, nor the ent client a read goes through.
// So `pd gen` writes a [Store] into an app that declares a past, `pd.Past`,
// which does exactly those things and decides nothing, and everything with a
// decision in it is here: what an event is filed under and caused by, which
// facts one supersedes, how a value is written down, which rows a read takes
// and whose they may be.
//
//	drv, tx, err := dialect.BeginTx(ctx, s.drv)
//	...
//	next, err := enttx.Rebind(s.Next(), drv) // what the operation's own writes go through
//	st, err := pd.Past(next, drv, s.content)
//	ctx, op, err := past.Begin(ctx, st, past.Event{
//		Subject: robot,
//		Value:   &app.RobotMoved{Robot: robot.Bytes(), From: from, To: to},
//	})
//	err = op.Set(ctx, past.Fact{Subject: robot, Values: past.Changes(before, after)})
//	...
//	return tx.Commit()
//
// # An operation
//
// [Begin] records the event an operation is, in the transaction it is
// written in, and refuses outside one: an event and the writes it is about
// commit together or not at all. Who did it and in which tenant is the
// caller's frame, the trace is the span's, and its kind is what the message
// it holds declares. An operation another one calls in the same transaction
// is part of it -- its event's cause is the caller's -- and [Detach] says one
// is not. An operation a client may send twice names itself ([Event.Id]):
// the second copy waits for the first and is told [ErrDone].
//
// An operation over many things records an event of its own, and one for
// each thing it changed, whose cause is the operation's: [Op.Child] for one,
// [Op.Batch] for many, a page of rows to a statement.
//
// # A fact
//
// A [Fact] is one statement about one resource -- its subject -- from one
// moment on: any number of its fields, each as the value it held. It is kept
// as a row for each field, typed by the subject's own descriptor rather than
// by a table of facts per kind of resource: the field's number, the entry of
// a map, and the one value as canonical JSON ([Encode]). A field the subject's
// entity declares `personal` or `secret` is written as having changed and
// never as what to: its value is withheld. [Changes] is the statement of what
// a write changed; [To], [Entry] and [Cleared] say one by hand.
//
// A fact is never changed. Writing one for a field at exactly the moment
// another was written for it supersedes that one ([Op.Set]), a correction
// supersedes the fact it corrects and writes it again at the moment it should
// have said ([Op.Correct], [Op.CorrectEvent]), and a fact that never held is
// superseded by nothing ([Op.Retract], [Op.RetractEvent]). So what was known
// at any moment stays readable.
//
// # Reading
//
// [StateAt] is what resources were at a moment, by what is known now or by
// what was known at another; [State.Into] puts one into its message, and
// [Diff] says what changed between two. [Facts] reads facts back as the
// statements they were written in, and [Timeline] is one resource's history,
// its facts and its events, newest first. Every read is narrowed to the
// tenants the caller may see, as the wall narrows every read of a server.
//
// # Saying what happened
//
// An event keeps no sentence, and is said when it is read. An app says, for
// each kind and in each language it is read in, what an event of it says --
// [Say], given the reading, the event and the message it holds -- and reads
// events with a [Reading] ([Sentences.Read]): in the reader's locale, or its
// language, or the app's default, and where none of them says one, as its
// kind and its fields ([Reading.Say]). A thing the message holds by value is
// said as it holds it, which is what it was called then; a person it holds by
// identifier is named as they are called now ([Reading.Name], by the app's
// [Names] -- `pd.PastNames` is one), and every name a sentence says is a row
// of the access record of the call that read it. [Unsaid] is for the test
// that every kind is said in every language.
//
//	r := sentences.Read(ctx, pd.PastNames(drv), "ko", zone)
//	for _, ev := range events {
//		say(r.Say(ev))
//	}
//	if err := r.Err(); err != nil { // a name that could not be read
//		return err
//	}
//
// # What is not kept, and the one act that takes something out
//
// An event names people by identifier and holds nothing of theirs -- `pd gen`
// refuses an event message that would -- and a fact withholds a personal value.
// The one free text the past keeps is an event's reason, which the Event
// declares personal: the trail records that it was written and never what it
// said. When a person asks for a text to be taken out, [Redact] blanks one
// event's reason, and records that on the trail as the act it is.
package past

import (
	"context"
	"errors"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/pdid"
)

// Store is an app's past as this package writes and reads it: its Event and
// Fact, through its own generated servers and its own ent client. `pd.Past`
// is the one `pd gen` writes into an app that declares a past.
//
// It is mechanical, and decides nothing. Every write it is asked for goes
// through the servers it was built on, so the wall, the trail and every
// recorder meet it as they meet any other write; every read is the predicates
// it is handed, over payday's own column names, applied as they are.
type Store interface {
	// Kinds is every event message the app declares, by the kind its rows are
	// written under: `pd.EventKinds`.
	Kinds() map[string]protoreflect.MessageType

	// Driver is what the store writes and reads on: for a write, the
	// operation's transaction.
	Driver() dialect.Driver

	// AddEvents writes `vs` through the app's generated servers -- an Add for
	// one, an AddMany a page at a time for more -- and answers each one's
	// identifier, in order.
	AddEvents(ctx context.Context, vs []EventRow) ([]pdid.Id, error)

	// AddFacts writes `vs` as AddEvents writes events.
	AddFacts(ctx context.Context, vs []FactRow) error

	// Supersede marks each of `vs` superseded at `at` by the event `by`, with
	// a Patch of each held to the version it was read at: a fact another
	// operation superseded meanwhile is refused rather than superseded twice.
	Supersede(ctx context.Context, vs []FactRow, at time.Time, by pdid.Id) error

	// Events and Facts read rows: `q` is predicates and an order over
	// payday's own column names, which this package builds and the store
	// applies as they are.
	Events(ctx context.Context, q Select) ([]EventRow, error)
	Facts(ctx context.Context, q Select) ([]FactRow, error)

	// Describe is the message the rows of the entity `subject` names are, and
	// the fields of it the entity declared `secret` and `personal`; false for
	// an identifier of no entity of the app's.
	Describe(subject pdid.Id) (Description, bool)

	// TenantOf is the tenant each of `ids` is in, read past the wall as the
	// trail reads it: pdid.Nil for a row of a kind behind no wall, and no
	// entry at all for an identifier that names no row -- nor for a row
	// behind the wall that reaches no tenant, a first hop of its path left
	// empty, which the wall shows to nobody. An identifier of a kind it
	// cannot say the tenant of -- payday's own records, a kind no server
	// writes and the trail does not read -- is an error.
	TenantOf(ctx context.Context, ids []pdid.Id) (map[pdid.Id]pdid.Id, error)

	// Redact blanks the reason of the event `id` beneath the servers -- an
	// event has no Patch -- and puts that on the trail as the act `action`
	// (`pd.Record`), in the same transaction.
	Redact(ctx context.Context, id pdid.Id, action string) error
}

// EventRow is a row of the app's Event, as this package writes it and reads
// it back. The app's own fields, which an overlay adds, are not here: the
// past writes none of them.
type EventRow struct {
	Id     pdid.Id
	Tenant pdid.Id
	Kind   string

	// Actor is who asked, and pdid.Nil for the deployment's own work.
	Actor pdid.Id

	// At is when it happened, which is not always when it was written down.
	At time.Time

	// Reason is why, in the words of whoever asked: personal, and so never on
	// the trail. Empty once it was redacted ([Redact]).
	Reason string

	// Subject is the thing it was about, pdid.Nil for an operation about none.
	Subject pdid.Id

	// Trace is the trace the operation was part of, empty for none.
	Trace []byte

	// Cause is the event of the operation this one was part of.
	Cause pdid.Id

	// Value is what was done: the message the app declares for its kind.
	Value *anypb.Any

	// Recorded is when it was written down.
	Recorded time.Time
}

// FactRow is a row of the app's Fact: one field of one subject, from one
// moment on.
type FactRow struct {
	Id      pdid.Id
	Tenant  pdid.Id
	Subject pdid.Id

	// Domain is the subject's entity, which its identifier carries too.
	Domain pdid.Domain

	// Field is which of the subject's fields, by number in its descriptor,
	// and Key which entry of a map field: empty for any other.
	Field protoreflect.FieldNumber
	Key   string

	// Value is the one value, as [Encode] writes it: "null" for the field
	// cleared, and empty for a value withheld.
	Value string

	// From is when the value became true; Recorded is when it was written
	// down, which is when it was known.
	From     time.Time
	Recorded time.Time

	// Event is the event that wrote it.
	Event pdid.Id

	// Version is the row's `date_updated`, what a Patch of it is held to.
	Version time.Time

	// Superseded is when a correction or a later fact replaced it, and
	// SupersededBy the event that did; nil and pdid.Nil while it stands.
	Superseded   *time.Time
	SupersededBy pdid.Id
}

// Slot is which of a subject's fields a fact is about: the field, and for a
// map field the entry.
func (r FactRow) Slot() Slot { return Slot{Field: r.Field, Key: r.Key} }

// Withheld says the row records that its field changed and not what to.
func (r FactRow) Withheld() bool { return r.Value == Withheld }

// Select is a read this package asks of a [Store]: predicates over payday's
// own column names, the order to read in, and at most how many rows -- none
// for every one.
//
// Functions of a selector because that is what ent's predicates are: the
// store's own `predicate.Fact` is one, so it applies these as they are, and
// this package builds them without naming a type of the app's.
type Select struct {
	Where []func(*sql.Selector)
	Order []func(*sql.Selector)
	Limit int
}

// Description is what [Store.Describe] answers of a subject: the message its
// rows are, and the fields of it its entity declared `secret` -- which nothing
// answers with -- and `personal`, which the trail records the setting of and
// never the value.
type Description struct {
	Message  protoreflect.MessageDescriptor
	Secret   []protoreflect.FieldDescriptor
	Personal []protoreflect.FieldDescriptor
}

// Withholds says a fact of `fd` is written without its value: a field the
// entity declared `secret` or `personal`.
func (d Description) Withholds(fd protoreflect.FieldDescriptor) bool {
	for _, v := range d.Secret {
		if v.Number() == fd.Number() {
			return true
		}
	}
	for _, v := range d.Personal {
		if v.Number() == fd.Number() {
			return true
		}
	}

	return false
}

// Withheld is the value of a fact of a field its subject's entity declares
// `personal` or `secret`: the field changed, and what to is not written down.
//
// Empty, which is no JSON at all -- an empty string is `""` -- so a withheld
// value cannot be read as one.
const Withheld = ""

// Null is the value of a fact of a field cleared: it held nothing from then
// on.
const Null = "null"

// Known is which facts a read takes: the ones standing as they are known now,
// which is the zero value; as they stood at a moment, by what had been
// written down by then and not yet replaced ([Known.At]); or every one ever
// written, superseded or not ([Known.All]).
type Known struct {
	At  *time.Time
	All bool
}

var (
	// ErrDone is an operation that already happened: an event of the
	// identifier [Begin] was given is there. The operation is a copy of one
	// sent before, and writes nothing.
	ErrDone = errors.New("past: this operation already happened")

	// ErrNotInTx is a write asked for outside a transaction. An event and
	// what it is about commit together or not at all, so the operation's
	// transaction is the caller's to begin, and the store is built on it.
	ErrNotInTx = errors.New("past: an operation is written in a transaction, and this store is on none")

	// ErrWithheld is the value of a fact that was not written down: its field
	// is one its subject's entity declares personal or secret.
	ErrWithheld = errors.New("past: the value is withheld")
)

// RedactAction is what [Redact] is on the trail as.
const RedactAction = "payday.past.redact"

// WithClock answers with `s` telling the time by `now`: the moment an
// operation is written down at, and what the facts it writes are known from
// and the ones it supersedes stop being. A store is otherwise on `time.Now`.
//
// For an app whose servers are on a clock of their own (`bare.WithClock`),
// and for a test that records at the moments it says.
func WithClock(s Store, now func() time.Time) Store { return clocked{s, now} }

type clocked struct {
	Store
	now func() time.Time
}

func (s clocked) Now() time.Time { return s.now() }

// now is the moment by `s`'s clock, to the microsecond every database keeps:
// a moment written down and read back is the same moment.
func now(s Store) time.Time {
	at := time.Now()
	if c, ok := s.(interface{ Now() time.Time }); ok {
		at = c.Now()
	}

	return moment(at)
}

// moment is `at` as a database keeps it: in UTC, to the microsecond.
func moment(at time.Time) time.Time { return at.UTC().Truncate(time.Microsecond) }
