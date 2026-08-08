package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
)

// TestACredentialCarriesWhenItStops is the field the seam was laid for.
//
// Adding it afterwards would have touched every handler, every store and the
// interceptor -- which is the whole argument for laying a seam before the thing
// that stands on it.
func TestACredentialCarriesWhenItStops(t *testing.T) {
	x := require.New(t)

	at := time.Now().Add(time.Hour)
	s := auth.NewMemTokenStore()
	s.Add("t0", auth.Identity{Tenant: "acme", Alias: "admin", Grant: frame.Whole()}, at)

	v, err := s.Lookup(t.Context(), "t0")
	x.NoError(err)
	x.WithinDuration(at, v.Expires, time.Second)

	// And a credential with nowhere to carry one says so by saying nothing.
	x.True(auth.Identity{}.Valid(time.Now()))
	x.True(v.Valid(time.Now()))
	x.False(v.Valid(at.Add(time.Second)))
}

// TestAStreamDoesNotOutliveItsCredential is the one thing a stream needs that a
// call does not.
//
// A call carries its credential every time and is refused at the next one. A
// stream carries it once, at the handshake, and is served until somebody hangs
// up -- so without this a ten-minute token is a stream that runs for a week.
// Over a WebSocket, where the browser puts the cookie on the handshake and
// never again, there is no next call to refuse at all.
func TestAStreamDoesNotOutliveItsCredential(t *testing.T) {
	x := require.New(t)

	s := auth.NewMemTokenStore()
	s.Add("t0", auth.Identity{Tenant: "acme", Alias: "admin", Grant: frame.Whole()},
		time.Now().Add(50*time.Millisecond))

	is := auth.InterceptorStream(auth.Bearer(s), known(), nil)

	// A handler that would go on forever, which is what a Watch is.
	served := make(chan struct{})
	err := is(nil, fakeStream{ctx: incoming("Bearer t0")},
		&grpc.StreamServerInfo{FullMethod: getMethod},
		func(_ any, ss grpc.ServerStream) error {
			close(served)
			<-ss.Context().Done()
			return ss.Context().Err()
		})

	<-served
	x.Equal(codes.Unauthenticated, status.Code(err))
	x.Contains(err.Error(), "expired")
}

// TestAStreamCutForOtherReasonsSaysSo is why the expiry cancels with a cause
// rather than setting a deadline.
//
// A stream that ran out of the time its caller asked for and one whose
// credential expired want different things done about them -- ask for longer,
// or go and get another credential -- so they must not arrive as the same
// answer.
func TestAStreamCutForOtherReasonsSaysSo(t *testing.T) {
	x := require.New(t)

	s := auth.NewMemTokenStore()
	s.Add("t0", auth.Identity{Tenant: "acme", Alias: "admin", Grant: frame.Whole()},
		time.Now().Add(time.Hour))

	is := auth.InterceptorStream(auth.Bearer(s), known(), nil)

	ctx, cancel := context.WithTimeout(incoming("Bearer t0"), 20*time.Millisecond)
	defer cancel()

	err := is(nil, fakeStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: getMethod},
		func(_ any, ss grpc.ServerStream) error {
			<-ss.Context().Done()
			return ss.Context().Err()
		})
	x.ErrorIs(err, context.DeadlineExceeded)
	x.NotEqual(codes.Unauthenticated, status.Code(err))
}

// TestAStreamWithNoExpiryIsNotCut, which is a header and a certificate: neither
// has anywhere to carry one, and a certificate's own life is the transport's
// business.
func TestAStreamWithNoExpiryIsNotCut(t *testing.T) {
	x := require.New(t)

	is := auth.InterceptorStream(auth.Plain(), known(), nil)

	err := is(nil, fakeStream{ctx: incoming("Plain @acme/admin")},
		&grpc.StreamServerInfo{FullMethod: getMethod},
		func(_ any, ss grpc.ServerStream) error {
			// Long enough that a mistaken zero expiry would have cut it.
			time.Sleep(30 * time.Millisecond)
			return nil
		})
	x.NoError(err)
}

// TestNothingIssuesCredentialsHere is the seam saying it is a seam.
func TestNothingIssuesCredentialsHere(t *testing.T) {
	x := require.New(t)

	// It compiles as an interface an app implements, and payday implements
	// none of it -- which is the same arrangement `gate.Policy` has.
	var i auth.Issuer
	x.Nil(i)

	var s auth.Sessions
	x.Nil(s)

	// And a Sessions is a TokenStore, which is how an app that writes one is
	// finished with the read path.
	var _ auth.TokenStore = s
}

// TestTheReferenceIssuerIssuesAndRevokes, which is the whole of the read path
// an app that writes a real one inherits: it hands the thing to `auth.Bearer`
// and is finished.
func TestTheReferenceIssuerIssuesAndRevokes(t *testing.T) {
	x := require.New(t)

	s := auth.NewMemIssuer()
	s.For = time.Hour

	c, err := s.Issue(t.Context(), auth.Subject{
		Tenant: "acme",
		Alias:  "admin",
		Grant:  frame.Whole().To(getMethod),
		How:    "password",
	})
	x.NoError(err)
	x.NotEmpty(c.Token)
	x.WithinDuration(time.Now().Add(time.Hour), c.Expires, time.Minute)

	// It is a TokenStore, so this is what `Bearer` does with it.
	ctx, err := serve(auth.Bearer(s), known(), nil, incoming("Bearer "+c.Token), getMethod)
	x.NoError(err)

	f, ok := frame.From(ctx)
	x.True(ok)
	x.Equal(adminId, f.Actor, "served as whoever the resolver found, not whoever the token said")

	// The narrowing the subject asked for survived, and nothing widened it.
	x.False(f.Grant.Allows("/app.RobotService/Erase"))

	// And revoking takes it away, twice without complaint: a token that was
	// already gone is not an error, since what the caller wanted is true.
	x.NoError(s.Revoke(t.Context(), c.Token))
	x.NoError(s.Revoke(t.Context(), c.Token))

	_, err = serve(auth.Bearer(s), known(), nil, incoming("Bearer "+c.Token), getMethod)
	x.Equal(codes.Unauthenticated, status.Code(err))
}
