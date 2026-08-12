package authsession_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
)

// actor is somebody a session can name. What it is does not matter here -- a
// resolver is what turns it into a row, and none of these has one.
const actor = "019ff410-e157-8c02-ac02-79d604a86685"

// TestMintIsWhatServeDoes, because `Serve` is now a wrapper and a wrapper that
// drifted from what it wraps is two sign-ins that agree today.
func TestMintIsWhatServeDoes(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s := authsession.New(authsession.NewMemStore(), authsession.Insecure())

	v, c, err := s.Mint(ctx, authsession.Session{Id: actor, Grant: frame.Whole()})
	x.NoError(err)
	x.NotEmpty(v.Key)
	x.Equal(v.Key, c.Value)

	// The three that are not optional, whatever transport asked for the cookie.
	x.True(c.HttpOnly)
	x.Equal(http.SameSiteLaxMode, c.SameSite)
	x.False(c.Expires.IsZero(), "a cookie that outlives its session")

	// And what `Serve` sets, for the same session.
	r := httptest.NewRequest(http.MethodPost, "/session", nil)
	w := httptest.NewRecorder()
	s.Serve(func(ctx context.Context, _ *http.Request) (authsession.Session, error) {
		return authsession.Session{Id: actor, Grant: frame.Whole()}, nil
	}).ServeHTTP(w, r)

	x.Equal(http.StatusNoContent, w.Result().StatusCode)

	got := w.Result().Cookies()
	x.Len(got, 1)
	x.Equal(c.Name, got[0].Name)
	x.Equal(c.Path, got[0].Path)
	x.Equal(c.HttpOnly, got[0].HttpOnly)
	x.Equal(c.SameSite, got[0].SameSite)
	x.NotEqual(c.Value, got[0].Value, "two sign-ins share a key")
}

// TestMintRefusesNobody is the failure that looks like success.
//
// A session with no actor makes a cookie that resolves to nothing on every
// later call. Every part of the sign-in succeeds and the page says they are in.
func TestMintRefusesNobody(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore(), authsession.Insecure())

	_, _, err := s.Mint(t.Context(), authsession.Session{Grant: frame.Whole()})
	x.ErrorIs(err, authsession.ErrNobody)
}

// TestEndIsWhatServeDoes -- the same, for signing out.
func TestEndIsWhatServeDoes(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	store := authsession.NewMemStore()
	s := authsession.New(store, authsession.Insecure())

	v, _, err := s.Mint(ctx, authsession.Session{Id: actor, Grant: frame.Whole()})
	x.NoError(err)

	_, err = store.Get(ctx, v.Key)
	x.NoError(err, "nothing was stored, so the delete below proves nothing")

	c := s.End(ctx, v.Key)

	// The row is gone, which is the part that matters: the key is dead in every
	// browser that had it, immediately.
	_, err = store.Get(ctx, v.Key)
	x.ErrorIs(err, authsession.ErrNoSession)

	// And cleared with the attributes it was set with, or a browser leaves the
	// original exactly where it was.
	x.Empty(c.Value)
	x.Equal(-1, c.MaxAge)
	x.True(c.HttpOnly)

	// Signing out twice is not an error, and still answers with the cookie.
	x.NotNil(s.End(ctx, ""))
	x.NotNil(s.End(ctx, "not-a-key"))
}

// TestMintFillsTheClocksItWasNotGiven, which is what a caller relies on when it
// hands over a session with neither set.
func TestMintFillsTheClocksItWasNotGiven(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore(),
		authsession.Insecure(),
		authsession.WithIdle(10*time.Minute),
		authsession.WithLifetime(time.Hour))

	v, _, err := s.Mint(t.Context(), authsession.Session{Id: actor})
	x.NoError(err)

	x.WithinDuration(time.Now().Add(time.Hour), v.Expires, time.Minute)
	x.WithinDuration(time.Now().Add(10*time.Minute), v.Idle, time.Minute)

	// And leaves what it was given. An app that hands somebody a short session
	// until they finish a second factor is saying so here.
	at := time.Now().Add(time.Minute)

	w, _, err := s.Mint(t.Context(), authsession.Session{Id: actor, Expires: at})
	x.NoError(err)
	x.True(at.Equal(w.Expires))
	x.False(w.Idle.After(at), "the idle clock outran the absolute one")
}
