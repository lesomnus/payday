// Package seen remembers, for the rest of a transaction, what the layers in
// front of every write have already asked about the rows it names.
//
// # What it is for
//
// A generated `Add` asks two things about each reference in it before the row
// is written. The gate reads the row through the wall, to learn whether the
// caller may see it; the sink reads which tenant the row is in, to stamp the
// row being written or to compare it with another path. Each is right and each
// is a round trip, once for every reference of every write -- so a transaction
// that writes many rows about the same few things asks the same few questions
// over and over. An import into rove asked, for each row of the sheet, whether
// the caller could see their own tenant nine times and the asset it had just
// added seven (lesomnus/payday#84).
//
// Asked once in a transaction, the answer is the same answer. [For] is what a
// transaction is begun with, and the generated layers ask [Visible] and
// [Tenant] rather than the database.
//
// # What is remembered
//
// A row named by its key, and nothing else: a reference by alias or by an
// index names whichever row holds that name now, and a write in the same
// transaction can give it to another. And an answer that found the row, never
// one that did not -- a refusal is asked again, so a row the transaction goes
// on to add is found once it is there.
//
// What the gate learns is kept per frame, since a frame is what a read through
// the wall is narrowed by and another frame is another question. By identity
// rather than by comparing what two frames say: a batch narrows each of its
// operations into a frame of its own, and an answer for one of them is not
// proof about the next. What the sink learns is about the row and not about
// who is asking, so every frame in the transaction shares it.
//
// # Why a write does not make it forget
//
// The tenant a row's path arrives at does not move. `pd gen` refuses a stamp
// or an `agrees:` path with a step that can be repointed, and the generated
// sink remembers no path that has one -- so what it remembers stays true.
//
// What the gate remembers can stop being true: the transaction may erase the
// row, or end a membership the caller saw it through. The gate then answers
// from before that, which is what it already does for the same write arriving
// from another transaction -- the read is not a lock, so a row erased or a set
// taken away between the gate's read and the write is one the gate never saw.
// Remembering stretches that window to the rest of a transaction the caller is
// running themselves. It does not open one that was not there, and it does not
// reach past the wall: the caller's tenants are fixed for the call and a row's
// tenant is fixed for good, so a row they could see is in a tenant they hold
// for as long as the transaction lasts.
//
// Forgetting on every write was the alternative, and it costs what this is
// for. A transaction that adds rows writes between every two questions it
// asks, so it would ask each of them every time, as it does now.
//
// # Who begins one
//
// Whoever begins the transaction, beside `dialect.BeginTx`. A generated batch
// does; a layer that writes several rows in one transaction says it itself --
// docs/guide/server.md § Completing a generated verb. A context that went
// through neither asks every question, as before.
package seen

import (
	"context"
	"slices"
	"sync"

	"github.com/protobuf-orm/ent/dialect"

	"github.com/lesomnus/payday/frame"
)

// For answers with `ctx` carrying what the transaction begun on `drv` has asked
// so far.
//
// `drv` is the driver the transaction is begun **on**, not the one
// `dialect.BeginTx` answers with. When it is already inside a transaction, that
// transaction is an enclosing call's, and so is what `ctx` carries: both are
// kept, so an operation made of other operations asks each question once for
// all of them. Any other driver begins a transaction of its own and it starts
// with nothing, whatever `ctx` carried -- the call that handed it on may be in
// another transaction, one that may yet be rolled back, and what that one saw
// is not this one's to know.
//
//	drv, tx, err := dialect.BeginTx(ctx, s.drv)
//	if err != nil {
//		return nil, err
//	}
//	defer tx.Rollback()
//
//	ctx = seen.For(ctx, s.drv)
func For(ctx context.Context, drv dialect.Driver) context.Context {
	if dialect.InTx(drv) {
		if _, ok := memoOf(ctx); ok {
			return ctx
		}
	}

	return context.WithValue(ctx, key{}, &memo{
		visible: map[visibleKey]struct{}{},
		tenant:  map[tenantKey][]byte{},
	})
}

// Ref is a reference that may name its row by key, which is the only kind
// whose answer is remembered.
//
// Every generated reference to a row behind the wall is one: payday names those
// rows by identifier.
type Ref interface {
	HasId() bool
	GetId() []byte
}

// Visible answers what `ask` does about whether the caller may see the row of
// `entity` that `ref` names, asking it once in a transaction for each frame and
// row, and answering from memory after the first time it found the row.
//
// `ask` is the read through the wall, and its error is answered as it is: a
// refusal is not remembered, so it is asked again next time.
func Visible(ctx context.Context, entity string, ref Ref, ask func() error) error {
	m, ok := memoOf(ctx)
	if !ok || !ref.HasId() {
		return ask()
	}

	f, ok := frame.From(ctx)
	if !ok {
		// Nobody vouched for this, so there is no "who" to remember an answer
		// for -- and the read is about to refuse it anyway.
		return ask()
	}

	k := visibleKey{frame: f, entity: entity, id: string(ref.GetId())}
	if m.saw(k) {
		return nil
	}
	if err := ask(); err != nil {
		return err
	}

	m.see(k)
	return nil
}

// Tenant answers what `ask` does about which tenant `path` arrives at from the
// row `ref` names, asking it once in a transaction for each path and row.
//
// `path` names where the walk starts and where it goes -- `Robot.tenant`, not
// the edge that reached the robot -- so two edges onto the same row share an
// answer. Only a path whose every step is immutable may be asked here; one that
// can be repointed arrives somewhere else after a write, and the generated sink
// asks the database for those every time.
func Tenant(ctx context.Context, path string, ref Ref, ask func() ([]byte, error)) ([]byte, error) {
	m, ok := memoOf(ctx)
	if !ok || !ref.HasId() {
		return ask()
	}

	k := tenantKey{path: path, id: string(ref.GetId())}
	if v, ok := m.tenantOf(k); ok {
		return v, nil
	}

	v, err := ask()
	if err != nil {
		return nil, err
	}

	m.keep(k, v)
	return v, nil
}

type key struct{}

func memoOf(ctx context.Context) (*memo, bool) {
	v, ok := ctx.Value(key{}).(*memo)
	return v, ok
}

// memo is one transaction's.
//
// Guarded, though a transaction is used from one goroutine at a time: what is
// in a context is reachable by anything handed it, and a lock nobody contends
// costs nothing next to the read it saves.
type memo struct {
	mu      sync.Mutex
	visible map[visibleKey]struct{}
	tenant  map[tenantKey][]byte
}

type visibleKey struct {
	frame  *frame.Frame
	entity string
	id     string
}

type tenantKey struct {
	path string
	id   string
}

func (m *memo) saw(k visibleKey) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.visible[k]
	return ok
}

func (m *memo) see(k visibleKey) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.visible[k] = struct{}{}
}

func (m *memo) tenantOf(k tenantKey) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.tenant[k]
	return slices.Clone(v), ok
}

func (m *memo) keep(k tenantKey, v []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.tenant[k] = slices.Clone(v)
}
