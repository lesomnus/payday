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
// The read the gate remembers goes through every scope the stack narrows by:
// the wall, and the set at field 3 when the app declared one. So what it found
// is that the row was inside both.
//
// # What a write makes it forget
//
// A write the generated servers make to a row -- a Patch, an Apply, an Erase
// -- forgets what the transaction was told about that row ([Forget]), and the
// next question about it is asked again. The Sink is where that is said, being
// what both stacks are built on: a write through the server with no wall
// forgets as one through the gate does.
//
// That is every way a write can make a remembered answer wrong about the row
// itself:
//
//   - An Erase takes the row out of every read, softly or for good.
//   - A Patch or an Apply may move it to another set. The set is an isolation
//     boundary as the tenant is, and nothing asks that its edge be immutable --
//     a row moved to a set the caller cannot see is a row they no longer see.
//   - An Erase that takes the row for good leaves its key free, and an Add
//     naming that key puts another row there, in whatever tenant the Add says.
//     Where the old row's walk arrived is nothing to say about the new one's.
//
// An Add forgets nothing: it names no row that was there to be asked about.
// And the tenant a row's walk arrives at does not otherwise move: `pd gen`
// refuses a stamp or an `agrees:` path with a step that can be repointed, and
// the generated sink remembers no walk that has one.
//
// What it does not forget is a change to the **caller** rather than to the
// row: a membership ended, a set taken away. The gate answers from before
// that, which is what it already does for the same change made by another
// transaction -- the read is not a lock, so a set taken away between the
// gate's read and the write is one the gate never saw. Remembering stretches
// that window to the rest of a transaction the caller is running themselves.
// It does not reach past the wall: the caller's tenants are fixed for the call,
// and a row's tenant is fixed for good.
//
// Nor does it see a write made around the servers -- ent itself, or SQL --
// which is not a write of theirs to forget by. A transaction that makes one to
// a row and then names that row through the servers is answered from before
// it.
//
// Forgetting on every write, every row, was the alternative, and it costs what
// this is for. A transaction that adds rows writes between every two questions
// it asks, so it would ask each of them every time. Forgetting the row a write
// was about costs the next question about that row and nothing else.
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

	at := row{entity: entity, id: string(ref.GetId())}
	if m.saw(at, f) {
		return nil
	}
	if err := ask(); err != nil {
		return err
	}

	m.see(at, f)
	return nil
}

// Tenant answers what `ask` does about which tenant the walk `walk` arrives at
// from the row of `entity` that `ref` names, asking it once for each walk and
// row in the transaction `drv` runs in.
//
// `entity` and `walk` say where the walk starts and where it goes -- `Robot`
// and `tenant`, not the edge that reached the robot -- so two edges onto the
// same row share an answer. Only a walk whose every step is immutable may be
// asked here; one that can be repointed arrives somewhere else after a write,
// and the generated sink asks the database for those every time.
func Tenant(drv dialect.Driver, entity, walk string, ref Ref, ask func() ([]byte, error)) ([]byte, error) {
	m, ok := memoOf(drv)
	if !ok || !ref.HasId() {
		return ask()
	}

	at := row{entity: entity, id: string(ref.GetId())}
	if v, ok := m.tenantOf(at, walk); ok {
		return v, nil
	}

	v, err := ask()
	if err != nil {
		return nil, err
	}

	m.keep(at, walk, v)
	return v, nil
}

// Forget drops what the transaction `drv` runs in was told about the row of
// `entity` that `ref` names: whether each frame may see it, and where each walk
// from it arrives. It is what a write to the row says, before the write; see
// the package comment.
//
// A reference that names its row some other way than by key -- an alias --
// forgets every row of `entity`, since which one it names is what a read would
// say. Outside a transaction that keeps anything it does nothing, and there is
// nothing to forget.
func Forget(drv dialect.Driver, entity string, ref Ref) {
	m, ok := memoOf(drv)
	if !ok {
		return
	}

	if ref == nil || !ref.HasId() {
		m.forgetAll(entity)
		return
	}

	m.forget(row{entity: entity, id: string(ref.GetId())})
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
			visible: map[row]map[*frame.Frame]struct{}{},
			tenant:  map[row]map[string][]byte{},
		}
	}).(*memo), true
}

// memo is one transaction's, by the row each answer is about, so that what a
// write to the row forgets is one entry of each map.
//
// Guarded, though a transaction is used from one goroutine at a time: what a
// transaction keeps is reachable by anything running on it, and a lock nobody
// contends costs nothing next to the read it saves.
type memo struct {
	mu sync.Mutex

	// visible is the frames each row was found by, through the wall.
	visible map[row]map[*frame.Frame]struct{}

	// tenant is the tenant each walk from a row arrives at, by the walk.
	tenant map[row]map[string][]byte
}

// row is a row of an entity, by its key.
type row struct {
	entity string
	id     string
}

func (m *memo) saw(at row, f *frame.Frame) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.visible[at][f]
	return ok
}

func (m *memo) see(at row, f *frame.Frame) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fs, ok := m.visible[at]
	if !ok {
		fs = map[*frame.Frame]struct{}{}
		m.visible[at] = fs
	}
	fs[f] = struct{}{}
}

func (m *memo) tenantOf(at row, walk string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.tenant[at][walk]
	return slices.Clone(v), ok
}

func (m *memo) keep(at row, walk string, v []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ws, ok := m.tenant[at]
	if !ok {
		ws = map[string][]byte{}
		m.tenant[at] = ws
	}
	ws[walk] = slices.Clone(v)
}

func (m *memo) forget(at row) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.visible, at)
	delete(m.tenant, at)
}

func (m *memo) forgetAll(entity string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for at := range m.visible {
		if at.entity == entity {
			delete(m.visible, at)
		}
	}
	for at := range m.tenant {
		if at.entity == entity {
			delete(m.tenant, at)
		}
	}
}
