package authsession_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
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

// TestACookieTheServerHasForgottenIsCleared is the half of lesomnus/roster#70
// that is this package's: the refusal above tells the browser to drop the key,
// so it stops presenting one nothing holds. `auth` serves it as nobody where
// the method asks for nobody, and `grpc_test.go` has the two together over the
// wire.
func TestACookieTheServerHasForgottenIsCleared(t *testing.T) {
	x := require.New(t)

	dead := &http.Cookie{Name: authsession.DefaultCookie, Value: "not-a-key"}

	var out headers
	_, err := authsession.New(authsession.NewMemStore()).Handler().
		Handle(grpc.NewContextWithServerTransportStream(carrying(dead), &out))
	x.ErrorIs(err, authsession.ErrNoSession)

	cs := out.md.Get("set-cookie")
	x.Len(cs, 1, "the browser was left carrying a key nothing holds")
	x.Contains(cs[0], authsession.DefaultCookie+"=;")
	x.Contains(cs[0], "Max-Age=0")

	// Not when the store could not say. The session may well be there, and a
	// store that is down for a minute would sign out everybody who called in
	// it.
	t.Run("and not by a store that is down", func(t *testing.T) {
		x := require.New(t)

		var out headers
		_, err := authsession.New(brokenStore{}).Handler().
			Handle(grpc.NewContextWithServerTransportStream(carrying(dead), &out))
		x.ErrorIs(err, auth.ErrUnavailable)
		x.Empty(out.md.Get("set-cookie"), "a store that was down signed the browser out")
	})

	// And a call that did not come in over the wire has nowhere to write one,
	// which is not an error: it has no cookie to clear either.
	t.Run("and quietly where there is no stream", func(t *testing.T) {
		x := require.New(t)

		_, err := authsession.New(authsession.NewMemStore()).Handler().Handle(carrying(dead))
		x.ErrorIs(err, authsession.ErrNoSession)
	})
}

// headers is the stream a response's metadata is set on, kept for reading.
type headers struct{ md metadata.MD }

func (h *headers) Method() string                  { return "" }
func (h *headers) SetHeader(md metadata.MD) error  { h.md = metadata.Join(h.md, md); return nil }
func (h *headers) SendHeader(md metadata.MD) error { return nil }
func (h *headers) SetTrailer(md metadata.MD) error { return nil }

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
	x.True(c.Secure, "the session travels over plain Http")
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

// TestOnlyGetPostAndDelete.
func TestOnlyGetPostAndDelete(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())

	w := httptest.NewRecorder()
	s.Serve(who("019-abc", "019-acme")).
		ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/session", nil))

	x.Equal(http.StatusMethodNotAllowed, w.Code)
	x.Equal("GET, POST, DELETE", w.Header().Get("Allow"))
}

// which asks the endpoint which session a browser holds, the way a page does on
// load.
func which(t *testing.T, s *authsession.Sessions, cs ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, "/session", nil)
	for _, c := range cs {
		r.AddCookie(c)
	}

	w := httptest.NewRecorder()
	s.Serve(nil).ServeHTTP(w, r)

	return w
}

func decoded(t *testing.T, w *httptest.ResponseRecorder) authsession.Which {
	t.Helper()

	var v authsession.Which
	require.NoError(t, json.NewDecoder(w.Body).Decode(&v))

	return v
}

// TestAPageIsToldWhichSessionItHolds.
//
// A page signed in by cookie cannot read its credential and names its store by
// it all the same, so the server tells it the name -- and who the session
// names, which is what the page asks its first question with. Never the key:
// that is what HttpOnly keeps from script, and an answer carrying it would
// undo the cookie.
func TestAPageIsToldWhichSessionItHolds(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())
	c := signIn(t, s, who("019-abc", "019-acme"))

	w := which(t, s, c)
	x.Equal(http.StatusOK, w.Code)
	x.Equal("no-store", w.Header().Get("Cache-Control"), "an answer about one browser, kept by a cache for the next")
	x.NotContains(w.Body.String(), c.Value)

	v := decoded(t, w)
	x.Equal(authsession.IdentityOf(authsession.Session{Id: "019-abc", TenantId: "019-acme", Grant: frame.Whole()}), v.Identity)
	x.NotEqual(authsession.IdentityOf(authsession.Session{Key: c.Value}), v.Identity, "the name is the key's")
	x.Equal("019-abc", v.Id)
	x.Equal("019-acme", v.TenantId)
}

// TestASignInThatSeesTheSameIsTheSameStore, so that the next session of one
// person opens the mirror the last one left -- sweeping it, and sending what it
// was keeping -- rather than leaving a mirror on disk per sign-in that nothing
// ever opens again.
//
// And a session that sees less is another store, which is what naming the
// store by the credential was for: a narrowed session never draws what a wide
// one kept.
func TestASignInThatSeesTheSameIsTheSameStore(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())
	identity := func(v authsession.Session) string {
		return decoded(t, which(t, s, signIn(t, s, func(context.Context, *http.Request) (authsession.Session, error) {
			return v, nil
		}))).Identity
	}

	one, other := pdid.New(1), pdid.New(1)
	person := authsession.Session{Id: "019-abc", TenantId: "019-acme", Grant: frame.Whole()}

	was := identity(person)
	x.Equal(was, identity(person), "one person, signed in twice, is two stores")

	narrowed := person
	narrowed.Grant = frame.Whole().In(one)
	x.NotEqual(was, identity(narrowed), "a narrowed session draws what a whole one kept")

	somebody := person
	somebody.Id = "019-xyz"
	x.NotEqual(was, identity(somebody))

	elsewhere := person
	elsewhere.TenantId = "019-other"
	x.NotEqual(was, identity(elsewhere))

	// Each axis is a set: the order a grant was built in and a name said twice
	// are not what it allows.
	x.Equal(
		authsession.IdentityOf(authsession.Session{Id: "019-abc", Grant: frame.Whole().In(one, other).To("/a/B", "/a/A")}),
		authsession.IdentityOf(authsession.Session{Id: "019-abc", Grant: frame.Whole().In(other, one, other).To("/a/A", "/a/B")}))

	// And an axis narrowed to nothing is not an axis narrowed by nothing.
	x.NotEqual(
		authsession.IdentityOf(authsession.Session{Id: "019-abc", Grant: frame.Whole().In()}),
		authsession.IdentityOf(authsession.Session{Id: "019-abc", Grant: frame.Whole()}))
}

// TestABrowserHoldingNoSessionIsToldSo, and one holding a session the server
// has forgotten is told to drop the cookie as well, as every call it makes is.
func TestABrowserHoldingNoSessionIsToldSo(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store)

	w := which(t, s)
	x.Equal(http.StatusUnauthorized, w.Code)
	x.Empty(w.Result().Cookies(), "nothing to clear")

	c := signIn(t, s, who("019-abc", "019-acme"))
	x.NoError(store.Del(context.Background(), c.Value))

	w = which(t, s, c)
	x.Equal(http.StatusUnauthorized, w.Code)
	cleared := w.Result().Cookies()
	x.Len(cleared, 1)
	x.Equal(authsession.DefaultCookie, cleared[0].Name)
	x.Less(cleared[0].MaxAge, 0)
}

// TestAStoreThatCannotBeReachedCannotSay, and does not sign anybody out for it:
// the session may well still be there.
func TestAStoreThatCannotBeReachedCannotSay(t *testing.T) {
	x := require.New(t)

	s := authsession.New(brokenStore{})

	w := which(t, s, &http.Cookie{Name: authsession.DefaultCookie, Value: "whatever"})
	x.Equal(http.StatusServiceUnavailable, w.Code)
	x.Empty(w.Result().Cookies())
	x.NotContains(w.Body.String(), "down")
}

// TestIdentityOfIsAName: sixteen hex characters, the same for the same session
// on every replica and in every release. A change to what it digests renames
// every store a page keeps, which is a migration -- so this says when one
// happens.
func TestIdentityOfIsAName(t *testing.T) {
	require.Equal(t, "180f789477cf89a2",
		authsession.IdentityOf(authsession.Session{Id: "019-abc", TenantId: "019-acme", Grant: frame.Whole()}))
}

// carryingFor is a call made for the store `identity` names, carrying these
// cookies -- what a page's query layer sends.
func carryingFor(identity string, cs ...*http.Cookie) context.Context {
	md, _ := metadata.FromIncomingContext(carrying(cs...))

	return metadata.NewIncomingContext(context.Background(),
		metadata.Join(md, metadata.Pairs(authsession.IdentityHeader, identity)))
}

// TestACallForAnotherStoreIsRefused is what binds a page's writes to the
// person who made them.
//
// The cookie is the browser's, so it is whoever signed in last, in any tab. A
// page still holding the store of the one before would otherwise draw the next
// person's rows into it and send the writes it was keeping as them. It says
// which store it is for, and a session of another is refused -- as itself, so
// nothing after this handler makes somebody else of it, and without clearing a
// cookie that is alive and the browser's.
func TestACallForAnotherStoreIsRefused(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())
	c := signIn(t, s, who("019-abc", "019-acme"))
	mine := decoded(t, which(t, s, c)).Identity

	id, err := s.Handler().Handle(carryingFor(mine, c))
	x.NoError(err, "a call for the session's own store was refused")
	x.Equal("019-abc", id.Id)

	other := decoded(t, which(t, s, signIn(t, s, who("019-xyz", "019-acme")))).Identity

	var out headers
	_, err = s.Handler().Handle(grpc.NewContextWithServerTransportStream(carryingFor(other, c), &out))
	x.ErrorIs(err, authsession.ErrOtherIdentity)
	x.NotErrorIs(err, auth.ErrNoCredential)
	x.Empty(out.md.Get("set-cookie"), "a session that is alive was cleared for a page it is not the session of")

	h := auth.Seq(s.Handler(), auth.HandlerFunc(func(ctx context.Context) (auth.Identity, error) {
		return auth.Identity{Id: "019-somebody-else"}, nil
	}))
	_, err = h.Handle(carryingFor(other, c))
	x.Error(err, "a call for another store became the next handler's caller")

	// A call that names no store is any call, as it always was.
	id, err = s.Handler().Handle(carrying(c))
	x.NoError(err)
	x.Equal("019-abc", id.Id)
}

// TestTwoCookiesOfTheNameAreNone: a browser holding one this server set and
// one a sibling host set for the whole domain, or one left at another path.
// Which it sends first is the browser's to decide, and the halves of this
// package once took one each -- the page was told one session while its calls
// were served as the other. So both halves take neither.
func TestTwoCookiesOfTheNameAreNone(t *testing.T) {
	x := require.New(t)

	store := authsession.NewMemStore()
	s := authsession.New(store, authsession.WithCookie("sid"))
	a := signIn(t, s, who("019-abc", "019-acme"))
	b := signIn(t, s, who("019-xyz", "019-acme"))

	for _, cs := range [][]*http.Cookie{{a, b}, {b, a}} {
		x.Empty(s.KeyOf([]string{cs[0].Name + "=" + cs[0].Value + "; " + cs[1].Name + "=" + cs[1].Value}))
		x.Empty(s.KeyOf([]string{cs[0].Name + "=" + cs[0].Value, cs[1].Name + "=" + cs[1].Value}), "split across two header lines")

		_, err := s.Handler().Handle(carrying(cs...))
		x.ErrorIs(err, auth.ErrNoCredential)

		w := which(t, s, cs...)
		x.Equal(http.StatusUnauthorized, w.Code, "the page was told a session its calls are not served as")
		x.Empty(w.Result().Cookies())
	}

	// Signing out of two is ending neither, since neither is the one: the
	// browser's own is cleared, and the rows stay for their clocks.
	r := httptest.NewRequest(http.MethodDelete, "/session", nil)
	r.AddCookie(a)
	r.AddCookie(b)
	w := httptest.NewRecorder()
	s.Serve(nil).ServeHTTP(w, r)
	x.Equal(http.StatusNoContent, w.Code)
	x.Len(w.Result().Cookies(), 1)
	x.Equal(2, store.Len())
}

// TestACookieThisCannotReadHidesNoOther: the line a browser sends is every
// cookie it holds for the host, including ones somebody else set. One of them
// that does not parse is that one skipped, and not the line -- or a sibling
// could sign everybody out by setting one.
func TestACookieThisCannotReadHidesNoOther(t *testing.T) {
	x := require.New(t)

	s := authsession.New(authsession.NewMemStore())
	c := signIn(t, s, who("019-abc", "019-acme"))

	line := `theme=da"rk; ` + c.Name + "=" + c.Value
	x.Equal(c.Value, s.KeyOf([]string{line}))

	id, err := s.Handler().Handle(metadata.NewIncomingContext(context.Background(), metadata.Pairs("cookie", line)))
	x.NoError(err)
	x.Equal("019-abc", id.Id)

	r := httptest.NewRequest(http.MethodGet, "/session", nil)
	r.Header.Set("Cookie", line)
	w := httptest.NewRecorder()
	s.Serve(nil).ServeHTTP(w, r)
	x.Equal(http.StatusOK, w.Code)

	// And one of this name that does not parse is not a key.
	x.Empty(s.KeyOf([]string{c.Name + `="unterminated`}))
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
