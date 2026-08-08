package config

import (
	"math"
	"slices"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/lesomnus/payday/batch"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
)

// DefaultAddr is where a server that was not told where to listen listens.
//
// It is a default and not a fallback the app writes in, because the alternative
// to having one is worse than having the wrong one: an empty address is a
// listener on a port the kernel picked, which serves, answers health checks,
// and is reachable by nobody.
const DefaultAddr = ":50051"

type ServerConfig struct {
	// Addr is the address the gRPC server listens on, e.g. ":50051". Nothing
	// written down is [DefaultAddr]; see [ServerConfig.ListenAddr].
	Addr string `yaml:"addr"`

	TLS TLSConfig `yaml:"tls"`

	// MaxRecvMsgSize is the largest message the server accepts, in bytes.
	// Zero leaves gRPC's own limit, which is 4 MiB.
	MaxRecvMsgSize int `yaml:"max_recv_msg_size"`
	// MaxSendMsgSize is the largest message the server sends, in bytes. Zero
	// leaves gRPC's own limit, which is no limit at all.
	MaxSendMsgSize int `yaml:"max_send_msg_size"`

	// MaxConcurrentStreams is how many calls one connection may have in flight
	// at a time. Zero leaves gRPC's own limit.
	MaxConcurrentStreams uint32 `yaml:"max_concurrent_streams"`

	// Timeout is how long a call that arrived without a deadline of its own is
	// given. A call that named one is left alone, however far away it is.
	//
	// Zero is [grpcx.DefaultTimeout], and a **negative** one caps nothing at
	// all. That is the odd case and it reads oddly on purpose: a server that
	// lets a call run forever is a thing to mean rather than to end up with,
	// and this way the ordinary meaning of an unwritten field is the ordinary
	// answer.
	Timeout time.Duration `yaml:"timeout"`

	// AllowReflection serves the reflection service, which is what lets grpcurl
	// and the like ask the server what it offers without holding the protobuf
	// definitions.
	//
	// Handy, and a listing of every RPC there is. What is unwritten is off, so
	// a deployment that trimmed its file down to what it meant does not serve
	// it by having forgotten to say no.
	AllowReflection bool `yaml:"allow_reflection"`

	// MaxBatchOps is how many operations one batch may carry; zero is
	// [batch.MaxOps]. A batch is a transaction, and one held open long enough
	// is a lock held against every other writer.
	MaxBatchOps int `yaml:"max_batch_ops"`

	// AllowGeneralWrites serves `Patch` and `Apply`, the two RPCs every
	// generated service has that can write anything the schema holds. It is
	// **off** unless it is turned on, and turning it on is a decision about the
	// API rather than about a deployment: what a caller may change, and under
	// what conditions, is not something a general write can be told.
	//
	// It closes them at the transport and not in the server stack, so an RPC
	// written by hand goes on being implemented with them. That is what they
	// are for.
	AllowGeneralWrites bool `yaml:"allow_general_writes"`

	Limit LimitConfig `yaml:"limit"`

	Http HttpConfig `yaml:"http"`

	Keepalive KeepaliveConfig `yaml:"keepalive"`
}

// HttpConfig is the second listener: what an app serves to something that
// cannot speak gRPC.
//
// It is a listener of its own and not the same port, because gRPC served
// through `net/http` gives up the transport gRPC brings. Nothing is served at
// all unless an address is written down.
type HttpConfig struct {
	// Addr is the address to listen on, e.g. ":8080". Nothing written down is
	// no second listener.
	//
	// There is no default here, which is the difference between this and
	// [ServerConfig.Addr]: not serving is a perfectly good thing for this one
	// to do, so an unwritten field has an answer already.
	Addr string `yaml:"addr"`

	// AllowWeb translates the protocols a browser can speak -- Connect and
	// gRPC-Web -- into the gRPC server, so a page reaches the same handlers,
	// the same interceptors and the same wall as anything else. See `web`.
	//
	// It is what a UI needs and it is not only for one: a Connect call is a
	// POST with a JSON body, so it is also the endpoint anything that has an
	// HTTP client and no protobuf can reach.
	AllowWeb bool `yaml:"allow_web"`

	// Origins are the origins a browser may call from. Nothing written down is
	// none, which is not "nothing works": a page served from this same origin
	// makes no cross-origin request and needs no answer from here. What it
	// means is that a UI somewhere else -- which is every `npm run dev` -- has
	// to be named.
	//
	// `["*"]` is every page on the internet, which is a thing to mean rather
	// than to reach for.
	//
	// It is not the wall and it is not authentication. It says which page may
	// make the call; who is calling and what they may see is decided where it
	// is decided for everything else.
	Origins []string `yaml:"origins"`

	// AllowPprof serves the profiles under `/debug/pprof/`. It should stay off
	// anywhere a stranger can reach: it is the heap, the goroutines, and the
	// ability to make the process spend thirty seconds profiling itself.
	AllowPprof bool `yaml:"allow_pprof"`
}

// Serves reports whether there is a second listener at all.
func (c HttpConfig) Serves() bool {
	return c.Addr != ""
}

// Origin reports whether a browser at this origin may make a grpc-web call,
// and is nothing when no origin was named.
func (c HttpConfig) Origin() func(origin string) bool {
	if len(c.Origins) == 0 {
		return nil
	}
	if slices.Contains(c.Origins, "*") {
		return func(string) bool { return true }
	}

	return func(v string) bool { return slices.Contains(c.Origins, v) }
}

// LimitConfig is how often one caller may call. What "one caller" is, is said
// where callers are worked out and not here; see `gate.ByTenant`.
//
// It is **off** unless a rate is written down, and that is a decision rather
// than an oversight. A deadline can be defaulted because the default only
// touches a call that named none, and a wrong one costs a call. A rate cannot:
// a number picked by a framework is a number nobody measured, and the traffic
// it would refuse is real traffic, on the day the app is busiest. So the
// deployment chooses it and nothing chooses it for them.
type LimitConfig struct {
	// Rate is how many calls a second one caller may make, kept up. Not
	// positive -- which is what an absent block says -- is no limit at all.
	Rate float64 `yaml:"rate"`

	// Burst is how many may arrive at once before the rate is what is left.
	// Unset is one second's worth, rounded up, and never less than one: a
	// burst below one is a limiter that refuses everything.
	//
	// It is the number that decides whether an honest client is refused. A
	// client that sends its work in batches is bursty by construction, and a
	// burst of one turns that into an error rather than into a wait.
	Burst int `yaml:"burst"`
}

// Limits reports whether a limit was configured at all.
func (c LimitConfig) Limits() bool {
	return c.Rate > 0
}

// BurstOr is the burst that was configured, or one second's worth of the rate.
func (c LimitConfig) BurstOr() int {
	if c.Burst > 0 {
		return c.Burst
	}

	return max(1, int(math.Ceil(c.Rate)))
}

// Limiter counts the calls of one caller, or is nothing if the configuration
// named no rate.
//
// The nil is written out rather than returned as a typed one, since a typed nil
// in an interface is not nil and [grpcx.Limit] reads it as a limiter that
// refuses to answer.
func (c ServerConfig) Limiter() grpcx.Limiter {
	if !c.Limit.Limits() {
		return nil
	}

	return grpcx.NewLimiter(c.Limit.Rate, c.Limit.BurstOr())
}

// KeepaliveConfig is when a connection is hung up on and when it is asked
// whether it is still there. Every duration is left to gRPC if it is zero.
type KeepaliveConfig struct {
	// MaxConnectionIdle closes a connection that has had no call for this
	// long.
	MaxConnectionIdle time.Duration `yaml:"max_connection_idle"`
	// MaxConnectionAge closes a connection this long after it was opened,
	// whatever it is doing. Behind a load balancer that only balances new
	// connections, this is what makes it balance anything at all.
	MaxConnectionAge time.Duration `yaml:"max_connection_age"`
	// MaxConnectionAgeGrace is how long a call has to finish once the
	// connection it is on has reached its age.
	MaxConnectionAgeGrace time.Duration `yaml:"max_connection_age_grace"`

	// Time is how long the server waits before asking an idle connection
	// whether it is still there, and Timeout is how long it waits for the
	// answer before hanging up.
	Time    time.Duration `yaml:"time"`
	Timeout time.Duration `yaml:"timeout"`

	// MinTime is how often a client may ask the same of the server. One that
	// asks more often is hung up on, which is what "too_many_pings" means.
	// Keep it below what the clients are configured with.
	MinTime time.Duration `yaml:"min_time"`
	// PermitWithoutStream lets a client ask while it has no call in flight.
	PermitWithoutStream bool `yaml:"permit_without_stream"`
}

// ListenAddr is where to listen, which is [DefaultAddr] when nothing was
// written down.
//
// It is answered here rather than written into the field once the file has been
// read, so that it cannot be answered at the wrong moment: a pass that fills in
// defaults runs either before the environment is read, where it is undone, or
// after, where it undoes it.
func (c ServerConfig) ListenAddr() string {
	if c.Addr == "" {
		return DefaultAddr
	}

	return c.Addr
}

// CallTimeout is how long a call that arrived without a deadline is given, in
// the terms [grpcx.WithDeadline] is told it in -- where what is not positive
// caps nothing.
func (c ServerConfig) CallTimeout() time.Duration {
	if c.Timeout == 0 {
		return grpcx.DefaultTimeout
	}

	return c.Timeout
}

// Closed names the methods this server does not serve at all.
func (c ServerConfig) Closed() func(method string) bool {
	if c.AllowGeneralWrites {
		return nil
	}

	return grpcx.GeneralWrite
}

// GrpcOptions is what the configuration says about the server itself, as
// opposed to what every call goes through, which is `grpcx`.
func (c ServerConfig) GrpcOptions() []grpc.ServerOption {
	opts := []grpc.ServerOption{}
	if v := c.MaxRecvMsgSize; v > 0 {
		opts = append(opts, grpc.MaxRecvMsgSize(v))
	}
	if v := c.MaxSendMsgSize; v > 0 {
		opts = append(opts, grpc.MaxSendMsgSize(v))
	}
	if v := c.MaxConcurrentStreams; v > 0 {
		opts = append(opts, grpc.MaxConcurrentStreams(v))
	}

	k := c.Keepalive
	if k.MaxConnectionIdle > 0 || k.MaxConnectionAge > 0 || k.MaxConnectionAgeGrace > 0 || k.Time > 0 || k.Timeout > 0 {
		opts = append(opts, grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     k.MaxConnectionIdle,
			MaxConnectionAge:      k.MaxConnectionAge,
			MaxConnectionAgeGrace: k.MaxConnectionAgeGrace,
			Time:                  k.Time,
			Timeout:               k.Timeout,
		}))
	}
	if k.MinTime > 0 || k.PermitWithoutStream {
		opts = append(opts, grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             k.MinTime,
			PermitWithoutStream: k.PermitWithoutStream,
		}))
	}

	return opts
}

// Guard is what a batch enforces per operation, read off the same fields the
// interceptors are built from.
//
// It exists so that there is **one** place. A batch checks by hand what the
// transport checks by interceptor, and the two are only ever in step if they
// come from the same source -- written out twice they will disagree, and the
// direction they disagree in is the one where the batch allows more.
//
// The policy is a parameter because it is not configuration: it is a thing an
// app builds, and the interceptor takes it the same way.
func (c ServerConfig) Guard(p gate.Policy) batch.Guard {
	return batch.Guard{
		Closed:  c.Closed(),
		Limiter: c.Limiter(),
		By:      gate.ByTenant(),
		Policy:  p,
		Max:     c.MaxBatchOps,
	}
}
