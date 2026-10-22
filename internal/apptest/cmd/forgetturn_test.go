package cmd_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/spin/spinpg"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestTwoErasuresInATransactionBesideAPassFinish (#115) is the wait a turn
// taken inside an app's transaction made, on PostgreSQL and its lease.
//
// The transaction forgets one thing, and a pass takes the turn and waits on
// the rows the transaction blanked; the transaction forgets another. Asked for
// the turn there, the second waited for the pass, and the pass for the
// transaction -- and the database saw nothing to break, since the turn is an
// advisory lock another session holds and not one of the locks it waits on.
// Inside a transaction an erasure takes no turn, so the second answers, the
// transaction commits, and the pass finishes. Paying what was owed then leaves
// nothing of either anywhere, the chunk the pass made of the rows as they were
// included.
func TestTwoErasuresInATransactionBesideAPassFinish(t *testing.T) {
	x := require.New(t)
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skipf("%s is not set; a pass waiting on a transaction's rows, and the lease's advisory locks, are PostgreSQL's", pdtest.Postgres)
	}

	db := dbOf(t)
	b, ctx := buildFrom(t, cmd.Config{
		Db:    db,
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Audit: config.AuditConfig{Content: "value"},
	})

	// One thing of a kind the pass moves at once, whose rows it will wait
	// on, and one of a kind it keeps an hour, which it does not touch.
	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "one",
	}.Build())
	x.NoError(err)
	moved := must(pdid.From(v.GetId()))
	h, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "another",
	}.Build())
	x.NoError(err)
	kept := must(pdid.From(h.GetId()))

	lease, err := spinpg.New(ctx, db.Dsn, spinpg.Retry(50*time.Millisecond))
	x.NoError(err)
	key, _, err := trail.GenerateKey()
	x.NoError(err)
	p := trail.Policy{
		Archive: flob.NewMemStores(),
		Signer:  must(trail.ParseKey(key)),
		Keep:    trail.Keep{Retain: time.Hour},
		By:      map[pdid.Domain]trail.Keep{pd.RobotDomain: {Retain: time.Nanosecond}},
		Lease:   lease,
	}

	said := func(of pdid.Id) (db int, archived int) {
		db, err := b.Ent.Audit.Query().Where(entaudit.ObjectId(of.Uuid()), entaudit.ValueNEQ([]byte{})).Count(ctx)
		x.NoError(err)
		x.NoError(p.Read(ctx, pd.TrailStore(b.Ent), pd.TrailOf(func(v *app.Audit) error {
			if pdid.Id(v.GetObjectId()) == of && len(v.GetValue()) > 0 {
				archived++
			}
			return nil
		})))
		return db, archived
	}
	n, _ := said(moved)
	x.Positive(n, "the trail holds nothing about what is moved, so this proves nothing")

	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	defer tx.Rollback()
	in := pd.TrailStore(b.Ent.WithDriver(drv))

	_, err = p.Forget(ctx, in, []pdid.Id{moved})
	x.NoError(err)

	// The pass, which moves what is about `moved` and waits on the rows this
	// transaction blanked.
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Pass(context.WithoutCancel(ctx), pd.TrailStore(b.Ent))
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Error("the pass never finished")
		}
	})

	var pid int
	rows := &entsql.Rows{}
	x.NoError(drv.Query(ctx, "SELECT pg_backend_pid()", []any{}, rows))
	x.True(rows.Next())
	x.NoError(rows.Scan(&pid))
	x.NoError(rows.Close())
	watch, err := sql.Open("pgx", db.Dsn)
	x.NoError(err)
	defer watch.Close()
	x.Eventually(func() bool {
		var n int
		err := watch.QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))", pid).Scan(&n)
		return err == nil && n > 0
	}, 30*time.Second, 50*time.Millisecond, "the pass never waited on the rows the transaction blanked, so this proves nothing")

	// The second, in the same transaction, while the pass holds the turn.
	soon, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = p.Forget(soon, in, []pdid.Id{kept})
	x.NoError(err, "the second erasure waited for a pass that was waiting for it")
	x.NoError(tx.Commit())

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		x.Fail("the pass did not finish once the transaction had committed")
	}

	// What the pass copied of the rows as they were is owed, and paying it
	// leaves nothing of either.
	_, archived := said(moved)
	x.Positive(archived, "the pass did not copy the rows as they were, so this proves nothing")
	paid, err := p.Pay(ctx, pd.TrailStore(b.Ent), moved, kept)
	x.NoError(err)
	x.Equal(2, paid.Objects)
	for _, of := range []pdid.Id{moved, kept} {
		db, archived := said(of)
		x.Zero(db, "the database still says something about %s", of)
		x.Zero(archived, "the archive still says something about %s", of)
	}
}
