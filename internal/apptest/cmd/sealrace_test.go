package cmd_test

import (
	"bytes"
	"context"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/payday/internal/apptest/internal/ent"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestASealBlankedStaysBlankedBesideACarry (#115, #144) is the seal a pass
// carrying a line forward could write back over an erasure's blank, made to
// happen every time.
//
// A pass whose window has run out on a chunk carries the newest line about a
// row still there onto a line of the database's own, and the row's seal with
// it: it reads the seal and writes it back bound to the new line. Here it
// reads the seal while an app's transaction is forgetting the row, and is held
// there until the transaction has committed -- what a carry waiting behind the
// transaction's lock on the seal would do. A transaction that blanked the seal
// itself would see the carry write back what it read, the seal of the row as
// it was, over the blank it had committed. It owes the seal instead, and the
// payment blanks it in a turn of its own, after the carry: so a seal is never
// blanked and then put back, and once paid it says the row was forgotten.
//
// On PostgreSQL, where the carry reads the seal as the last commit left it
// while the transaction is open, and a pass writes beside a transaction that
// has not committed.
func TestASealBlankedStaysBlankedBesideACarry(t *testing.T) {
	x := require.New(t)
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skipf("%s is not set; a carry reading a seal beside a transaction that has not committed is PostgreSQL's", pdtest.Postgres)
	}

	b, ctx, d := digesting(t)
	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)
	robot := pdid.Id(k)
	sealOf := func() []byte {
		s, err := b.Ent.RowSeal.Get(ctx, k)
		x.NoError(err)
		return s.Sum
	}
	forgotten := func(sum []byte) bool { return bytes.Equal(sum, d.Forgotten(robot)) }

	// Every line about it into the archive first, so that the transaction has
	// no row of the trail to blank and the pass has a chunk to carry it out of.
	a := flob.NewMemStores()
	moves := trail.Policy{Archive: a, Signer: pdtest.Signer(t), Digest: d, Keep: trail.Keep{Retain: time.Nanosecond, Destroy: time.Hour}, Lease: spin.Local()}
	x.NoError(moves.Valid())
	moves.Pass(ctx, pd.TrailStore(b.Ent))
	x.Empty(b.trailOf(t, ctx, v.GetId()), "the robot's lines are still in the database, so this proves nothing")
	carries := moves
	carries.Keep.Destroy = 2 * time.Nanosecond
	was := sealOf()
	x.NotEmpty(was)
	x.False(forgotten(was))

	// The carry's read of the robot's seal, held until it is let go: the first
	// read of it once the pass is under way, and no other.
	var armed, fired atomic.Bool
	read, release := make(chan struct{}), make(chan struct{})
	b.Ent.RowSeal.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			v, err := next.Query(ctx, q)
			vs, ok := v.([]*ent.RowSeal)
			if ok && armed.Load() && slices.ContainsFunc(vs, func(s *ent.RowSeal) bool { return s.Id == k }) && fired.CompareAndSwap(false, true) {
				close(read)
				<-release
			}
			return v, err
		})
	}))

	// The app's transaction, forgetting the robot.
	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	defer tx.Rollback()
	_, err = carries.Forget(ctx, pd.TrailStore(b.Ent.WithDriver(drv)), []pdid.Id{robot})
	x.NoError(err)

	// The pass, which carries the robot's newest line out of the chunk it
	// destroys, and reads its seal beside the transaction.
	done := make(chan struct{})
	var let sync.Once
	letGo := func() { let.Do(func() { close(release) }) }
	t.Cleanup(func() {
		letGo()
		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Error("the pass never finished")
		}
	})
	armed.Store(true)
	go func() {
		defer close(done)
		carries.Pass(context.WithoutCancel(ctx), pd.TrailStore(b.Ent))
	}()
	select {
	case <-read:
	case <-time.After(30 * time.Second):
		x.FailNow("the pass never read the robot's seal to carry it, so this proves nothing")
	}

	x.NoError(tx.Commit())
	committed := sealOf()
	letGo()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		x.FailNow("the pass did not finish once its carry was let go")
	}
	carried := sealOf()
	x.NotEqual(committed, carried, "the carry wrote nothing after the commit, so this proves nothing")

	x.False(forgotten(committed) && !forgotten(carried),
		"a carry that read the seal before the commit wrote it back over the blank the transaction committed")

	// And paid, the seal says the robot was forgotten.
	paid, err := carries.Pay(ctx, pd.TrailStore(b.Ent), robot)
	x.NoError(err)
	x.Equal(1, paid.Seals)
	x.True(forgotten(sealOf()), "the payment did not blank the seal")
}
