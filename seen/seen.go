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
// Asked once in a transaction, the answer is the same answer. A transaction
// begun with `dialect.BeginTx` keeps what it was told (`dialect.Keep`), and
// the generated layers ask [Visible] and [Tenant], with the driver they run
// on, rather than the database.
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
// # Whose it is
//
// The transaction's, and nobody begins it: what a transaction begun with
// `dialect.BeginTx` was told is kept with it, and goes with its commit or its
// rollback. One begun inside another is that one, and shares what it was
// told, so an operation made of other operations asks each question once for
// all of them. One begun afresh starts with nothing, whatever the call that
// began it was inside -- that may be another transaction, one that may yet be
// rolled back, and what that one saw is not this one's to know.
//
// It was the context's, and whoever began a transaction had to say so beside
// `dialect.BeginTx`. A layer that did not asked every question as before, and
// nothing said it had not; a transaction cannot forget what it keeps itself.
//
// A write in no transaction, or in one a generated `Client.Tx` began, keeps
// nothing and asks every question, as before.
package seen

import (
	"context"
	"slices"
	"sync"

	"github.com/protobuf-orm/ent/dialect"

	"github.com/lesomnus/payday/frame"
)

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
// `entity` that `ref` names, asking it once for each frame and row in the
// transaction `drv` runs in, and answering from memory after the first time it
// found the row.
//
// `ask` is the read through the wall, and its error is answered as it is: a
// refusal is not remembered, so it is asked again next time.
func Visible(ctx context.Context, drv dialect.Driver, entity string, ref Ref, ask func() error) error {
	m, ok := memoOf(drv)
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
// row `ref` names, asking it once for each path and row in the transaction
// `drv` runs in.
//
// `path` names where the walk starts and where it goes -- `Robot.tenant`, not
// the edge that reached the robot -- so two edges onto the same row share an
// answer. Only a path whose every step is immutable may be asked here; one that
// can be repointed arrives somewhere else after a write, and the generated sink
// asks the database for those every time.
func Tenant(drv dialect.Driver, path string, ref Ref, ask func() ([]byte, error)) ([]byte, error) {
	m, ok := memoOf(drv)
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

// memoOf is what the transaction `drv` runs in has been told, made the first
// time it is asked for. A driver in no transaction that keeps anything has
// none.
func memoOf(drv dialect.Driver) (*memo, bool) {
	if drv == nil {
		return nil, false
	}
	k, ok := dialect.KeepOf(drv)
	if !ok {
		return nil, false
	}

	return k.Value(key{}, func() any {
		return &memo{
			visible: map[visibleKey]struct{}{},
			tenant:  map[tenantKey][]byte{},
		}
	}).(*memo), true
}

// memo is one transaction's.
//
// Guarded, though a transaction is used from one goroutine at a time: what a
// transaction keeps is reachable by anything running on it, and a lock nobody
// contends costs nothing next to the read it saves.
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
