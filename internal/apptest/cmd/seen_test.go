package cmd_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// asked counts the two questions a reading's `Add` asks about its robot, read
// off the driver its transaction is begun on: the gate's read of the robot
// through the wall, and the sink's read of the tenant the robot is in.
//
// By the statement, since that is the round trip being saved. Quotes are taken
// out first so that SQLite's and PostgreSQL's spellings of it are one.
type asked struct {
	mu      sync.Mutex
	walled  int
	tenancy int
}

func (a *asked) on(drv dialect.Driver) dialect.Driver {
	unquote := strings.NewReplacer("`", "", `"`, "")
	return dialect.DebugWithContext(drv, func(_ context.Context, vs ...any) {
		q := unquote.Replace(fmt.Sprint(vs...))

		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case strings.Contains(q, "FROM robot WHERE") && strings.Contains(q, "robot.tenant_id IN"):
			a.walled++
		case strings.Contains(q, "JOIN (SELECT robot.tenant_id FROM robot"):
			a.tenancy++
		}
	})
}

func (b *built) robot(t *testing.T, ctx context.Context, tenant pdid.Id, alias string) []byte {
	t.Helper()

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(t, err)

	return v.GetId()
}

func reading(robot []byte, celsius float64) *app.ReadingAddRequest {
	return app.ReadingAddRequest_builder{
		Robot:   app.RobotRef_builder{Id: robot}.Build(),
		Celsius: celsius,
	}.Build()
}

// TestATransactionAsksAboutARowOnce is lesomnus/payday#84. Ten readings of one
// robot in one transaction asked the gate's question and the sink's ten times
// each: the same two questions, with the same two answers.
func TestATransactionAsksAboutARowOnce(t *testing.T) {
	for _, tc := range []struct {
		what  string
		begun bool
		want  int
	}{
		{"in a transaction begun with dialect.BeginTx", true, 1},
		{"and in none, every time, as before", false, 10},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			b, ctx := build(t)
			robot := b.robot(t, ctx, b.Tenant, "arm")

			a := &asked{}
			drv := a.on(b.Drv)
			if tc.begun {
				v, tx, err := dialect.BeginTx(ctx, drv)
				x.NoError(err)
				defer tx.Commit()
				drv = v
			}

			// Nothing said beside the transaction: it keeps what it was told
			// itself.
			s, err := enttx.Rebind(b.Walled, drv)
			x.NoError(err)

			// One caller, so one frame: what the gate remembers is per frame.
			in := b.as(ctx)
			for i := range 10 {
				_, err := s.Reading().Add(in, reading(robot, float64(i)))
				x.NoError(err)
			}

			x.Equal(tc.want, a.walled, "the gate's read through the wall")
			x.Equal(tc.want, a.tenancy, "the sink's read of the robot's tenant")
		})
	}
}

// What one caller was told is not an answer for another, in the same
// transaction and about the same row: a frame is what a read through the wall
// is narrowed by.
func TestWhatOneFrameSawIsNotAnotherFramesAnswer(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)
	robot := b.robot(t, ctx, b.Tenant, "arm")

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)
	ok, err := pdid.From(other.GetId())
	x.NoError(err)

	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	defer tx.Rollback()

	s, err := enttx.Rebind(b.Walled, drv)
	x.NoError(err)

	in := ctx
	_, err = s.Reading().Add(b.as(in), reading(robot, 1))
	x.NoError(err)

	f := frame.New(pdid.New(pd.HolderDomain), ok, frame.Whole()).WithScope(frame.Only(ok))
	_, err = s.Reading().Add(frame.Into(in, f), reading(robot, 2))
	x.ErrCode(codes.NotFound, err)
}

// The sink remembers which tenant a row is in, and goes on comparing: what it
// remembers is one side of an `agrees:`, and the other side is still asked.
func TestTheSinkComparesWhatItRemembers(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)

	beta, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	bk, err := pdid.From(beta.GetId())
	x.NoError(err)

	mine := b.robot(t, ctx, b.Tenant, "lead-01")
	also := b.robot(t, ctx, b.Tenant, "follow-01")
	theirs := b.robot(t, ctx, bk, "follow-02")

	pair := func(lead, follow []byte) *app.PairingAddRequest {
		return app.PairingAddRequest_builder{
			Lead:   app.RobotRef_builder{Id: lead}.Build(),
			Follow: app.RobotRef_builder{Id: follow}.Build(),
		}.Build()
	}

	drv, tx, err := dialect.BeginTx(ctx, b.Drv)
	x.NoError(err)
	defer tx.Rollback()

	s, err := enttx.Rebind(b.Walled, drv)
	x.NoError(err)

	// Somebody who holds both, which is the caller the gate lets through and
	// only the comparison refuses.
	f := frame.New(b.Holder, b.Tenant, frame.Whole()).WithScope(frame.Only(b.Tenant, bk))
	in := frame.Into(ctx, f)

	_, err = s.Pairing().Add(in, pair(mine, also))
	x.NoError(err)

	_, err = s.Pairing().Add(in, pair(mine, theirs))
	x.ErrCode(codes.InvalidArgument, err)
	x.Contains(err.Error(), "another tenant")
}

// A batch is one transaction, and the sink asks about a row once for all of
// it. The gate asks once per operation: a batch narrows each one into a frame
// of its own, and an answer for one frame is not proof about the next.
func TestABatchAsksAboutARowOnce(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	robot := b.robot(t, ctx, b.Tenant, "arm")

	a := &asked{}
	s, err := pd.Batch(b.Walled, a.on(b.Drv), closedGuard)
	x.NoError(err)

	ops := []*pdpb.Op{}
	for i := range 5 {
		ops = append(ops, op(t, app.ReadingService_Add_FullMethodName, reading(robot, float64(i))))
	}
	_, err = s.Do(b.as(ctx), pdpb.BatchRequest_builder{Ops: ops}.Build())
	x.NoError(err)

	x.Equal(1, a.tenancy, "the sink's read of the robot's tenant")
	x.Equal(5, a.walled, "the gate's read through the wall, once for each operation's frame")
}

// A write to a row is what can make an answer about it wrong, and the Sink
// forgets what the transaction was told about the row before the write: the
// next question about it is asked again, and answered as the row now is.
func TestARowWrittenInTheTransactionIsAskedAboutAgain(t *testing.T) {
	begin := func(t *testing.T, b *built, ctx context.Context, s app.Server) app.Server {
		t.Helper()

		drv, tx, err := dialect.BeginTx(ctx, b.Drv)
		require.NoError(t, err)
		t.Cleanup(func() { tx.Rollback() })

		v, err := enttx.Rebind(s, drv)
		require.NoError(t, err)

		return v
	}

	// What the gate was told about the robot is the read through the wall,
	// which an erased row is not found by.
	t.Run("erased, it is a row the gate no longer finds", func(t *testing.T) {
		x := pdtest.NewX(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")
		s := begin(t, b, ctx, b.Walled)

		in := b.as(ctx)
		_, err := s.Reading().Add(in, reading(robot, 1))
		x.NoError(err)
		_, err = s.Robot().Erase(in, app.RobotRef_builder{Id: robot}.Build())
		x.NoError(err)

		_, err = s.Reading().Add(in, reading(robot, 2))
		x.ErrCode(codes.NotFound, err)
	})

	// And what the sink was told -- the tenant the robot is in -- is as much
	// about a row that is there, on the server the deployment writes through.
	t.Run("erased, it is a row the sink no longer finds", func(t *testing.T) {
		x := pdtest.NewX(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")
		s := begin(t, b, ctx, b.Ungated)

		_, err := s.Reading().Add(ctx, reading(robot, 1))
		x.NoError(err)
		_, err = s.Robot().Erase(ctx, app.RobotRef_builder{Id: robot}.Build())
		x.NoError(err)

		_, err = s.Reading().Add(ctx, reading(robot, 2))
		x.ErrCode(codes.InvalidArgument, err)
		x.Contains(err.Error(), "does not name one row")
	})

	// The set is an isolation boundary as the tenant is, and the edge that
	// puts a robot in a cell can be moved. A caller whose credential is for
	// one cell saw the robot there; moved to another, it is out of their
	// reach, however recently they saw it.
	t.Run("moved to a set the caller cannot see, it is out of reach", func(t *testing.T) {
		x := pdtest.NewX(t)
		b, ctx := build(t)

		cell := func(alias string) []byte {
			v, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  alias,
			}.Build())
			x.NoError(err)
			return v.GetId()
		}
		north, south := cell("north"), cell("south")

		v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Cell:   app.CellRef_builder{Id: north}.Build(),
			Alias:  "arm",
		}.Build())
		x.NoError(err)
		robot, version := v.GetId(), v.GetDateUpdated()

		// Both axes: the wall, and the set beside it.
		sink, err := pd.NewSink(b.Ent, bare.WithMinter(pd.Minter()), bare.WithScope(bare.Scopes{pd.Wall(), pd.Grouped(nil)}))
		x.NoError(err)
		grouped, err := app.Build(sink, pd.GateBuild())
		x.NoError(err)

		drv, tx, err := dialect.BeginTx(ctx, b.Drv)
		x.NoError(err)
		defer tx.Rollback()
		s, err := enttx.Rebind(grouped, drv)
		x.NoError(err)
		u, err := enttx.Rebind(b.Ungated, drv)
		x.NoError(err)

		k, err := pdid.From(north)
		x.NoError(err)
		in := frame.Into(ctx, frame.New(b.Holder, b.Tenant, frame.Whole().Within(k)).WithScope(frame.Only(b.Tenant)))

		_, err = s.Reading().Add(in, reading(robot, 1))
		x.NoError(err)

		_, err = u.Robot().Patch(ctx, app.RobotPatchRequest_builder{
			Ref:         app.RobotRef_builder{Id: robot}.Build(),
			Cell:        app.CellRef_builder{Id: south}.Build(),
			DateUpdated: version,
		}.Build())
		x.NoError(err)

		_, err = s.Reading().Add(in, reading(robot, 2))
		x.ErrCode(codes.NotFound, err)
	})

	// A write to another row forgets nothing about this one.
	t.Run("and a write to another row does not make it asked again", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")
		other := b.robot(t, ctx, b.Tenant, "leg")

		a := &asked{}
		drv, tx, err := dialect.BeginTx(ctx, a.on(b.Drv))
		x.NoError(err)
		defer tx.Rollback()
		s, err := enttx.Rebind(b.Walled, drv)
		x.NoError(err)

		in := b.as(ctx)
		_, err = s.Reading().Add(in, reading(robot, 1))
		x.NoError(err)
		_, err = s.Robot().Erase(in, app.RobotRef_builder{Id: other}.Build())
		x.NoError(err)

		// The erase reads the robot it erases, through the wall, which is
		// not a question about this one.
		walled, tenancy := a.walled, a.tenancy
		_, err = s.Reading().Add(in, reading(robot, 2))
		x.NoError(err)

		x.Equal(walled, a.walled, "the gate read the robot through the wall again")
		x.Equal(tenancy, a.tenancy, "the sink read the robot's tenant again")
	})
}
