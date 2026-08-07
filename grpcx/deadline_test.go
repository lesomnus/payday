package grpcx_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/grpcx"
)

// deadlineIn reports how long the handler was given, and whether it was given
// anything at all.
func deadlineIn(t *testing.T, ctx context.Context, d time.Duration) (time.Duration, bool) {
	t.Helper()

	var (
		left time.Duration
		ok   bool
	)
	_, err := grpcx.DeadlineUnary(d)(ctx, nil, nil, func(ctx context.Context, _ any) (any, error) {
		var t time.Time
		if t, ok = ctx.Deadline(); ok {
			left = time.Until(t)
		}
		return nil, nil
	})
	require.NoError(t, err)

	return left, ok
}

func TestDeadline(t *testing.T) {
	t.Run("caps a call that brought none", func(t *testing.T) {
		x := require.New(t)

		left, ok := deadlineIn(t, t.Context(), time.Minute)
		x.True(ok)
		// Anything within a whisker of a minute; what is under test is that
		// something was set, not the clock.
		x.Greater(left, 50*time.Second)
		x.LessOrEqual(left, time.Minute)
	})

	t.Run("leaves alone a call that brought one", func(t *testing.T) {
		x := require.New(t)

		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()

		// Further away than the cap, and still the caller's to decide: they
		// said how long the answer is worth waiting for.
		left, ok := deadlineIn(t, ctx, time.Minute)
		x.True(ok)
		x.Greater(left, time.Minute)
	})

	t.Run("caps nothing when there is no cap", func(t *testing.T) {
		x := require.New(t)

		_, ok := deadlineIn(t, t.Context(), 0)
		x.False(ok)

		x.Nil(grpcx.Deadline(0))
		x.Nil(grpcx.Deadline(-time.Second))
	})

	t.Run("the chain says the cap, or says nothing and gets the usual one", func(t *testing.T) {
		x := require.New(t)

		// A server that caps nothing is a decision, so it is said rather than
		// left out -- and what saying it does is take an interceptor away.
		whole := grpcx.Serving(t.Context())
		none := grpcx.Serving(t.Context(), grpcx.WithDeadline(0))
		x.Len(none.Unary, len(whole.Unary)-1, "the deadline interceptor, and only it")

		// And it is the unary chain it comes out of, since a stream is never
		// capped.
		x.Len(none.Stream, len(whole.Stream))
	})

	t.Run("does not cap a stream", func(t *testing.T) {
		x := require.New(t)

		// A stream is long-lived by design, and a default deadline would be a
		// server hanging up on a subscription every so often for no reason
		// anybody could see. Written down because the natural thing to do when
		// the first stream is added is to reach for symmetry.
		x.Len(grpcx.Deadline(time.Minute), 1, "the unary interceptor, and only it")
	})
}
