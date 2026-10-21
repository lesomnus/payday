package grpcx_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/grpcx"
)

// stubLimiter answers what it was told to, and remembers what it was asked.
type stubLimiter struct {
	retry time.Duration
	ok    bool

	keys  []string
	costs []int
}

func (l *stubLimiter) Allow(_ context.Context, key string, n int) (time.Duration, bool) {
	l.keys = append(l.keys, key)
	l.costs = append(l.costs, n)
	return l.retry, l.ok
}

// limited runs one call through the interceptor and reports whether the handler
// ran, and what the caller was told.
func limited(t *testing.T, l grpcx.Limiter, by func(context.Context, string) string) (bool, error) {
	t.Helper()

	return costly(t, l, by, nil)
}

// costly is [limited] with the calls costing what `cost` says.
func costly(t *testing.T, l grpcx.Limiter, by func(context.Context, string) string, cost grpcx.Cost) (bool, error) {
	t.Helper()

	var ran bool
	_, err := grpcx.LimitUnary(l, by, cost)(
		t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/go_app.HolderService/Get"},
		func(context.Context, any) (any, error) {
			ran = true
			return nil, nil
		})

	return ran, err
}

// by is a keying function that always answers the same thing.
func by(key string) func(context.Context, string) string {
	return func(context.Context, string) string { return key }
}

func TestLimit(t *testing.T) {
	t.Run("a call over the line never reaches its handler", func(t *testing.T) {
		x := require.New(t)

		l := &stubLimiter{retry: 250 * time.Millisecond}
		ran, err := limited(t, l, by("tenant:acme"))
		x.False(ran)
		x.Equal(codes.ResourceExhausted, status.Code(err))
		x.Equal([]string{"tenant:acme"}, l.keys, "asked once; asking again would charge for the refusal")
	})

	t.Run("the caller is told how long to wait", func(t *testing.T) {
		x := require.New(t)

		_, err := limited(t, &stubLimiter{retry: 250 * time.Millisecond}, by("tenant:acme"))

		// As RetryInfo, which is what the API conventions say and what a
		// generated client reads however it was configured. A refusal a client
		// cannot time is a client that asks again at once.
		s, ok := status.FromError(err)
		x.True(ok)

		var found *errdetails.RetryInfo
		for _, d := range s.Details() {
			if v, ok := d.(*errdetails.RetryInfo); ok {
				found = v
			}
		}
		x.NotNil(found)
		x.Equal(250*time.Millisecond, found.GetRetryDelay().AsDuration())
	})

	t.Run("a call the keying names nothing for is not counted", func(t *testing.T) {
		x := require.New(t)

		// Which is how health and reflection go through: nobody vouched for
		// them, so there is nothing to count them against.
		l := &stubLimiter{ok: false}
		ran, err := limited(t, l, by(""))
		x.True(ran)
		x.NoError(err)
		x.Empty(l.keys, "the limiter was not even asked")
	})

	t.Run("nothing to count with is no interceptor", func(t *testing.T) {
		x := require.New(t)

		// Not an interceptor that lets everything through, which is what a
		// deployment that configured no limit gets.
		x.Nil(grpcx.Limit(nil, by("tenant:acme"), nil))
		x.Nil(grpcx.Limit(&stubLimiter{ok: true}, nil, nil))
		x.Len(grpcx.Limit(&stubLimiter{ok: true}, by("tenant:acme"), nil), 2, "the unary one and the stream one")
	})

	// An AddMany of a thousand rows is a thousand Adds' worth of work, and
	// counted as one call it would be a thousand calls' worth for the price
	// of one.
	t.Run("a call is charged what it costs", func(t *testing.T) {
		x := require.New(t)

		l := &stubLimiter{ok: true}
		ran, err := costly(t, l, by("tenant:acme"), func(method string, _ any) int {
			if method == "/go_app.HolderService/Get" {
				return 7
			}
			return 1
		})
		x.True(ran)
		x.NoError(err)
		x.Equal([]int{7}, l.costs)
	})

	t.Run("and a call that costs less than one, or nothing said, is one", func(t *testing.T) {
		x := require.New(t)

		l := &stubLimiter{ok: true}
		_, err := costly(t, l, by("tenant:acme"), func(string, any) int { return 0 })
		x.NoError(err)
		_, err = costly(t, l, by("tenant:acme"), nil)
		x.NoError(err)
		x.Equal([]int{1, 1}, l.costs)
	})

	// Waiting changes nothing for a call larger than the limit lets through at
	// once, so the caller is not told how long to wait: a client told to wait
	// asks the same thing again.
	t.Run("a call that costs more than is ever let through is told to ask for less", func(t *testing.T) {
		x := require.New(t)

		ran, err := limited(t, &stubLimiter{retry: grpcx.Never}, by("tenant:acme"))
		x.False(ran)
		x.Equal(codes.ResourceExhausted, status.Code(err))
		x.Contains(status.Convert(err).Message(), "ask for less")

		s, _ := status.FromError(err)
		for _, d := range s.Details() {
			_, retry := d.(*errdetails.RetryInfo)
			x.False(retry, "a call that can never run was told when to ask again")
		}
	})
}

func TestMemLimiter(t *testing.T) {
	t.Run("the burst goes through and the next one does not", func(t *testing.T) {
		x := require.New(t)

		// One a second, two at once. Slow enough that the third call is refused
		// however long the two before it took.
		l := grpcx.NewLimiter(1, 2)
		for range 2 {
			_, ok := l.Allow(t.Context(), "tenant:acme", 1)
			x.True(ok)
		}

		retry, ok := l.Allow(t.Context(), "tenant:acme", 1)
		x.False(ok)
		x.Positive(retry)
		x.LessOrEqual(retry, time.Second, "a token a second, so never longer than that")
	})

	t.Run("one caller over the line does not refuse another", func(t *testing.T) {
		x := require.New(t)

		// The whole of what "per caller" means, and the thing a single global
		// bucket would get wrong.
		l := grpcx.NewLimiter(1, 1)
		_, ok := l.Allow(t.Context(), "tenant:acme", 1)
		x.True(ok)
		_, ok = l.Allow(t.Context(), "tenant:acme", 1)
		x.False(ok)

		_, ok = l.Allow(t.Context(), "tenant:hooli", 1)
		x.True(ok)
	})

	t.Run("a burst below one is one", func(t *testing.T) {
		x := require.New(t)

		// A bucket that never holds a whole token is a limiter that refuses
		// everything, which is not what anybody writing a zero meant.
		l := grpcx.NewLimiter(1, 0)
		_, ok := l.Allow(t.Context(), "tenant:acme", 1)
		x.True(ok)
	})

	t.Run("calls at the same moment are still counted once each", func(t *testing.T) {
		x := require.New(t)

		// Slow enough that nothing refills while the test runs, so the number
		// that gets through is exactly the burst and not "about" it. A server
		// answers many calls at once, and a bucket that is read and written
		// without a lock lets more through the busier it gets -- which is the
		// moment the limit was for.
		l := grpcx.NewLimiter(0.001, 10)

		var (
			wg  sync.WaitGroup
			ok  atomic.Int64
			ctx = t.Context()
		)
		for range 100 {
			wg.Go(func() {
				if _, allowed := l.Allow(ctx, "tenant:acme", 1); allowed {
					ok.Add(1)
				}
			})
		}
		wg.Wait()

		x.Equal(int64(10), ok.Load())
	})

	t.Run("a rate that is not a rate is a wiring mistake", func(t *testing.T) {
		x := require.New(t)

		// At wiring time rather than on the first call, and not silently "no
		// limit" either: a server that answers nothing is not what a zero here
		// was meant to say. Saying no limit is installing none.
		x.Panics(func() { grpcx.NewLimiter(0, 1) })
		x.Panics(func() { grpcx.NewLimiter(-1, 1) })
	})
}

// TestNoLimitIsNoLimit is about the shape the interceptors are taken in.
//
// [grpcx.Limit] answers with no options at all when there is nothing to limit,
// and for a while that was the only guard there was -- so the bare interceptor,
// which is what a chain and a batch reach for, dereferenced a nil limiter on
// the first request it saw.
func TestNoLimitIsNoLimit(t *testing.T) {
	x := require.New(t)

	served := false
	handler := func(ctx context.Context, req any) (any, error) {
		served = true
		return nil, nil
	}

	for _, at := range []grpc.UnaryServerInterceptor{
		grpcx.LimitUnary(nil, nil, nil),
		grpcx.LimitUnary(nil, func(context.Context, string) string { return "who" }, nil),
		grpcx.LimitUnary(grpcx.NewLimiter(1, 1), nil, nil),
	} {
		served = false
		_, err := at(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/a.B/C"}, handler)
		x.NoError(err)
		x.True(served, "a call was refused by a limit nobody configured")
	}
}
