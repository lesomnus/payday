package grpcx

import (
	"context"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Limiter says whether a call may run now.
//
// It is an interface for the same reason [Policy] in `server/gate` is one: what
// is bundled here counts in one process, and a deployment that needs the count
// to mean something across all of them replaces it with something that counts
// where the processes can see each other. See [MemLimiter] for what that
// difference costs.
type Limiter interface {
	// Allow reports whether a call counted against `key` may run now, and if
	// not, how long it would be before one counted against the same key could.
	//
	// The duration is answered rather than a bare no because the caller is told
	// it. A refusal a client cannot time is a client that tries again at once,
	// which is the traffic the limit was for.
	Allow(ctx context.Context, key string) (time.Duration, bool)
}

// Limit refuses a call that arrives faster than `l` allows.
//
// `by` says what a call is counted against. It is a function and not a rule of
// this package because who a caller is belongs to the layers that work it out:
// `server/gate` says the key is the Tenant, and an app with something else to
// count against says that instead. A key it answers empty for is a call that is
// not counted at all.
//
// A nil `l` or a nil `by` is no limit, so a deployment that configured none is
// served by exactly the chain it was before.
//
// It goes **behind whatever says who is calling**, since the key is about them,
// and **in front of whatever consults a policy**, since asking one is work that
// a caller over their line should not be able to ask for. It is behind the log,
// so a refused call is logged like any other that failed.
//
// A stream is one call here, counted when it opens and never again however long
// it runs. That is the same crudeness as [Deadline] leaving streams alone, and
// for the same reason: what a stream costs is not known when it starts.
func Limit(l Limiter, by func(ctx context.Context, method string) string) []grpc.ServerOption {
	if l == nil || by == nil {
		return nil
	}

	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(LimitUnary(l, by)),
		grpc.ChainStreamInterceptor(LimitStream(l, by)),
	}
}

func LimitUnary(l Limiter, by func(ctx context.Context, method string) string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := allow(ctx, l, by, info.FullMethod); err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

func LimitStream(l Limiter, by func(ctx context.Context, method string) string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := allow(ss.Context(), l, by, info.FullMethod); err != nil {
			return err
		}

		return handler(srv, ss)
	}
}

func allow(ctx context.Context, l Limiter, by func(ctx context.Context, method string) string, method string) error {
	key := by(ctx, method)
	if key == "" {
		return nil
	}

	// Asked once. A limiter counts what it is asked, so asking again to find
	// out how long to wait would charge the caller for having been refused.
	retry, ok := l.Allow(ctx, key)
	if ok {
		return nil
	}

	return errLimited(retry)
}

// errLimited is ResourceExhausted, which is what the status codes have for a
// caller that is asking for more than there is to give, and it carries how long
// to wait as [errdetails.RetryInfo] -- the way the API conventions say it, and
// the way a generated client reads it whatever it was configured with.
//
// Note that it says nothing about *which* limit was reached or how much of it
// is left. That is on purpose here: how many calls a moment holds is something
// a caller can measure by making them, and there is nothing to gain by helping.
// An app whose limits are part of what it sells says the opposite, and says it
// in a header.
//
// A client that has a gRPC retry policy would also honour a `grpc-retry-
// pushback-ms` trailer, which is the same number said where the transport
// rather than the application reads it. It is not set here because this app
// does not know that its clients retry, and a pushback nobody reads is a
// trailer on every refusal.
func errLimited(retry time.Duration) error {
	s := status.New(codes.ResourceExhausted, "too many calls; wait and ask again")
	if retry <= 0 {
		return s.Err()
	}

	d, err := s.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(retry)})
	if err != nil {
		// Only if the detail cannot be marshalled, which it can. The refusal is
		// the answer; how long to wait is the courtesy.
		return s.Err()
	}

	return d.Err()
}

var _ Limiter = (*MemLimiter)(nil)

// MemLimiter is a token bucket per key, held in this process.
//
// **In this process** is the whole of the caveat, and it is worth reading twice
// before a number is chosen: three replicas behind a load balancer are three
// buckets, so a limit of a hundred a second is three hundred a second to
// anybody the balancer spreads around, and which of the three a call lands on
// is not something either end decides. That is fine for what a limit like this
// is usually for -- keeping one caller from taking a whole process -- and it is
// wrong for a quota somebody is billed against, which has to be counted
// somewhere every process can see. [Limiter] is where that goes.
//
// The keys are forgotten as they go quiet, and what makes that safe is that a
// full bucket answers exactly the way a bucket that was never made does. So the
// map holds the keys that are *behind*, not the keys that have ever been seen.
// That bound is enough here because the keys are the Tenants, and a deployment
// decides how many of those there are. A key a caller can invent -- an address,
// a token -- is a map a caller can grow between two sweeps, and wants something
// that forgets by size rather than by silence.
type MemLimiter struct {
	rate  rate.Limit
	burst int

	// full is how long an empty bucket takes to fill, which is the soonest a
	// sweep can find anything it did not find last time.
	full time.Duration

	mu    sync.Mutex
	at    map[string]*rate.Limiter
	swept time.Time
}

// NewLimiter answers with a limiter that lets `r` calls a second through for
// each key, `b` of which may arrive at once.
//
// A rate that is not positive is not a limiter -- it is a server that answers
// nothing -- so it panics here, at wiring time, rather than at the first call.
// A configuration that means "no limit" says so by installing none; see
// [Limit], which takes a nil one.
func NewLimiter(r float64, b int) *MemLimiter {
	if r <= 0 {
		panic("grpcx: a rate limit must be positive; install no limiter instead")
	}
	if b < 1 {
		// A burst below one is a bucket that never holds a whole token, which
		// is a limiter that refuses everything.
		b = 1
	}

	// Sweeping more often than a bucket can fill finds the same keys again, and
	// a floor keeps a generous rate from walking the map on every call.
	full := time.Duration(float64(b) / r * float64(time.Second))
	if full < time.Second {
		full = time.Second
	}

	return &MemLimiter{
		rate:  rate.Limit(r),
		burst: b,
		full:  full,
		at:    map[string]*rate.Limiter{},
	}
}

func (l *MemLimiter) Allow(_ context.Context, key string) (time.Duration, bool) {
	return l.allow(time.Now(), key)
}

func (l *MemLimiter) allow(now time.Time, key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)

	b, ok := l.at[key]
	if !ok {
		b = rate.NewLimiter(l.rate, l.burst)
		l.at[key] = b
	}
	if b.AllowN(now, 1) {
		return 0, true
	}

	// What the bucket holds, rather than what a reservation would say. Both
	// answer the same number and [rate.Limiter.Reserve] takes the token to do
	// it, which would charge a caller for being refused.
	held := b.TokensAt(now)
	return time.Duration(math.Ceil((1 - held) / float64(l.rate) * float64(time.Second))), false
}

// Len is how many keys are being counted, which is the number the sweep keeps
// from growing with every caller that has ever called.
func (l *MemLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.at)
}

// sweep forgets every key whose bucket is full, which changes no answer: the
// bucket a key gets when it comes back is the full one it left behind.
//
// It runs from inside [MemLimiter.allow] rather than from a goroutine of its
// own, so there is nothing to start and nothing to stop -- a limiter nobody is
// calling is one with nothing to forget.
func (l *MemLimiter) sweep(now time.Time) {
	if now.Sub(l.swept) < l.full {
		return
	}
	l.swept = now

	for k, b := range l.at {
		if b.TokensAt(now) >= float64(l.burst) {
			delete(l.at, k)
		}
	}
}
