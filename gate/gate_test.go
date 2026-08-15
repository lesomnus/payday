package gate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
)

const domain = pdid.Domain(200)

var (
	actor = pdid.New(domain)
	acme  = pdid.New(domain)
	hooli = pdid.New(domain)
)

// policy is a [gate.Policy] written out, which is what an app injects.
//
// It is here because payday has none: the interface is the seam for what payday
// does not decide, so the only implementations there will ever be are in the
// applications that make the decision. Without one in a test, `May` and `Where`
// are two methods nothing calls -- and the branches around them in
// [gate.Decide] have never run.
type policy struct {
	may   error
	where frame.Tenants
	fails error

	// asked is every call this was consulted about, so that a test can say what
	// it was asked rather than only what came back.
	asked []gate.Call
}

func (p *policy) May(_ context.Context, c gate.Call) error {
	p.asked = append(p.asked, c)

	return p.may
}

func (p *policy) Where(_ context.Context, _ gate.Call) (frame.Tenants, error) {
	if p.fails != nil {
		return frame.Nothing, p.fails
	}

	return p.where, nil
}

// held is a request from somebody in acme, carrying a credential that allows
// everything the actor does.
func held() context.Context {
	f := frame.New(actor, acme, frame.Whole())

	return frame.Into(context.Background(), f)
}

// TestACallNobodyVouchedForIsLeftAlone.
//
// A method that reached here without a frame is one served to anybody -- the
// health check, the reflection listing -- and there is nothing to decide about
// it. What matters is that the policy is not asked: it would be asked about an
// actor that is the zero identifier, and a policy written to answer about a row
// would either refuse the health check or, worse, answer for whoever that
// identifier happens to name.
func TestACallNobodyVouchedForIsLeftAlone(t *testing.T) {
	x := require.New(t)

	p := &policy{may: errors.New("this would refuse everything")}

	ctx := context.Background()
	got, err := gate.Decide(ctx, p, "/grpc.health.v1.Health/Check")
	x.NoError(err)
	x.Equal(ctx, got, "the context is handed on as it arrived")
	x.Empty(p.asked, "a policy is not asked about a call with no actor")
}

// TestWithoutAPolicyACallerSeesTheirOwnTenant, which is what payday shows on
// its own.
//
// A nil policy is not a missing piece and this is what it means. There is no
// caller it is not: nothing in here compares an identifier against a well-known
// one and answers with more.
func TestWithoutAPolicyACallerSeesTheirOwnTenant(t *testing.T) {
	x := require.New(t)

	ctx, err := gate.Decide(held(), nil, "/apptest.RobotService/List")
	x.NoError(err)

	f, ok := frame.From(ctx)
	x.True(ok)
	x.False(f.Scope.All())
	x.Equal([]pdid.Id{acme}, f.Scope.Ids())
}

// TestAPolicyIsAskedAboutTheMethodGrpcDispatched.
//
// The action is the whole reason `May` is separate from `Where`: what a caller
// may see is about them, and whether this call may be made at all is about the
// call. A policy that was not told which method could only answer the first.
func TestAPolicyIsAskedAboutTheMethodGrpcDispatched(t *testing.T) {
	x := require.New(t)

	p := &policy{where: frame.Only(acme)}

	_, err := gate.Decide(held(), p, "/apptest.RobotService/Erase")
	x.NoError(err)

	x.Len(p.asked, 1)
	x.Equal(actor, p.asked[0].Actor)
	x.Equal(acme, p.asked[0].Tenant)
	x.Equal("/apptest.RobotService/Erase", p.asked[0].Action)
}

// TestAPolicyRefusing is `May` answering, and the answer reaching the caller as
// it was written.
//
// A policy's error is the caller's answer, so a status stays a status: an app
// that says PermissionDenied with a sentence explaining what to do about it is
// not turned into Unknown on the way out.
func TestAPolicyRefusing(t *testing.T) {
	x := require.New(t)

	p := &policy{
		may:   status.Error(codes.PermissionDenied, "erasing is done by whoever runs this"),
		where: frame.Everything,
	}

	ctx, err := gate.Decide(held(), p, "/apptest.RobotService/Erase")
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Contains(err.Error(), "whoever runs this")
	x.Nil(ctx, "a refused call has no context to serve")
}

// TestAPolicyThatCannotAnswerRefusesTheCall.
//
// `Where` is a question with an answer somewhere else -- a membership table, a
// directory, something over a network -- so it can fail without anybody having
// decided anything. The failure has to be the call's, and this is the branch
// that says so: what it must not do is fall through to the tenant the caller
// belongs to, which is a scope nobody granted, arrived at because a lookup was
// down.
func TestAPolicyThatCannotAnswerRefusesTheCall(t *testing.T) {
	x := require.New(t)

	p := &policy{fails: status.Error(codes.Unavailable, "the directory is not answering")}

	ctx, err := gate.Decide(held(), p, "/apptest.RobotService/List")
	x.Equal(codes.Unavailable, status.Code(err))
	x.Nil(ctx)
}

// TestTheCredentialCanOnlyNarrowWhatAPolicyAnswers.
//
// The two are different questions and the order matters. A policy says what the
// actor may see; a credential says what this particular token may do with it.
// A token naming every tenant, held by somebody who may see one, still sees
// one -- and that is what makes handing out a scoped credential safe without
// re-deriving what its holder is allowed.
func TestTheCredentialCanOnlyNarrowWhatAPolicyAnswers(t *testing.T) {
	x := require.New(t)

	t.Run("a credential narrows a policy that answered with everything", func(t *testing.T) {
		p := &policy{where: frame.Everything}

		f := frame.New(actor, acme, frame.Whole().In(hooli))
		ctx, err := gate.Decide(frame.Into(context.Background(), f), p, "/apptest.RobotService/List")
		x.NoError(err)

		s := frame.Must(ctx).Scope
		x.False(s.All(), "a credential made for one tenant does not see all of them")
		x.Equal([]pdid.Id{hooli}, s.Ids())
	})

	t.Run("a credential that names more does not widen", func(t *testing.T) {
		p := &policy{where: frame.Only(acme)}

		f := frame.New(actor, acme, frame.Whole().In(acme, hooli))
		ctx, err := gate.Decide(frame.Into(context.Background(), f), p, "/apptest.RobotService/List")
		x.NoError(err)

		x.Equal([]pdid.Id{acme}, frame.Must(ctx).Scope.Ids())
	})
}

// TestTheInterceptorsServeWhatDecideAnswered, both shapes.
//
// `Decide` is exported because a batch applies it per operation, so it is the
// function that gets the tests. What is left to check about the two
// interceptors is that a handler is served with the context the decision was
// put on, and that a refusal never reaches one.
func TestTheInterceptorsServeWhatDecideAnswered(t *testing.T) {
	x := require.New(t)

	t.Run("unary", func(t *testing.T) {
		p := &policy{where: frame.Only(acme, hooli)}

		seen := frame.Nothing
		_, err := gate.Unary(p)(held(), nil,
			&grpc.UnaryServerInfo{FullMethod: "/apptest.RobotService/List"},
			func(ctx context.Context, _ any) (any, error) {
				seen = frame.Must(ctx).Scope
				return nil, nil
			})
		x.NoError(err)
		x.Equal([]pdid.Id{acme, hooli}, seen.Ids())
	})

	t.Run("stream", func(t *testing.T) {
		p := &policy{where: frame.Only(hooli)}

		seen := frame.Nothing
		err := gate.Stream(p)(nil, stream{ctx: held()},
			&grpc.StreamServerInfo{FullMethod: "/apptest.RobotService/Watch"},
			func(_ any, ss grpc.ServerStream) error {
				seen = frame.Must(ss.Context()).Scope
				return nil
			})
		x.NoError(err)
		x.Equal([]pdid.Id{hooli}, seen.Ids())
	})

	t.Run("a refusal never reaches the handler", func(t *testing.T) {
		p := &policy{may: status.Error(codes.PermissionDenied, "no")}

		served := false
		_, err := gate.Unary(p)(held(), nil,
			&grpc.UnaryServerInfo{FullMethod: "/apptest.RobotService/Erase"},
			func(context.Context, any) (any, error) { served = true; return nil, nil })
		x.Equal(codes.PermissionDenied, status.Code(err))
		x.False(served)

		err = gate.Stream(p)(nil, stream{ctx: held()},
			&grpc.StreamServerInfo{FullMethod: "/apptest.RobotService/Watch"},
			func(any, grpc.ServerStream) error { served = true; return nil })
		x.Equal(codes.PermissionDenied, status.Code(err))
		x.False(served)
	})
}

// stream is a [grpc.ServerStream] that is nothing but a context; the embedded
// interface panics on anything else, which is what a test wants.
type stream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s stream) Context() context.Context { return s.ctx }
