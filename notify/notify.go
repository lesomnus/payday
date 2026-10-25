// Package notify provides topic notifications with application-defined access
// and lazy, independently owned receipts. An app's generated Store writes through
// its own servers so notification, audit and change records share a transaction.
//
// A Topic identifies an interest, not a recipient list. Resource identifiers
// carry their own kind; named topics need not correspond to a database row.
// Policy decides who may publish, who the authenticated caller reads as, and
// whether that reader may access a topic. The tenant wall is applied before app
// policy and cannot be widened by it. A receipt reader need not be a Holder.
//
// List returns shared notifications and Read pairs them with the resolved
// reader's receipts. Neither creates a receipt. Mark alone records the reader's
// first read time. Receipt identity and time survive retries; a notification
// has no single shared read state.
//
// Writes begin or join an app transaction through Store.Rebind. A joined write
// does not publish before its outer transaction commits: the outer Watch
// context or the app's change outbox owns that publication. An app worker that
// wants immediate publication wraps its complete outer transaction with
// watch.Watch.Transaction; notification writes may also start their own.
// A policy that reads the app database implements PolicyBinder so those reads
// use the same driver, including changes not yet committed by an outer operation.
//
// Listen installs a live subscription and sends readiness before invalidation
// hints. Clients wait for readiness, query Read, and query again when hints
// arrive, including hints received during a query. Reconnection repeats that
// sequence. There is no replay cursor or durable subscription. After committing
// changes to authorization relationships, the app calls Service.Invalidate;
// readers that lost access are disconnected rather than shown a stale hint.
//
// Options.CursorKey is a deployment secret shared by replicas. Cursors bind the
// authenticated caller, credential authority, resolved reader, tenant, topic
// set and RPC; they do not grant access when app policy changes.
package notify

import (
	"context"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"
)

// Topic identifies an interest. Exactly one of Resource and Name is set; a
// named topic and a resource's textual identifier have distinct identities.
type Topic struct {
	Resource pdid.Id
	Name     string
}

// Notification is shared by all authorized readers of Topic. Producer and
// IdempotencyKey are storage metadata rather than public notification fields.
type Notification struct {
	ID             pdid.Id
	Tenant         pdid.Id
	Topic          Topic
	Kind           string
	Source         pdid.Id
	Payload        *anypb.Any
	DateCreated    time.Time
	DateUpdated    time.Time
	Producer       string
	IdempotencyKey string
}

type Receipt struct {
	ID           pdid.Id
	Tenant       pdid.Id
	Notification pdid.Id
	Reader       pdid.Id
	DataRead     time.Time
	DateCreated  time.Time
	DateUpdated  time.Time
}

type AddRequest struct {
	Tenant         pdid.Id
	Topic          Topic
	Kind           string
	Source         pdid.Id
	Payload        *anypb.Any
	IdempotencyKey string
}

type Query struct {
	Tenant pdid.Id
	Topics []Topic
	Size   int
	Cursor string
}

type Page struct {
	Items []Notification
	Next  string
}

type ReadItem struct {
	Notification Notification
	Receipt      *Receipt
}

type ReadPage struct {
	Items  []ReadItem
	Next   string
	Reader pdid.Id
}

type Position struct {
	DateCreated time.Time
	ID          pdid.Id
}

// Selection is already authorized. Store applies all predicates before Limit,
// orders by (date_created, id) descending and continues past Position.
type Selection struct {
	Tenant   pdid.Id
	Topics   []Topic
	Position *Position
	Limit    int
}

// Hint has a zero Reader for a shared notification or policy change. Receipt
// changes name their reader so another reader's sessions learn nothing of them.
type Hint struct {
	Tenant pdid.Id
	Topics []Topic
	Reader pdid.Id
}

type ListenRequest struct {
	Tenant pdid.Id
	Topics []Topic
}

// Event is readiness or an invalidation. Ready is sent exactly once first;
// subsequent events contain only a nonempty subset of the requested topics.
type Event struct {
	Ready  bool
	Reader pdid.Id
	Topics []Topic
}

// Policy is the application's meaning of a topic and a reader. A reader may
// name any persistent app identity; authenticating a caller alone is not a
// delegation to write another identity's receipt. An ordinary error refuses
// access; return a gRPC status to distinguish an operational failure from denial.
type Policy interface {
	Publisher(context.Context, pdid.Id, Topic) (string, error)
	Reader(context.Context, pdid.Id) (pdid.Id, error)
	Read(context.Context, pdid.Id, pdid.Id, Topic) error
}

// PolicyBinder is implemented by a Policy that reads the application's database.
// WithDriver returns that policy on the notification store's driver, including
// its transaction. The original binder is retained for later transactions; the
// returned Policy need not itself implement PolicyBinder. A policy with no
// database dependency can implement Policy alone.
type PolicyBinder interface {
	WithDriver(dialect.Driver) (Policy, error)
}

// Store is generated into the app: the runtime cannot name its ent client or
// protobuf types. Insert and InsertReceipt use its generated write path. Rebind
// retains the complete server stack on the transaction driver it is given.
type Store interface {
	Driver() dialect.Driver
	Rebind(dialect.Driver) (Store, error)
	Insert(context.Context, Notification) (Notification, error)
	// Find returns NotFound for a missing identifier, including an identifier of
	// another entity kind. Service applies the tenant wall and topic policy.
	Find(context.Context, pdid.Id) (Notification, error)
	Select(context.Context, Selection) ([]Notification, error)
	FindPublication(context.Context, pdid.Id, string, string) (Notification, bool, error)
	Receipts(context.Context, pdid.Id, pdid.Id, []pdid.Id) ([]Receipt, error)
	InsertReceipt(context.Context, Receipt) (Receipt, error)
	// Change extracts immutable notification scope from a committed change.
	// Unrelated entity changes return false. Signal invalidations are decoded
	// by Service itself and are never passed here.
	Change(context.Context, watch.Change) (Hint, bool, error)
}

type Options struct {
	// CursorKey authenticates cursors. All app replicas use the same secret of
	// at least 32 bytes; changing it invalidates outstanding cursors.
	CursorKey []byte
	// Clock is server time; nil uses time.Now. Stored times use microsecond
	// precision so PostgreSQL and an initial mutation response agree.
	Clock func() time.Time
}
