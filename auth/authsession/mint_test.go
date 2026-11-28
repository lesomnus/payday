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

// tableStore keeps a session's times as a table of PostgreSQL's does: to the
// microsecond, dropping what is below it.
type tableStore struct{ authsession.Store }

func (s tableStore) Put(ctx context.Context, v authsession.Session) error {
	v.Expires = v.Expires.Truncate(time.Microsecond)
	v.Idle = v.Idle.Truncate(time.Microsecond)
	v.DateAuthenticated = v.DateAuthenticated.Truncate(time.Microsecond)

	return s.Store.Put(ctx, v)
}

// TestMintAnswersWhatAStoreKeeps: a store is a table as often as it is a map,
// and PostgreSQL's keeps microseconds where the clock gives nanoseconds. The
// deadlines Mint computed had the clock's digits, and what Read then gave did
// not, so the session a caller held was not the session the store held. A
// deadline this package computes is cut before it is stored -- the two Mint
// fills, and the idle one when a Read moves it -- so the two are one value
// whatever the store.
//
// What a caller hands in is left exactly as it is, which
// TestMintFillsTheClocksItWasNotGiven and
// TestSessionUseNeverRefreshesAuthentication hold: a caller whose store is a
// table hands in what a table keeps.
func TestMintAnswersWhatAStoreKeeps(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	whole := func(name string, v time.Time) {
		x.False(v.IsZero(), name)
		x.Zero(v.Nanosecond()%1000, "%s has digits below the microsecond: %s", name, v.Format(time.RFC3339Nano))
	}

	for name, store := range map[string]authsession.Store{
		"a map":   authsession.NewMemStore(),
		"a table": tableStore{authsession.NewMemStore()},
	} {
		s := authsession.New(store,
			authsession.Insecure(),
			authsession.WithIdle(10*time.Minute),
			authsession.WithLifetime(time.Hour))

		v, _, err := s.Mint(ctx, authsession.Session{Id: actor})
		x.NoError(err, name)
		whole(name+": expires", v.Expires)
		whole(name+": idle", v.Idle)

		got, err := s.Read(ctx, v.Key)
		x.NoError(err, name)
		x.True(v.Expires.Equal(got.Expires), "%s: Mint answered %s and Read gave %s",
			name, v.Expires.Format(time.RFC3339Nano), got.Expires.Format(time.RFC3339Nano))
		x.True(v.Idle.Equal(got.Idle), "%s: the idle clock", name)
	}
}

// TestTheIdleClockAReadMovesIsWhatAStoreKeeps: the idle deadline moves when a
// session more than halfway to going stale is used, and what it moves to is
// computed from the clock too.
func TestTheIdleClockAReadMovesIsWhatAStoreKeeps(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	store := tableStore{authsession.NewMemStore()}
	s := authsession.New(store, authsession.Insecure(), authsession.WithIdle(time.Hour), authsession.WithLifetime(24*time.Hour))

	// Almost stale, so the next use moves the deadline.
	v, _, err := s.Mint(ctx, authsession.Session{Id: actor, Idle: time.Now().Add(time.Minute).Truncate(time.Microsecond)})
	x.NoError(err)

	used, err := s.Read(ctx, v.Key)
	x.NoError(err)
	x.True(used.Idle.After(v.Idle), "the use did not move the idle clock, so this proves nothing")
	x.Zero(used.Idle.Nanosecond()%1000, "the idle clock has digits below the microsecond: %s", used.Idle.Format(time.RFC3339Nano))

	got, err := s.Inspect(ctx, v.Key)
	x.NoError(err)
	x.True(used.Idle.Equal(got.Idle), "a use answered %s and the store holds %s", used.Idle.Format(time.RFC3339Nano), got.Idle.Format(time.RFC3339Nano))
}
