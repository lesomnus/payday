package cmd_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
)

// trailWrites counts the statements that write the trail, read off the driver
// they go through. Quotes are taken out so that SQLite's spelling and
// PostgreSQL's are one.
type trailWrites struct {
	mu sync.Mutex
	n  int
}

func (w *trailWrites) on(drv dialect.Driver) dialect.Driver {
	unquote := strings.NewReplacer("`", "", `"`, "")
	return dialect.DebugWithContext(drv, func(_ context.Context, vs ...any) {
		if strings.Contains(unquote.Replace(fmt.Sprint(vs...)), "INSERT INTO audit") {
			w.mu.Lock()
			w.n++
			w.mu.Unlock()
		}
	})
}

func (w *trailWrites) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// readings adds n readings of one robot through `s`, and answers with what
// they are.
func readings(t *testing.T, ctx context.Context, s app.Server, robot []byte, n int) []pdid.Id {
	t.Helper()

	ks := make([]pdid.Id, n)
	for i := range n {
		v, err := s.Reading().Add(ctx, reading(robot, float64(i)))
		require.NoError(t, err)
		k, err := pdid.From(v.GetId())
		require.NoError(t, err)
		ks[i] = k
	}
	return ks
}

func uuids(ks []pdid.Id) []uuid.UUID {
	us := make([]uuid.UUID, len(ks))
	for i, k := range ks {
		us[i] = k.Uuid()
	}
	return us
}

// onTheTrail is how many rows of the trail are about `ks`, read after the fact
// and from outside every transaction.
func (b *built) onTheTrail(t *testing.T, ctx context.Context, ks []pdid.Id) int {
	t.Helper()

	got := 0
	for _, k := range ks {
		n, err := b.Ent.Audit.Query().Where(entaudit.ObjectId(k.Uuid())).Count(ctx)
		require.NoError(t, err)
		got += n
	}
	return got
}

// TestATransactionWritesItsTrailTogether is #84's fourth: every write is a row
// of the trail, and in a transaction that writes many rows they were a
// statement each. They are written together now, by the commit -- and the
// commit is what writes them, so none is left out and none outlives a rollback.
func TestATransactionWritesItsTrailTogether(t *testing.T) {
	t.Run("in one statement, when it commits", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")

		w := &trailWrites{}
		on := w.on(b.Drv)
		drv, tx, err := dialect.BeginTx(ctx, on)
		x.NoError(err)
		defer tx.Rollback()
		s, err := enttx.Rebind(b.Walled, drv)
		x.NoError(err)

		ks := readings(t, b.as(ctx), s, robot, 10)
		x.Equal(0, w.count(), "the trail was written before the transaction was done")

		x.NoError(tx.Commit())
		x.Equal(1, w.count(), "ten rows of the trail, and the statements that wrote them")
		x.Equal(10, b.onTheTrail(t, ctx, ks))
	})

	t.Run("and none of it when the transaction is rolled back", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")

		w := &trailWrites{}
		drv, tx, err := dialect.BeginTx(ctx, w.on(b.Drv))
		x.NoError(err)
		s, err := enttx.Rebind(b.Walled, drv)
		x.NoError(err)

		ks := readings(t, b.as(ctx), s, robot, 10)
		x.NoError(tx.Rollback())

		x.Equal(0, w.count())
		x.Equal(0, b.onTheTrail(t, ctx, ks))
	})

	t.Run("a few hundred rows a statement in a long one", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")

		w := &trailWrites{}
		drv, tx, err := dialect.BeginTx(ctx, w.on(b.Drv))
		x.NoError(err)
		defer tx.Rollback()
		s, err := enttx.Rebind(b.Walled, drv)
		x.NoError(err)

		ks := readings(t, b.as(ctx), s, robot, 1200)
		x.Equal(2, w.count(), "five hundred at a time, as they were recorded")

		x.NoError(tx.Commit())
		x.Equal(3, w.count(), "and the two hundred left, by the commit")
		x.Equal(1200, b.onTheTrail(t, ctx, ks))
	})

	// A write that began a transaction of its own -- nobody else's -- is the
	// one transaction that has nothing to gather, and it is on the trail by
	// the time it answers.
	t.Run("a write in no transaction of anybody's is on the trail at once", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")

		w := &trailWrites{}
		s, err := enttx.Rebind(b.Walled, w.on(b.Drv))
		x.NoError(err)

		ks := readings(t, b.as(ctx), s, robot, 1)
		x.Equal(1, w.count())
		x.Equal(1, b.onTheTrail(t, ctx, ks))
	})

	// A statement about the trail inside the transaction finds every row it
	// recorded: what is kept is written ahead of it. Counted here, and blanked
	// below -- which is what erasure does to a person's rows, in the same
	// transaction as the writes that put them there.
	t.Run("a count of the trail inside the transaction finds what was kept", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")

		drv, tx, err := dialect.BeginTx(ctx, b.Drv)
		x.NoError(err)
		defer tx.Rollback()
		s, err := enttx.Rebind(b.Walled, drv)
		x.NoError(err)

		ks := readings(t, b.as(ctx), s, robot, 3)
		n, err := b.Ent.WithDriver(drv).Audit.Query().Where(entaudit.ObjectIdIn(uuids(ks)...)).Count(ctx)
		x.NoError(err)
		x.Equal(3, n)
	})

	t.Run("and blanking it there blanks what was kept", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")

		drv, tx, err := dialect.BeginTx(ctx, b.Drv)
		x.NoError(err)
		defer tx.Rollback()
		s, err := enttx.Rebind(b.Walled, drv)
		x.NoError(err)

		ks := readings(t, b.as(ctx), s, robot, 3)
		_, err = b.Ent.WithDriver(drv).Audit.Update().
			Where(entaudit.ObjectIdIn(uuids(ks)...)).
			SetValue([]byte{}).
			SetPatch([]byte{}).
			Save(ctx)
		x.NoError(err)
		x.NoError(tx.Commit())

		vs, err := b.Ent.Audit.Query().Where(entaudit.ObjectIdIn(uuids(ks)...)).All(ctx)
		x.NoError(err)
		x.Len(vs, 3)
		for _, v := range vs {
			x.Empty(v.Value, "a row recorded before the blanking was written after it, whole")
			x.Empty(v.Patch)
		}
	})

	// What a row says is decided when the write is recorded, the moment
	// included: only the writing waits for the commit.
	t.Run("a row is stamped when it was recorded, not when it was written", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")

		drv, tx, err := dialect.BeginTx(ctx, b.Drv)
		x.NoError(err)
		defer tx.Rollback()
		s, err := enttx.Rebind(b.Walled, drv)
		x.NoError(err)

		ks := readings(t, b.as(ctx), s, robot, 1)
		recorded := time.Now()
		time.Sleep(50 * time.Millisecond)
		x.NoError(tx.Commit())

		v, err := b.Ent.Audit.Query().Where(entaudit.ObjectId(ks[0].Uuid())).Only(ctx)
		x.NoError(err)
		x.False(v.DateCreated.After(recorded), "stamped %s, after it was recorded at %s", v.DateCreated, recorded)
	})
}
