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
	// Allow reports whether a call counted against `key`, which costs `n`
	// calls ([Cost]), may run now, and if not, how long it would be before one
	// counted against the same key could -- or [Never], when no wait would do,
	// because `n` is more than the limiter lets through at once.
	//
	// The duration is answered rather than a bare no because the caller is told
	// it. A refusal a client cannot time is a client that tries again at once,
	// which is the traffic the limit was for.
	Allow(ctx context.Context, key string, n int) (time.Duration, bool)
}

// Never is the wait a [Limiter] answers for a call it will never let through
// as it is: one that costs more than it lets through at once. Waiting changes
// nothing, so the caller is told to ask for less rather than when to ask again.
const Never = time.Duration(math.MaxInt64)

// Cost is how many calls a request counts as: what a limit charges for it, and
// what a batch adds up of its operations.
//
// Nearly every call is one. An AddMany of a thousand rows is a thousand Adds'
// worth of work sent as one call, and counted as one it is a thousand calls'
// worth for the price of one -- the thing a batch counts its operations one by
// one to stop. Which RPCs those are is the schema's to say, so an app's Cost is
// generated (`pd.Cost`); nil counts every call as one.
type Cost func(method string, req any) int

// CostOf is what `c` says a call to `method` asking `req` costs, and one when
// there is no `c` or it says less: a call is at least a call.
func CostOf(c Cost, method string, req any) int {
	if c == nil {
		return 1
	}

	return max(c(method, req), 1)
}

// Limit refuses a call that arrives faster than `l` allows.
//
// `by` says what a call is counted against. It is a function and not a rule of
// this package because who a caller is belongs to the layers that work it out:
// `server/gate` says the key is the Tenant, and an app with something else to
// count against says that instead. A key it answers empty for is a call that is
// not counted at all.
//
// `cost` is how many calls each one counts as; see [Cost]. A nil `l` or a nil
// `by` is no limit, so a deployment that configured none is served by exactly
// the chain it was before.
//
// It goes **behind whatever says who is calling**, since the key is about them,
// and **in front of whatever consults a policy**, since asking one is work that
// a caller over their line should not be able to ask for. It is behind the log,
// so a refused call is logged like any other that failed.
//
// A stream is one call here, counted when it opens and never again however long
// it runs. That is the same crudeness as [Deadline] leaving streams alone, and
// for the same reason: what a stream costs is not known when it starts.
func Limit(l Limiter, by func(ctx context.Context, method string) string, cost Cost) []grpc.ServerOption {
	if l == nil || by == nil {
		return nil
	}

	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(LimitUnary(l, by, cost)),
		grpc.ChainStreamInterceptor(LimitStream(l, by)),
	}
}

// LimitUnary is [Limit]'s unary half: a call charged what `cost` says it is.
func LimitUnary(l Limiter, by func(ctx context.Context, method string) string, cost Cost) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := Allow(ctx, l, by, info.FullMethod, CostOf(cost, info.FullMethod, req)); err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// LimitStream is [Limit]'s stream half: a stream is one call.
func LimitStream(l Limiter, by func(ctx context.Context, method string) string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := Allow(ss.Context(), l, by, info.FullMethod, 1); err != nil {
			return err
		}

		return handler(srv, ss)
	}
}

// Allow is the check the two interceptors are made of, as a function: a call to
// `method` that costs `n` calls ([Cost]), charged against whoever `by` says is
// calling.
//
// It is exported because a batch of calls inside one call has to count each of
// them: a batch is one method to gRPC, so an interceptor charges a caller once
// for a thousand operations. What a batch does with this is call it per
// operation, with what each costs, which is what makes the limit mean the same
// thing on both roads.
func Allow(ctx context.Context, l Limiter, by func(ctx context.Context, method string) string, method string, n int) error {
	// No limit at all, which is what a deployment that configured none has.
	//
	// The guard is here and not only in [Limit] because the bare interceptors
	// are the shape everything else takes them in -- a chain assembled for a
	// server that is not gRPC's, a batch applying them per operation -- and
	// those reach this without going past that constructor. It used to be in
	// the constructor alone, and calling LimitUnary(nil, ...) directly was a
	// panic on the first request.
	if l == nil || by == nil {
		return nil
	}

	key := by(ctx, method)
	if key == "" {
		return nil
	}

	// Asked once. A limiter counts what it is asked, so asking again to find
	// out how long to wait would charge the caller for having been refused.
	retry, ok := l.Allow(ctx, key, max(n, 1))
	if ok {
		return nil
	}
	if retry == Never {
		return errTooMuch()
	}

	return errLimited(retry)
}

// errTooMuch is ResourceExhausted, as [errLimited] is, but with no time to wait:
// the call costs more than one caller is let through at once, so it is refused
// however long the caller waits, and what they are told is to ask for less in
// each call -- fewer items in an AddMany, say.
func errTooMuch() error {
	return status.Error(codes.ResourceExhausted, "this call asks for more than one caller may at once; ask for less in each call")
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
// each key, `b` of which may arrive at once -- calls as their [Cost] counts
// them, so `b` is also the most one call may cost.
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

// Allow takes `n` tokens of `key`'s bucket when it holds them.
//
// A call that costs more than the bucket holds when full is refused with
// [Never] and takes nothing: no wait fills a bucket past its burst. Charged
// only what the bucket holds, it would be a call of a thousand rows for the
// price of a burst, which is what the cost is there to stop; a deployment that
// means to take large calls says so with a burst at least as large.
func (l *MemLimiter) Allow(_ context.Context, key string, n int) (time.Duration, bool) {
	return l.allow(time.Now(), key, n)
}

func (l *MemLimiter) allow(now time.Time, key string, n int) (time.Duration, bool) {
	n = max(n, 1)
	if n > l.burst {
		return Never, false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)

	b, ok := l.at[key]
	if !ok {
		b = rate.NewLimiter(l.rate, l.burst)
		l.at[key] = b
	}
	if b.AllowN(now, n) {
		return 0, true
	}

	// What the bucket holds, rather than what a reservation would say. Both
	// answer the same number and [rate.Limiter.Reserve] takes the tokens to
	// do it, which would charge a caller for being refused.
	held := b.TokensAt(now)
	return time.Duration(math.Ceil((float64(n) - held) / float64(l.rate) * float64(time.Second))), false
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
