package gate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/batch"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
)

const (
	ownMethod   = "/apptest.InvitationService/Accept"
	otherMethod = "/apptest.RobotService/Get"
)

func accountMethods(m string) bool { return m == ownMethod }

// asAccount is a call an account makes as itself (payday#184).
func asAccount(account pdid.Id) context.Context {
	return frame.Into(context.Background(), frame.OfAccount(account, frame.Whole(), accountMethods))
}

// wheres is a policy that would answer every tenant, and remembers whether it
// was asked to.
type wheres struct {
	policy
	whereAsked bool
}

func (p *wheres) Where(context.Context, gate.Call) (frame.Tenants, error) {
	p.whereAsked = true
	return frame.Everything, nil
}

// TestACallAnAccountMakesAsItselfIsOfItsOwnMethods.
//
// Its frame says which methods it was made for, and no policy widens that: the
// method is refused before a policy is asked, since an answer of one cannot
// make it any other call.
func TestACallAnAccountMakesAsItselfIsOfItsOwnMethods(t *testing.T) {
	x := require.New(t)
	a := pdid.New(domain)

	p := &policy{}
	_, err := gate.Decide(asAccount(a), p, otherMethod)
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Empty(p.asked, "refused before the policy is asked")

	ctx, err := gate.Decide(asAccount(a), p, ownMethod)
	x.NoError(err)
	x.True(frame.Must(ctx).AsAccount())
	x.Len(p.asked, 1, "a policy is still asked about its own methods: it may refuse them")
	x.True(p.asked[0].AsAccount)
	x.Equal(a, p.asked[0].Account)
	x.Equal(a, p.asked[0].Actor)
	x.True(p.asked[0].Tenant.IsZero())

	// And a policy refusing is the answer.
	_, err = gate.Decide(asAccount(a), &policy{may: status.Error(codes.FailedPrecondition, "agree first")}, ownMethod)
	x.Equal(codes.FailedPrecondition, status.Code(err))
}

// TestACallAnAccountMakesAsItselfSeesNoTenant, whatever a policy would say.
//
// `Where` is not asked: a policy that answered every tenant for somebody who
// is nobody's member would hand them the deployment, and what such a call
// reads is said by the layer serving its method instead.
func TestACallAnAccountMakesAsItselfSeesNoTenant(t *testing.T) {
	x := require.New(t)

	for _, p := range []gate.Policy{nil, &wheres{}} {
		ctx, err := gate.Decide(asAccount(pdid.New(domain)), p, ownMethod)
		x.NoError(err)

		f := frame.Must(ctx)
		x.False(f.Scope.All())
		x.Empty(f.Scope.Ids())
		if w, ok := p.(*wheres); ok {
			x.False(w.whereAsked)
		}
	}
}

// TestTheGateAsksAboutTheMethodTheCallerAskedFor.
//
// `gate.AsAccount` is what the generated gate asks of every method it serves.
// It reads the method gRPC dispatched -- a layer serving an account's own
// method reads through the servers behind it, and those reads are the wall's
// -- and the one being served when nothing was dispatched.
func TestTheGateAsksAboutTheMethodTheCallerAskedFor(t *testing.T) {
	a := pdid.New(domain)

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		served string
		code   codes.Code
	}{
		{"a call nobody vouched for is not its to refuse", context.Background(), otherMethod, codes.OK},
		{"a membership's call is not its to refuse", held(), otherMethod, codes.OK},
		{"an account's call to another method", batch.AsOp(asAccount(a), otherMethod), otherMethod, codes.PermissionDenied},
		{"an account's call to its own method", batch.AsOp(asAccount(a), ownMethod), ownMethod, codes.OK},
		{"what its own method reads, through the servers behind", batch.AsOp(asAccount(a), ownMethod), otherMethod, codes.OK},
		{"with nothing dispatched, what is being served", asAccount(a), otherMethod, codes.PermissionDenied},
		{"with nothing dispatched, its own method", asAccount(a), ownMethod, codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := gate.AsAccount(tc.ctx, tc.served, accountMethods)
			require.Equal(t, tc.code, status.Code(err))
		})
	}

	t.Run("a frame made for a method the schema does not mark is refused it", func(t *testing.T) {
		x := require.New(t)

		ctx := batch.AsOp(frame.Into(context.Background(), frame.OfAccount(a, frame.Whole(), func(string) bool { return true })), otherMethod)
		x.Equal(codes.PermissionDenied, status.Code(gate.AsAccount(ctx, otherMethod, accountMethods)))
		x.Equal(codes.PermissionDenied, status.Code(gate.AsAccount(batch.AsOp(asAccount(a), ownMethod), ownMethod, nil)),
			"no methods are an account's own")
	})
}

// TestACallAnAccountMakesAsItselfIsCountedAgainstIt rather than against every
// such call there is: it is in no tenant, and one count for all of them would
// be one account's limit raised by every other.
func TestACallAnAccountMakesAsItselfIsCountedAgainstIt(t *testing.T) {
	x := require.New(t)

	a, b := pdid.New(domain), pdid.New(domain)
	key := gate.ByTenant()
	x.Equal("account:"+a.String(), key(asAccount(a), ownMethod))
	x.NotEqual(key(asAccount(a), ownMethod), key(asAccount(b), ownMethod))
	x.Equal(acme.String(), key(held(), otherMethod))
}

// TestTheInterceptorRefusesAnAccountAnotherMethod, as Decide does: what a
// stream is opened for is asked about too.
func TestTheInterceptorRefusesAnAccountAnotherMethod(t *testing.T) {
	x := require.New(t)

	served := false
	_, err := gate.Unary(nil)(asAccount(pdid.New(domain)), nil,
		&grpc.UnaryServerInfo{FullMethod: otherMethod},
		func(context.Context, any) (any, error) { served = true; return nil, nil })
	x.Equal(codes.PermissionDenied, status.Code(err))

	err = gate.Stream(nil)(nil, stream{ctx: asAccount(pdid.New(domain))},
		&grpc.StreamServerInfo{FullMethod: "/apptest.RobotService/Watch"},
		func(any, grpc.ServerStream) error { served = true; return nil })
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.False(served)
}
