package authsession_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
)

// who is a sign-in that always works, for the tests that are about everything
// else.
func who(id, tenant string) authsession.Verify {
	return func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		return authsession.Session{Id: id, TenantId: tenant, Grant: frame.Whole()}, nil
	}
}

// signIn posts to the endpoint and answers with the cookie it set.
func signIn(t *testing.T, s *authsession.Sessions, v authsession.Verify) *http.Cookie {
	t.Helper()

	w := httptest.NewRecorder()
	s.Serve(v).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/session", nil))

	require.Equal(t, http.StatusNoContent, w.Code)

	cs := w.Result().Cookies()
	require.Len(t, cs, 1)

	return cs[0]
}

// carrying is a call that arrived with these cookies, the way one does: as
// metadata, because a browser cannot speak gRPC and `web` translated it.
func carrying(cs ...*http.Cookie) context.Context {
	line := ""
	for i, c := range cs {
		if i > 0 {
			line += "; "
		}
		line += c.Name + "=" + c.Value
	}

	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("cookie", line))
}

// TestACookieMintedHereIsReadBackHere is the whole of it, and the reason the
// two halves are one value: configured apart, the name they agree on is a thing
// that can differ, and the failure is every sign-in succeeding and every call
// after it being anonymous.
func TestACookieMintedHereIsReadBackHere(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())
	c := signIn(t, s, who("019-abc", "019-acme"))

	id, err := s.Handler().Handle(carrying(c))
	x.NoError(err)
	x.Equal("019-abc", id.Id)
	x.Equal("019-acme", id.TenantId)
	x.Equal(authsession.Method, id.Method)
}

// TestTheCookieCarriesNothingButAHandle.
//
// Not who they are, not when they signed in, not what they may do. Everything
// is a row in the store, which is what makes signing somebody out a delete
// rather than a wait.
func TestTheCookieCarriesNothingButAHandle(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())
	c := signIn(t, s, who("019-abc", "019-acme"))

	x.NotContains(c.Value, "019-abc")
	x.NotContains(c.Value, "019-acme")
	x.GreaterOrEqual(len(c.Value), 40, "a 32-byte key does not encode this short")
}

// TestNoTwoSessionsShareAKey, which is the whole security of the scheme: the
// key is a bearer credential, so anything guessable about it is a way to be
// somebody.
func TestNoTwoSessionsShareAKey(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())

	seen := map[string]bool{}
	for range 50 {
		c := signIn(t, s, who("019-abc", "019-acme"))
		x.False(seen[c.Value], "a key came up twice")
		seen[c.Value] = true
	}
}

// TestAVerifyCannotChooseTheKey. One that could is one that could choose a
// predictable one, or the same one for everybody.
func TestAVerifyCannotChooseTheKey(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())
	c := signIn(t, s, func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		return authsession.Session{Key: "hunter2", Id: "019-abc", TenantId: "019-acme"}, nil
	})

	x.NotEqual("hunter2", c.Value)
}

// TestNoCookieIsNoCredentialAndNotARefusal, so that another handler in the
// chain still gets asked.
func TestNoCookieIsNoCredentialAndNotARefusal(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())

	// Nothing at all.
	_, err := s.Handler().Handle(context.Background())
	x.ErrorIs(err, auth.ErrNoCredential)

	// Cookies, but not this one.
	_, err = s.Handler().Handle(carrying(&http.Cookie{Name: "theme", Value: "dark"}))
	x.ErrorIs(err, auth.ErrNoCredential)
}

// TestACookieThatNamesNothingDoesNotFallThrough is the distinction `auth`
// insists on: a credential that is there and wrong is not the same as none.
//
// Falling through would serve somebody whose session was revoked as whatever
// the next handler makes of them -- which, in a chain that ends in `Plain`, is
// whoever they say.
func TestACookieThatNamesNothingDoesNotFallThrough(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())

	dead := &http.Cookie{Name: authsession.DefaultCookie, Value: "not-a-key"}

	_, err := s.Handler().Handle(carrying(dead))
	x.ErrorIs(err, authsession.ErrNoSession)
	x.NotErrorIs(err, auth.ErrNoCredential)

	// And in a chain, it stops the search rather than becoming somebody else.
	h := auth.Seq(s.Handler(), auth.HandlerFunc(
		func(ctx context.Context) (auth.Identity, error) {
			return auth.Identity{Id: "019-somebody-else"}, nil
		}))

	_, err = h.Handle(carrying(dead))
	x.Error(err, "a dead session became the next handler's caller")
}

// TestAnExpiredSessionIsRefusedEvenWhileTheStoreHoldsIt.
//
// The check is here rather than trusted to the store, because a store is a
// cache as often as it is a table, and "it will have expired it by now" is not
// something this can know.
func TestAnExpiredSessionIsRefusedEvenWhileTheStoreHoldsIt(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store)

	c := signIn(t, s, func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		return authsession.Session{
			Id:       "019-abc",
			TenantId: "019-acme",
			Expires:  time.Now().Add(-time.Second),
		}, nil
	})

	// It is still there.
	v, err := store.Get(t.Context(), c.Value)
	x.NoError(err)
	x.Equal("019-abc", v.Id)

	// And it is refused anyway.
	_, err = s.Handler().Handle(carrying(c))
	x.ErrorIs(err, authsession.ErrNoSession)
}

// TestAVerifyMaySetAShorterExpiry, which is how a session given to somebody
// halfway through a second factor is one that does not last the day.
func TestAVerifyMaySetAShorterExpiry(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store)

	c := signIn(t, s, func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		return authsession.Session{
			Id:      "019-abc",
			Expires: time.Now().Add(time.Minute),
		}, nil
	})

	v, err := store.Get(t.Context(), c.Value)
	x.NoError(err)
	x.WithinDuration(time.Now().Add(time.Minute), v.Expires, 5*time.Second)
}

// TestTheExpiryIsCarriedSoAStreamEnds.
//
// A call presents its cookie every time and finds out at the next one. A Watch
// reads it once at the handshake and would otherwise be served for as long as
// somebody leaves the tab open. See [auth.Identity.Expires].
func TestTheExpiryIsCarriedSoAStreamEnds(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore(), authsession.WithLifetime(time.Hour))
	c := signIn(t, s, who("019-abc", "019-acme"))

	id, err := s.Handler().Handle(carrying(c))
	x.NoError(err)
	x.False(id.Expires.IsZero(), "nothing would ever cut the stream")
	x.False(id.Valid(time.Now().Add(2 * time.Hour)))
	x.True(id.Valid(time.Now()))
}

// TestAStoreThatCannotBeReachedIsNotTheCallersFault.
//
// Told unauthenticated, a browser throws away a cookie that is perfectly good
// and sends somebody to sign in again -- against the store that is down.
func TestAStoreThatCannotBeReachedIsNotTheCallersFault(t *testing.T) {
	x := require.New(t)

	s := authsession.New(brokenStore{})

	_, err := s.Handler().Handle(carrying(
		&http.Cookie{Name: authsession.DefaultCookie, Value: "whatever"}))

	x.ErrorIs(err, auth.ErrUnavailable)
	x.NotErrorIs(err, auth.ErrNoCredential)
}

type brokenStore struct{}

func (brokenStore) Put(context.Context, authsession.Session) error { return errors.New("down") }
func (brokenStore) Del(context.Context, string) error              { return errors.New("down") }
func (brokenStore) Get(context.Context, string) (authsession.Session, error) {
	return authsession.Session{}, errors.New("down")
}

// TestAStoreThatWouldNotTakeTheSessionIsNotARefusal.
//
// The credentials were right; the store is down. Told unauthorized, somebody
// retypes a password that was fine, and a body that names the store's error
// tells a stranger what is behind the endpoint.
func TestAStoreThatWouldNotTakeTheSessionIsNotARefusal(t *testing.T) {
	x := require.New(t)

	s := authsession.New(brokenStore{})

	w := httptest.NewRecorder()
	s.Serve(who("019-abc", "019-acme")).
		ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/session", nil))

	x.Equal(http.StatusServiceUnavailable, w.Code)
	x.Empty(w.Result().Cookies())
	x.NotContains(w.Body.String(), "down")
}

// TestTheCookieAttributesAreTheOnesThatMatter.
//
// HttpOnly above all: without it a script reads the session, and the reason for
// a server-side session rather than a token in local storage is gone.
func TestTheCookieAttributesAreTheOnesThatMatter(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())
	c := signIn(t, s, who("019-abc", "019-acme"))

	x.True(c.HttpOnly, "a script can read the session")
	x.True(c.Secure, "the session travels over plain HTTP")
	x.Equal(http.SameSiteLaxMode, c.SameSite, "a cross-site POST carries the session")
	x.Equal("/", c.Path)
	x.Equal(authsession.DefaultCookie, c.Name)
}

// TestInsecureSaysSoByChangingTheName.
//
// A browser will not store a `__Host-` cookie over plain HTTP, so the prefix
// has to go -- which means a deployment that left this on has a cookie by a
// different name, and that is at least something a person can see.
func TestInsecureSaysSoByChangingTheName(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore(), authsession.Insecure())
	c := signIn(t, s, who("019-abc", "019-acme"))

	x.False(c.Secure)
	x.Equal(authsession.InsecureCookie, c.Name)
	x.True(c.HttpOnly, "insecure gave up more than it said")
}

// TestSigningOutDeletesTheSessionAndClearsTheCookie.
func TestSigningOutDeletesTheSessionAndClearsTheCookie(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store)
	c := signIn(t, s, who("019-abc", "019-acme"))

	x.Equal(1, store.Len())

	r := httptest.NewRequest(http.MethodDelete, "/session", nil)
	r.AddCookie(c)

	w := httptest.NewRecorder()
	s.Serve(nil).ServeHTTP(w, r)
	x.Equal(http.StatusNoContent, w.Code)

	// Gone from the store, so the key is dead everywhere and not only in this
	// browser.
	x.Equal(0, store.Len())

	// And cleared, with the attributes it was set with -- a browser matches by
	// name and path, so one cleared elsewhere leaves the original in place.
	cleared := w.Result().Cookies()
	x.Len(cleared, 1)
	x.Equal(authsession.DefaultCookie, cleared[0].Name)
	x.Empty(cleared[0].Value)
	x.Equal("/", cleared[0].Path)
	x.Less(cleared[0].MaxAge, 0)

	// The cookie the browser still has, if it kept it, names nothing.
	_, err := s.Handler().Handle(carrying(c))
	x.ErrorIs(err, authsession.ErrNoSession)
}

// TestSigningOutWorksWhenTheStoreDoesNot.
//
// The caller asked to be signed out. Answering 503 leaves somebody looking at a
// page that says they are still signed in, and the cookie is cleared either
// way.
func TestSigningOutWorksWhenTheStoreDoesNot(t *testing.T) {
	x := require.New(t)

	s := authsession.New(brokenStore{})

	r := httptest.NewRequest(http.MethodDelete, "/session", nil)
	r.AddCookie(&http.Cookie{Name: authsession.DefaultCookie, Value: "whatever"})

	w := httptest.NewRecorder()
	s.Serve(nil).ServeHTTP(w, r)

	x.Equal(http.StatusNoContent, w.Code)
	x.Len(w.Result().Cookies(), 1)
}

// TestARefusedSignInMintsNothing, and says nothing about which half was wrong.
func TestARefusedSignInMintsNothing(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store)

	w := httptest.NewRecorder()
	s.Serve(func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		return authsession.Session{}, errors.New("no such person")
	}).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/session", nil))

	x.Equal(http.StatusUnauthorized, w.Code)
	x.Empty(w.Result().Cookies())
	x.Equal(0, store.Len())
	x.NotContains(w.Body.String(), "no such person")
}

// TestAVerifyThatNamesNobodyIsRefused.
//
// Minting here would make a cookie that resolves to nothing on every later
// call, and the sign-in would look like it worked.
func TestAVerifyThatNamesNobodyIsRefused(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store)

	w := httptest.NewRecorder()
	s.Serve(func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		return authsession.Session{TenantId: "019-acme"}, nil
	}).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/session", nil))

	x.Equal(http.StatusUnauthorized, w.Code)
	x.Equal(0, store.Len())
}

// TestTheGrantSurvivesTheRoundTrip, which is what lets a session mean "this
// person, for reading" rather than everything they may do.
func TestTheGrantSurvivesTheRoundTrip(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())
	c := signIn(t, s, who("019-abc", "019-acme"))

	id, err := s.Handler().Handle(carrying(c))
	x.NoError(err)
	x.Equal(frame.Whole(), id.Grant)
}

// TestOnlyPostAndDelete.
func TestOnlyPostAndDelete(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())

	w := httptest.NewRecorder()
	s.Serve(who("019-abc", "019-acme")).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/session", nil))

	x.Equal(http.StatusMethodNotAllowed, w.Code)
	x.Equal("POST, DELETE", w.Header().Get("Allow"))
}

// TestANamedCookieIsUsedByBothHalves.
func TestANamedCookieIsUsedByBothHalves(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore(), authsession.WithCookie("sid"))
	c := signIn(t, s, who("019-abc", "019-acme"))

	x.Equal("sid", c.Name)

	id, err := s.Handler().Handle(carrying(c))
	x.NoError(err)
	x.Equal("019-abc", id.Id)
}

// TestTheStoreForgetsWhatHasExpired, so that a process signing people in for a
// year is not a process holding a year of sessions.
func TestTheStoreForgetsWhatHasExpired(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store, authsession.WithLifetime(-time.Second))

	for range 5 {
		signIn(t, s, who("019-abc", "019-acme"))
	}

	// Each sign-in drops what has expired before writing its own, so what is
	// left is the last one.
	x.Equal(1, store.Len())
}

// TestEndingSomebodysSessionsEndsThemNow, which is what a store has to be able
// to do for a departure to mean anything.
//
// A session is keyed by its own value, because that is what a request carries.
// "Whose are these" is a different question, asked once when somebody leaves
// rather than once per call, and a store that cannot answer it has sessions
// outliving the person by however long they had left.
func TestEndingSomebodysSessionsEndsThemNow(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store)

	// Two browsers of theirs, and somebody else's.
	a := signIn(t, s, who("019-abc", "019-acme"))
	bb := signIn(t, s, who("019-abc", "019-acme"))
	other := signIn(t, s, who("019-xyz", "019-acme"))
	x.Equal(3, store.Len())

	x.NoError(store.DelBy(t.Context(), func(v authsession.Session) bool {
		return v.Id == "019-abc"
	}))

	for _, c := range []*http.Cookie{a, bb} {
		_, err := s.Handler().Handle(carrying(c))
		x.ErrorIs(err, authsession.ErrNoSession, "a session of theirs survived")
	}

	// And nobody else's.
	id, err := s.Handler().Handle(carrying(other))
	x.NoError(err)
	x.Equal("019-xyz", id.Id)
}

// Two clocks: one for being away, one for the day.
//
// This carried only the absolute one for a while, on the argument that a
// sliding expiry means a stolen key works for as long as somebody keeps using
// it. That is true of a sliding expiry **alone**; the pair is what everybody
// ships, and leaving the first out had a cost -- an absolute-only session has
// to be long enough to be usable, which makes ending one early a separate
// problem, which is how this grew a streaming channel to end sessions and how
// that channel became the only thing between somebody leaving and their access
// ending.

// TestAnIdleSessionEndsWithoutEndingAWorkingOne is the whole of what the second
// clock buys.
func TestAnIdleSessionEndsWithoutEndingAWorkingOne(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore(),
		authsession.WithIdle(100*time.Millisecond),
		authsession.WithLifetime(time.Hour))

	c := signIn(t, s, who("019-abc", "019-acme"))

	// Used, so it keeps going. Past the idle window several times over.
	for range 5 {
		time.Sleep(60 * time.Millisecond)

		_, err := s.Handler().Handle(carrying(c))
		x.NoError(err, "a session in use went stale")
	}

	// Left alone, so it stops.
	time.Sleep(150 * time.Millisecond)

	_, err := s.Handler().Handle(carrying(c))
	x.ErrorIs(err, authsession.ErrNoSession)
}

// TestTheIdleClockNeverPassesTheOtherOne, which is what makes the pair safe:
// the first is a convenience and the second is the limit.
func TestTheIdleClockNeverPassesTheOtherOne(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store,
		authsession.WithIdle(time.Hour),
		authsession.WithLifetime(50*time.Millisecond))

	c := signIn(t, s, who("019-abc", "019-acme"))

	v, err := store.Get(t.Context(), c.Value)
	x.NoError(err)
	x.Equal(v.Expires, v.Idle, "a one-hour idle window outlived a fifty-millisecond session")

	time.Sleep(60 * time.Millisecond)

	// And using it does not save it.
	_, err = s.Handler().Handle(carrying(c))
	x.ErrorIs(err, authsession.ErrNoSession)
}

// TestUsingASessionDoesNotWriteEveryTime.
//
// An idle deadline written on every request is a write on the busiest path an
// app has, for a value about to be written again. It moves once the session is
// more than halfway to stale, so a burst of requests costs one write.
func TestUsingASessionDoesNotWriteEveryTime(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store, authsession.WithIdle(time.Hour))

	c := signIn(t, s, who("019-abc", "019-acme"))

	was, err := store.Get(t.Context(), c.Value)
	x.NoError(err)

	for range 20 {
		_, err := s.Handler().Handle(carrying(c))
		x.NoError(err)
	}

	now, err := store.Get(t.Context(), c.Value)
	x.NoError(err)
	x.Equal(was.Idle, now.Idle, "twenty requests moved the deadline twenty times")
}

// TestNoIdleClockIsTheOldBehaviour, for a deployment that wants only the cap.
func TestNoIdleClockIsTheOldBehaviour(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store, authsession.WithIdle(0))

	c := signIn(t, s, who("019-abc", "019-acme"))

	v, err := store.Get(t.Context(), c.Value)
	x.NoError(err)
	x.True(v.Idle.IsZero())
	x.False(v.Expires.IsZero())

	id, err := s.Handler().Handle(carrying(c))
	x.NoError(err)
	x.Equal(v.Expires, id.Expires, "what a stream is cut by is not the absolute clock")
}

// TestAStreamIsCutByWhicheverRunsOutFirst, which is the idle one in a
// deployment that has both -- and a stream being open is not use, so a Watch on
// a forgotten tab ends.
func TestAStreamIsCutByWhicheverRunsOutFirst(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore(),
		authsession.WithIdle(time.Minute),
		authsession.WithLifetime(12*time.Hour))

	c := signIn(t, s, who("019-abc", "019-acme"))

	id, err := s.Handler().Handle(carrying(c))
	x.NoError(err)
	x.WithinDuration(time.Now().Add(time.Minute), id.Expires, 5*time.Second)
	x.False(id.Valid(time.Now().Add(2 * time.Minute)))
}
