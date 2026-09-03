package authsession_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
)

func aKey(t *testing.T) []byte {
	t.Helper()

	k := make([]byte, authsession.KeySize)
	_, err := rand.Read(k)
	require.NoError(t, err)

	return k
}

func sealedWith(t *testing.T, keys ...[]byte) *authsession.Sealed {
	t.Helper()

	s, err := authsession.NewSealed(keys...)
	require.NoError(t, err)

	return s
}

// TestASealedSessionIsReadBackWithoutAStore: the contract with the browser is
// the one a store has -- a key goes out, the key comes back and names the
// session -- with nothing kept on the server.
func TestASealedSessionIsReadBackWithoutAStore(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	s := authsession.New(sealedWith(t, aKey(t)))

	holding := func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		return authsession.Session{
			Id:       "a-person",
			TenantId: "a-tenant",
			Grant:    frame.Whole().To("/app.ThingService/Get"),
			Held:     map[string]string{"token": "rd_something"},
		}, nil
	}
	c := signIn(t, s, holding)

	id, err := s.Handler().Handle(carrying(c))
	x.NoError(err)
	x.Equal("a-person", id.Id)
	x.Equal("a-tenant", id.TenantId)
	x.True(id.Grant.Allows("/app.ThingService/Get"))
	x.False(id.Grant.Allows("/app.ThingService/Erase"), "the grant did not survive the seal")

	v, err := s.Read(ctx, c.Value)
	x.NoError(err)
	x.Equal("rd_something", v.Held["token"], "what the app held did not ride with the session")
	x.Equal(c.Value, v.Key)
}

// TestAnotherReplicaOpensTheSameCookie is the reason to seal at all.
func TestAnotherReplicaOpensTheSameCookie(t *testing.T) {
	x := require.New(t)

	k := aKey(t)
	one := authsession.New(sealedWith(t, k))
	two := authsession.New(sealedWith(t, k))

	c := signIn(t, one, who("a-person", "a-tenant"))

	id, err := two.Handler().Handle(carrying(c))
	x.NoError(err, "a cookie minted on one replica was anonymous on the other")
	x.Equal("a-person", id.Id)
}

// TestASealedCookieThatWasTouchedNamesNothing, and is not a store that could
// not be reached: a forged or altered cookie is a wrong credential.
func TestASealedCookieThatWasTouchedNamesNothing(t *testing.T) {
	x := require.New(t)

	s := authsession.New(sealedWith(t, aKey(t)))
	c := signIn(t, s, who("a-person", "a-tenant"))

	// One character changed for another the alphabet allows, so that what is
	// tested is the seal and not the cookie parser.
	b := []byte(c.Value)
	i := len(b) / 2
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	touched := &http.Cookie{Name: c.Name, Value: string(b)}

	_, err := s.Handler().Handle(carrying(touched))
	x.ErrorIs(err, authsession.ErrNoSession)
	x.False(errors.Is(err, auth.ErrUnavailable))

	_, err = s.Handler().Handle(carrying(&http.Cookie{Name: c.Name, Value: "not-even-base64!"}))
	x.ErrorIs(err, authsession.ErrNoSession)
}

// TestAnotherKeyDoesNotOpenIt, and rotation: a new first key with the old one
// behind it opens both; the old one alone does not open the new.
func TestAnotherKeyDoesNotOpenIt(t *testing.T) {
	x := require.New(t)

	old, next := aKey(t), aKey(t)
	before := authsession.New(sealedWith(t, old))
	c := signIn(t, before, who("a-person", "a-tenant"))

	_, err := authsession.New(sealedWith(t, next)).Handler().Handle(carrying(c))
	x.ErrorIs(err, authsession.ErrNoSession, "a cookie sealed under one key opened under another")

	rotated := authsession.New(sealedWith(t, next, old))
	_, err = rotated.Handler().Handle(carrying(c))
	x.NoError(err, "a rotation that kept the old key could not open what it sealed")

	c2 := signIn(t, rotated, who("a-person", "a-tenant"))
	_, err = before.Handler().Handle(carrying(c2))
	x.ErrorIs(err, authsession.ErrNoSession, "the new first key was not the one sealing")
}

// TestASealedSessionHasOneClock: the idle deadline is a write to a store, and
// there is none, so it is not promised -- and the absolute one is kept.
func TestASealedSessionHasOneClock(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	s := authsession.New(sealedWith(t, aKey(t)), authsession.WithIdle(time.Hour))
	v, c, err := s.Mint(ctx, authsession.Session{Id: "a-person"})
	x.NoError(err)
	x.True(v.Idle.IsZero(), "a sealed session was given an idle clock nothing can move")
	x.False(v.Expires.IsZero())
	x.Equal(v.Expires.Unix(), c.Expires.Unix())

	_, c, err = s.Mint(ctx, authsession.Session{Id: "a-person", Expires: time.Now().Add(-time.Second)})
	x.NoError(err)
	_, err = s.Handler().Handle(carrying(c))
	x.ErrorIs(err, authsession.ErrNoSession, "an expired sealed session opened")
}

// TestAStoreThatCannotHoldSaysSo: a store with nowhere to put Held refuses the
// mint rather than dropping what the app believes it is holding. MemStore
// holds it.
func TestAStoreThatCannotHoldSaysSo(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	held := authsession.Session{Id: "a-person", Held: map[string]string{"token": "rd_x"}}

	_, _, err := authsession.New(&cannotHold{}).Mint(ctx, held)
	x.ErrorIs(err, authsession.ErrCannotHold)

	s := authsession.New(authsession.NewMemStore())
	v, _, err := s.Mint(ctx, held)
	x.NoError(err)
	got, err := s.Read(ctx, v.Key)
	x.NoError(err)
	x.Equal("rd_x", got.Held["token"])
}

type cannotHold struct{ authsession.MemStore }

func (*cannotHold) Put(ctx context.Context, v authsession.Session) error {
	if len(v.Held) > 0 {
		return authsession.ErrCannotHold
	}

	return nil
}

func TestNewSealedRefusesAKeyOfTheWrongSize(t *testing.T) {
	x := require.New(t)

	_, err := authsession.NewSealed()
	x.Error(err)
	_, err = authsession.NewSealed([]byte("short"))
	x.Error(err)
	_, err = authsession.NewSealed(aKey(t), []byte("short"))
	x.Error(err, "a bad key behind a good one was let through")
}
