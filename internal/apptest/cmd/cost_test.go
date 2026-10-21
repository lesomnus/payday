package cmd_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/batch"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/robot"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// counting is a limiter that lets everything through and remembers what each
// call was charged.
type counting struct {
	mu    sync.Mutex
	costs []int
}

func (l *counting) Allow(_ context.Context, _ string, n int) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.costs = append(l.costs, n)
	return 0, true
}

func (l *counting) charged() []int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]int(nil), l.costs...)
}

func aliases(n int) []string {
	vs := make([]string, n)
	for i := range vs {
		vs[i] = fmt.Sprintf("arm-%02d", i)
	}
	return vs
}

// An AddMany of many rows is that many Adds' worth of work, sent as one call.
// Counted as one, it was that many calls' worth for the price of one -- the
// thing a batch counts its operations one by one to stop. So a call costs what
// it asks for: the schema says which RPCs those are (`pd.Cost`), and the limit
// charges it.
func TestAnAddManyCostsItsItems(t *testing.T) {
	t.Run("as a call", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		l := &counting{}
		limit := grpcx.LimitUnary(l, gate.ByTenant(), pd.Cost)
		call := func(method string, req any) {
			t.Helper()
			_, err := limit(b.as(ctx), req, &grpc.UnaryServerInfo{FullMethod: method},
				func(context.Context, any) (any, error) { return nil, nil })
			x.NoError(err)
		}

		call(app.RobotService_AddMany_FullMethodName, robots(b.Tenant, aliases(5)...))
		call(app.RobotService_Add_FullMethodName, robots(b.Tenant, "arm").GetItems()[0])
		call(app.RobotService_AddMany_FullMethodName, app.RobotAddManyRequest_builder{}.Build())
		x.Equal([]int{5, 1, 1}, l.charged(), "an AddMany of nothing is still a call")
	})

	t.Run("as an operation of a batch, by the schema's cost when the guard says none", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		l := &counting{}
		_, err := b.batched(t, batch.Guard{Closed: grpcx.GeneralWrite, Limiter: l, By: gate.ByTenant()}).Do(b.as(ctx), pdpb.BatchRequest_builder{
			Ops: []*pdpb.Op{
				op(t, app.RobotService_AddMany_FullMethodName, robots(b.Tenant, aliases(3)...)),
				op(t, app.RobotService_Add_FullMethodName, robots(b.Tenant, "leg").GetItems()[0]),
			},
		}.Build())
		x.NoError(err)
		x.Equal([]int{3, 1}, l.charged())
	})

	t.Run("and refused when it costs more than is ever let through at once", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		// Ten at once, and a call of twelve rows.
		_, err := b.batched(t, batch.Guard{Closed: grpcx.GeneralWrite, Limiter: grpcx.NewLimiter(1, 10), By: gate.ByTenant()}).Do(b.as(ctx), pdpb.BatchRequest_builder{
			Ops: []*pdpb.Op{op(t, app.RobotService_AddMany_FullMethodName, robots(b.Tenant, aliases(12)...))},
		}.Build())
		x.Equal(codes.ResourceExhausted, status.Code(err))
		x.Contains(status.Convert(err).Message(), "ask for less")
		x.Zero(b.Ent.Robot.Query().Where(robot.AliasHasPrefix("arm-")).CountX(ctx))
	})
}

// A batch is one transaction, and an operation is not a row: one AddMany is a
// thousand. So what a batch may write is bounded by the rows its operations
// ask for, as well as by how many operations there are -- and a batch over it
// is refused before anything is charged or written.
func TestABatchIsBoundedByTheRowsItAsksFor(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	l := &counting{}
	g := batch.Guard{Closed: grpcx.GeneralWrite, Limiter: l, By: gate.ByTenant(), Rows: 5}

	_, err := b.batched(t, g).Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{
			op(t, app.RobotService_AddMany_FullMethodName, robots(b.Tenant, aliases(3)...)),
			op(t, app.RobotService_AddMany_FullMethodName, robots(b.Tenant, aliases(6)[3:]...)),
		},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(status.Convert(err).Message(), "6 rows")
	x.Empty(l.charged(), "a batch refused for its size was charged")
	x.Zero(b.Ent.Robot.Query().Where(robot.AliasHasPrefix("arm-")).CountX(ctx), "a batch refused for its size wrote")

	// And one within it goes through.
	_, err = b.batched(t, g).Do(b.as(ctx), pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{op(t, app.RobotService_AddMany_FullMethodName, robots(b.Tenant, aliases(5)...))},
	}.Build())
	x.NoError(err)
	x.Equal(5, b.Ent.Robot.Query().Where(robot.AliasHasPrefix("arm-")).CountX(ctx))
}
