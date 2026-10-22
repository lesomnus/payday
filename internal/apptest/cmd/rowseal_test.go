package cmd_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/robot"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/rowseal"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
)

// A row's seal (payday#115): the MAC of a row whose kind declares a field
// personal, which the trail's digest leaves out -- written in the transaction
// of every write that leaves the row, kept beside it, removed with it, and
// blanked when its person is forgotten.

// sealWrites counts the statements that write the seals, as [trailWrites]
// counts the trail's.
type sealWrites struct {
	mu sync.Mutex
	n  int
}

func (w *sealWrites) on(drv dialect.Driver) dialect.Driver {
	unquote := strings.NewReplacer("`", "", `"`, "")
	return dialect.DebugWithContext(drv, func(_ context.Context, vs ...any) {
		if strings.Contains(unquote.Replace(fmt.Sprint(vs...)), "INSERT INTO "+rowseal.Table+" ") {
			w.mu.Lock()
			w.n++
			w.mu.Unlock()
		}
	})
}

func (w *sealWrites) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// TestATransactionWritesItsSealsTogether: the seals of a transaction's writes
// are kept for its commit, as its trail is, and written there in one
// statement -- so sealing costs a statement a batch, and not one a row.
func TestATransactionWritesItsSealsTogether(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)
	was := must(b.Ent.RowSeal.Query().Count(ctx))

	w := &sealWrites{}
	drv, tx, err := dialect.BeginTx(ctx, w.on(b.Drv))
	x.NoError(err)
	defer tx.Rollback()
	s, err := enttx.Rebind(b.Ungated, drv)
	x.NoError(err)

	for i := range 10 {
		_, err := s.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  fmt.Sprintf("arm-%02d", i),
			Keeper: "somebody@example.com",
		}.Build())
		x.NoError(err)
	}
	x.Equal(0, w.count(), "the seals were written before the transaction was done")

	x.NoError(tx.Commit())
	x.Equal(1, w.count(), "ten seals, and the statements that wrote them")
	x.Equal(was+10, must(b.Ent.RowSeal.Query().Count(ctx)))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
	x.Empty(got.Unsealed)
}

// TestASealGoesWithItsRow: a robot erased softly is a row still, sealed as the
// erase left it; one removed past the servers and put on the trail as gone
// (`RecordErased`) takes its seal with it.
func TestASealGoesWithItsRow(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)

	_, err = b.Ungated.Robot().Erase(ctx, app.RobotRef_builder{Id: v.GetId()}.Build())
	x.NoError(err)
	x.True(must(b.Ent.RowSeal.Query().Where(rowseal.IdEQ(k)).Exist(ctx)), "an erased row is a row, and was left unsealed")
	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)

	x.NoError(b.Ent.Robot.DeleteOneId(k).Exec(ctx))
	n, err := pd.RecordErased(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Purge", []pdid.Id{pdid.Id(k)})
	x.NoError(err)
	x.Equal(1, n)
	x.False(must(b.Ent.RowSeal.Query().Where(rowseal.IdEQ(k)).Exist(ctx)), "a seal outlived its row")

	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
}

// TestARowFromBeforeSealsIsListedAndARecordSealsIt: a row whose newest line
// was written by a build that sealed nothing -- made here by writing its
// line's digest as such a build would have, and taking its seal off -- is
// listed, and no finding; put on the trail by `Record`, as an app's
// acceptance does, it is sealed, and listed no more.
func TestARowFromBeforeSealsIsListedAndARecordSealsIt(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, rows := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)

	newest := rows[len(rows)-1]
	q := b.Ent.Robot.Query().Where(robot.IdEQ(k))
	bare.RobotSelectInit(q, nil)
	r, err := q.Only(ctx)
	x.NoError(err)
	sum, err := d.Of(pdid.Id(k), r.Proto(), newest.DigestFields, audit.Line{Id: newest.Id, Created: newest.DateCreated, Action: newest.Action})
	x.NoError(err)
	x.NoError(b.Ent.Audit.UpdateOneId(newest.Id).SetDigest(sum).Exec(ctx))
	x.NoError(b.Ent.RowSeal.DeleteOneId(k).Exec(ctx))

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "a row from before seals is no finding: %+v", got)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Unsealed)

	n, err := pd.Record(ctx, b.Ent, audit.Contents{Digest: d}, "/test/Accept", got.Unsealed)
	x.NoError(err)
	x.Equal(1, n)

	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
	x.Empty(got.Unsealed)
	x.True(must(b.Ent.RowSeal.Query().Where(rowseal.IdEQ(k)).Exist(ctx)))
}

// TestForgettingLeavesASealThatSaysSo: the trail's forgetting blanks the
// row's seal, leaving one that says the person was forgotten and nothing of
// what they were. The row matches on what its digest covers, and its keeper is
// vouched for by nothing -- until the next write through the servers seals it
// again.
func TestForgettingLeavesASealThatSaysSo(t *testing.T) {
	x := require.New(t)
	b, ctx, d := digesting(t)

	v, _ := b.kept(t, ctx)
	k, err := entuuid.FromBytes(v.GetId())
	x.NoError(err)

	forgot, err := trail.Policy{Digest: d}.Forget(ctx, pd.TrailStore(b.Ent), []pdid.Id{pdid.Id(k)})
	x.NoError(err)
	x.Equal(1, forgot.Seals)
	seal, err := b.Ent.RowSeal.Get(ctx, k)
	x.NoError(err)
	x.Equal(d.Forgotten(pdid.Id(k)), seal.Sum)

	got, err := pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
	x.Equal(1, got.Forgotten)

	_, err = b.Ungated.Robot().Patch(ctx, app.RobotPatchRequest_builder{
		Ref:              app.RobotRef_builder{Id: v.GetId()}.Build(),
		Keeper:           z.Ptr("somebody-new@example.com"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	x.NoError(b.Ent.Robot.UpdateOneId(k).SetKeeper("somebody-else@example.com").Exec(ctx))

	got, err = pd.Digests(ctx, b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(k)}, got.Differs, "sealed again by the write after")
	x.Zero(got.Forgotten)
}
