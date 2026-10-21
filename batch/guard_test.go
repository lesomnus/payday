package batch_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/batch"
	"github.com/lesomnus/payday/grpcx"
)

// charged is a limiter that lets everything through and remembers what each
// call was charged.
type charged struct{ costs []int }

func (l *charged) Allow(_ context.Context, _ string, n int) (time.Duration, bool) {
	l.costs = append(l.costs, n)
	return 0, true
}

// many is a cost that says an operation of "/app.RobotService/AddMany" is as
// many calls as the request says, as `pd.Cost` does with its items.
func many(method string, req any) int {
	if n, ok := req.(int); ok && method == "/app.RobotService/AddMany" {
		return n
	}
	return 1
}

// An operation is charged what it costs, which for an AddMany is its rows: a
// batch counts its operations one by one so that a thousand calls' worth of
// work is not done for the price of one, and an AddMany of a thousand is that.
func TestAnOperationIsChargedWhatItCosts(t *testing.T) {
	x := require.New(t)

	l := &charged{}
	g := batch.Guard{
		Limiter: l,
		By:      func(context.Context, string) string { return "tenant:acme" },
		Cost:    many,
	}

	for _, op := range []struct {
		method string
		req    any
	}{
		{"/app.RobotService/AddMany", 300},
		{"/app.RobotService/Add", nil},
		{"/app.RobotService/AddMany", 0},
	} {
		_, err := g.Allow(context.Background(), op.method, g.CostOf(op.method, op.req))
		x.NoError(err)
	}
	x.Equal([]int{300, 1, 1}, l.costs, "an AddMany of nothing is still a call")

	// With nothing said, everything is one.
	x.Equal(1, batch.Guard{}.CostOf("/app.RobotService/AddMany", 300))
}

// A batch is a transaction, and an operation is not a row: the rows its
// operations ask to write are bounded as well as how many there are.
func TestABatchIsBoundedByTheRowsItWrites(t *testing.T) {
	x := require.New(t)

	x.Equal(batch.MaxRows, batch.Guard{}.RowLimit())
	x.NoError(batch.Guard{}.CheckRows(batch.MaxRows))

	err := batch.Guard{}.CheckRows(batch.MaxRows + 1)
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(status.Convert(err).Message(), "rows")

	g := batch.Guard{Rows: 10}
	x.Equal(10, g.RowLimit())
	x.NoError(g.CheckRows(10))
	x.Error(g.CheckRows(11))
}

// A guard that says only how operations cost, or how many rows a batch takes,
// is one somebody filled in.
func TestAGuardOfCostsAloneIsNotZero(t *testing.T) {
	x := require.New(t)

	x.True(batch.Guard{}.IsZero())
	x.False(batch.Guard{Cost: grpcx.Cost(many)}.IsZero())
	x.False(batch.Guard{Rows: 10}.IsZero())
}
