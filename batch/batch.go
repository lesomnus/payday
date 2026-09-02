// Package batch runs several writes as one transaction.
//
// # Why this does not undo the doctrine
//
// payday's position is that a server exposes the RPC it *means* rather than a
// general write, which is why `Patch` and `Apply` are closed by default. A
// batch looks like the opposite of that and is not, and the difference is worth
// being exact about: what the doctrine is against is an **untyped** write --
// one where the caller names a field and a value and the server has no rule
// about the pair. Every operation of a batch is a real RPC with its own
// validation, its own layers and its own audit. What a batch adds is that they
// hold or fall together.
//
// It is a server RPC and not a client convenience, so anything that speaks to
// this server has it -- a browser, a CLI, another service.
//
// # The hard part, which is this package
//
// payday enforces four things by looking at the method gRPC dispatched. A batch
// arrives as **one** method carrying many, so every one of them is enforced
// against `BatchService/Do` and none against what is actually being asked for:
//
//   - `grpcx.Closed`, which is what makes `Patch` and `Apply` unreachable.
//   - `frame.Grant.Allows`, which is how a credential is attenuated to a few
//     methods.
//   - `gate.Policy.May`, and the scope `gate.Policy.Where` answers with.
//   - `grpcx.Limit`, which counts calls.
//
// Left alone, a batch is a way to reach every one of them -- a caller whose
// token allows one method wraps any method they like in a batch and it is
// allowed, silently, with a trail that says it was. That is not a gap to close
// later: it is the whole security model with a hole in it, and the hole is
// exactly as wide as the batch RPC is useful.
//
// [Guard] is those four as functions, applied per operation. Every one of them
// was already a function or was made one -- `gate.Decide` says in its own
// comment that it is exported for this.
//
// # It is refused rather than defaulted
//
// A [Guard] that was not built from the same configuration the interceptors
// were built from is a batch that enforces less than the transport it sits
// behind. There is no zero value that means "allow everything" here: the
// generated `Batch` refuses one that [Guard.IsZero] answers for, with
// [ErrNoGuard], rather than serving it open.
package batch

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
)

// MaxOps is how many operations one batch may carry when nothing said.
//
// There is a limit at all because a batch is a transaction, and an unbounded
// batch is a transaction held open for as long as it takes -- which is a lock
// held against every other writer. The number is generous: what it is for is
// the request that would have run for a minute, not the one that writes twenty
// rows.
const MaxOps = 128

// ErrNoGuard is a batch built without the rules the transport enforces.
//
// It is a refusal at build time rather than a permissive default, because the
// permissive default is a privilege escalation and the way it would be found is
// somebody reading the wiring and noticing what is *not* there.
var ErrNoGuard = errors.New(
	"batch: a Guard is required. Every rule payday enforces by method name is " +
		"enforced against the batch Rpc and not against what it carries, so a " +
		"batch without one is a way past all of them")

// Guard is what the transport enforces by method name, as functions.
//
// It is built from the same configuration the interceptors are built from, and
// that is the only way it stays true: two places deciding what is closed will
// eventually disagree, and the direction they disagree in is the one where the
// batch allows more.
type Guard struct {
	// Closed reports a method no caller may ask for, and is
	// [config.ServerConfig.Closed] -- nil when the deployment allows the
	// general writes.
	Closed func(method string) bool

	// Policy is what says whether this actor may do this, and what they may
	// see while doing it. Nil is the same policy the interceptor uses when it
	// has none: the caller's own tenant and nothing else.
	Policy gate.Policy

	// Limiter and By are [grpcx.Limit]'s two halves. A batch of a thousand
	// operations counts as a thousand rather than as one, which is the whole
	// point -- an unguarded batch is a way to do a thousand calls' worth of
	// work for the price of one.
	Limiter grpcx.Limiter
	By      func(ctx context.Context, method string) string

	// Max is how many operations one batch may carry; zero is [MaxOps].
	Max int
}

// Allow applies every rule to one operation and answers with the context that
// operation is served with.
//
// The order is the interceptor chain's: what the credential allows, what is
// closed to everybody, how often, and then what the policy says -- which is
// last because it is the one that answers with something, and the others only
// answer yes or no.
func (g Guard) Allow(ctx context.Context, method string) (context.Context, error) {
	// The credential first. It is the cheapest and the most specific: a token
	// attenuated to two methods says so without anything being looked up.
	if f, ok := frame.From(ctx); ok && !f.Grant.Allows(method) {
		return nil, status.Errorf(codes.PermissionDenied,
			"%s: this credential is not for that", method)
	}

	if g.Closed != nil && g.Closed(method) {
		return nil, grpcx.ErrClosed(method)
	}

	if err := grpcx.Allow(ctx, g.Limiter, g.By, method); err != nil {
		return nil, err
	}

	// And the policy, which narrows the scope the operation runs in. Its answer
	// is the context, so this is the one that has to be threaded rather than
	// checked.
	return gate.Decide(ctx, g.Policy, method)
}

// IsZero reports a guard nobody filled in.
//
// It is what [ErrNoGuard] is about, and it is the zero value rather than a
// judgement about how permissive the fields are: a deployment that closes
// nothing and limits nothing is a real thing to be, and
// `config.ServerConfig.Guard` still fills in how a caller is counted -- so what
// this catches is a `batch.Guard{}` written by hand and nothing else.
func (g Guard) IsZero() bool {
	return g.Closed == nil && g.Policy == nil && g.Limiter == nil && g.By == nil && g.Max == 0
}

// Ops is how many operations this guard takes.
func (g Guard) Ops() int {
	if g.Max <= 0 {
		return MaxOps
	}

	return g.Max
}

// Check reports whether a batch of `n` operations is one this guard takes.
func (g Guard) Check(n int) error {
	switch max := g.Ops(); {
	case n == 0:
		return status.Error(codes.InvalidArgument, "a batch of no operations")
	case n > max:
		return status.Errorf(codes.InvalidArgument,
			"a batch of %d operations, and %d is the most this server takes: a batch is "+
				"a transaction, and one held open long enough is a lock held against "+
				"every other writer", n, max)
	}

	return nil
}

// AsOp answers with the context an operation is dispatched with: one in which
// gRPC's own question -- what method is being served? -- answers with the
// operation's method rather than the batch's.
//
// It exists for the trail. The recorder below the write sites fills in what
// the caller asked for by asking gRPC, because the sites cannot know it: one
// call writes through several servers, and only the transport knows which
// name it all happened under. Inside the batch handler the transport's answer
// is `BatchService/Do` -- true of the wire and false of every operation, so
// left alone the trail files a hundred different writes under one envelope
// and "who renamed this" answers with a method that renames nothing. The
// operation is what the caller asked for; the envelope is only how it
// travelled.
func AsOp(ctx context.Context, method string) context.Context {
	return grpc.NewContextWithServerTransportStream(ctx, opStream{
		outer:  grpc.ServerTransportStreamFromContext(ctx),
		method: method,
	})
}

// opStream is a [grpc.ServerTransportStream] that answers with the
// operation's method. That answer is the whole reason it exists; everything
// else it is asked, it hands to the stream the batch itself arrived on.
//
// Everything it is *asked*, which is not everything gRPC offers. Two of its
// helpers -- `grpc.SetSendCompressor` and `grpc.ClientSupportedCompressors` --
// reach past the interface for the concrete `*transport.ServerStream`, and no
// wrapper satisfies a type assertion: inside an operation the two answer
// "failed to fetch the stream from the given context" rather than delegating.
// Nothing here calls them and nothing generated does, and what they are about
// is the wire call's anyway -- there is one response with one encoding, and a
// batch of a hundred operations does not get a hundred of them to negotiate.
//
// Delegated rather than absorbed, because the batch is the only RPC on the
// wire and so its metadata is the only place a header can reach the caller.
// Absorbing would mean a handler that set one said nothing, silently -- the
// generated handlers set none, so what is preserved here is the behaviour of
// one somebody writes by hand. The cost of delegating is that every operation
// writes into the one wire call's metadata, but that is not a cost of this
// type: there is one wire call, and pretending each operation has its own
// would be the lie.
//
// `outer` is nil for a handler called without a transport, which is what a
// test calling `Do` directly is. A header then has nowhere to go, and the
// delegating methods answer an error rather than dropping it -- the same
// answer gRPC gives for a handler called this way without this type in the
// middle.
type opStream struct {
	outer  grpc.ServerTransportStream
	method string
}

// errNoTransport is metadata with no wire call to ride on.
var errNoTransport = status.Error(codes.Internal,
	"batch: there is no transport stream to carry it")

func (s opStream) Method() string { return s.method }

func (s opStream) SetHeader(md metadata.MD) error {
	if s.outer == nil {
		return errNoTransport
	}

	return s.outer.SetHeader(md)
}

func (s opStream) SendHeader(md metadata.MD) error {
	if s.outer == nil {
		return errNoTransport
	}

	return s.outer.SendHeader(md)
}

func (s opStream) SetTrailer(md metadata.MD) error {
	if s.outer == nil {
		return errNoTransport
	}

	return s.outer.SetTrailer(md)
}

// Failed says which operation of a batch refused, and why.
//
// A batch answers with one error and the caller has to know which of their
// operations it was about -- an index, since that is the only thing that names
// an operation. It keeps the code of what refused, so a caller acts on the same
// answer they would have got from making the call on its own.
func Failed(i int, method string, err error) error {
	s, _ := status.FromError(err)

	return status.Errorf(s.Code(), "ops[%d] %s: %s", i, method, s.Message())
}

// ErrNoMethod is an operation naming an Rpc this server does not have.
func ErrNoMethod(method string) error {
	return status.Errorf(codes.Unimplemented, "%s: no such method", method)
}

// ErrRequest is an operation whose request did not decode into what the method
// takes.
func ErrRequest(method string, err error) error {
	return status.Errorf(codes.InvalidArgument, "%s: %s", method, err)
}
