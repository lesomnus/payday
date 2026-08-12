// Package authsession is the browser's half of authentication.
//
// # What this is for
//
// A browser cannot hold a credential the way a service can. There is no
// keychain to read a certificate from and nowhere safe to keep a token: script
// that can read one is script that can send it somewhere else, and an XSS
// becomes a permanent theft rather than a bad afternoon. What a browser has is
// a cookie the script cannot read, which is a **handle** to state the server
// keeps.
//
// So this is two halves of one thing, and they are one type for a reason worth
// stating: [Sessions.Serve] mints the cookie and [Sessions.Handler] reads it,
// and configuring them apart is how the cookie name comes to differ between the
// end that writes it and the end that looks for it. That failure is silent --
// every sign-in succeeds and every subsequent call is anonymous -- so there is
// no arrangement here that allows it.
//
// # It is not [auth.Issuer], which payday deleted
//
// The distinction is the whole reason this belongs in payday at all. What was
// removed minted **tokens that other parties verify**, which makes the thing
// holding it an identity provider, and payday is not one. A session key is
// opaque, means nothing anywhere else, and is a row in a store this server owns
// -- closer to a row number than to a claim. Revoking it is a delete.
//
// It is also why there is no signing key here, no expiry a client can read and
// no claims. Everything about a session is on the server, which is what makes
// "sign this person out everywhere, now" a thing that works.
//
// # What payday supplies and what the app does
//
// payday does not know what a login form contains. It supplies the parts every
// app gets wrong by leaving out -- an unguessable key, the cookie attributes,
// an absolute expiry, and the read path that turns a cookie back into an
// [auth.Identity] -- and takes a [Verify] for the part it cannot know.
//
//	mux.Handle("POST /session", sessions.Serve(login))
//	mux.Handle("DELETE /session", sessions.Serve(login))
//
// The mux is the app's: [web.Mux] embeds [http.ServeMux] exactly so that a route
// like this has somewhere to go.
//
// # Where the session lives
//
// [MemStore] holds them in this process, which is right for one replica and
// **silently wrong** for two -- a browser whose cookie was minted on one is
// anonymous on the other, intermittently, in proportion to how the load
// balancer feels. It is the same trap `watch`'s memory broker carries and it is
// named here for the same reason: the failure looks like a flaky login rather
// than like a missing decision.
//
// # Cross-origin
//
// A page on another origin -- which is every `npm run dev` -- sends no cookie
// unless it asks (`credentials: "include"`) and unless the server allows it.
// payday's [web.Cors] sends `Access-Control-Allow-Credentials` for an origin
// that was named, so the server half is done; the page still has to ask.
package authsession

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
)

// Method is what this calls itself in [auth.Identity.Method].
const Method = "session"

// DefaultCookie is the cookie a session is carried in when nothing said.
//
// The prefix is not decoration: a browser refuses to store a `__Host-` cookie
// unless it is Secure, has no Domain and is pathed at `/`, and it refuses one
// set over plain HTTP. So a deployment that gets the attributes wrong finds out
// at once rather than serving sessions that a subdomain can also write.
//
// It is dropped by [Insecure], which has to serve over HTTP and could not use
// this name if it wanted to.
const (
	DefaultCookie  = "__Host-pd_session"
	InsecureCookie = "pd_session"
)

// Two clocks, which is what every session store that people actually use has.
//
//	DefaultIdle       how long an unused session survives
//	DefaultLifetime   how long any session survives, used or not
//
// # Why both, and why one is not enough
//
// This carried only the second for a while, on the argument that a sliding
// expiry means a key somebody stole works for as long as they keep using it.
// That is true of a sliding expiry **alone**, and it is not what anybody
// ships: an absolute cap is what closes it, and the pair is the standard
// arrangement -- an idle timeout somebody notices as "you were away too long",
// under a maximum they notice as "sign in again, it has been a day".
//
// The cost of leaving the first one out was worse than the risk it avoided. An
// absolute-only session has to be long enough to be usable, so it is long
// enough that ending it early becomes a separate problem to solve -- which is
// how this app grew a streaming channel to end sessions faster, and how that
// channel came to be the only thing standing between somebody leaving and
// their access ending.
//
// Twelve hours is a working day and thirty minutes is a coffee.
const (
	DefaultIdle     = 30 * time.Minute
	DefaultLifetime = 12 * time.Hour
)

// ErrNoSession is a key that names nothing here.
//
// Expired, signed out, or never real: one error for all three, because the
// difference is not the caller's business and telling them apart says whether a
// key was ever good.
var ErrNoSession = errors.New("authsession: no such session")

// Session is one signed-in browser.
//
// It carries what a credential carries and no more -- see [auth.Identity],
// which is what it becomes. Nothing about the person is here: the row is read
// by a [auth.Resolver] on every request, so a session cannot be a stale copy of
// somebody.
type Session struct {
	// Key is the value in the cookie, and the only part the browser ever sees.
	// [Sessions.Serve] fills it; a [Verify] leaves it alone.
	Key string

	// Id is the actor, written the way [pdid.Id.String] writes one, and
	// TenantId is who holds them. They are what a [auth.Resolver] is given.
	Id       string
	TenantId string

	// Grant is what this session allows, which is at most what the actor does.
	// The zero value is nothing; a [Verify] that means "everything this person
	// may do" answers [frame.Whole].
	Grant frame.Grant

	// Expires is when it stops working whatever happens, counted from the
	// sign-in. [Sessions.Serve] fills it from the lifetime unless a [Verify]
	// set one, which is how an app gives a short session to somebody who has
	// not finished a second factor.
	Expires time.Time

	// Idle is when it stops working if it is not used, counted from the last
	// time it was. [Sessions.Handler] moves it forward as the session is used
	// and never past [Session.Expires].
	//
	// The zero value is a session with no idle timeout, which is what a store
	// written before this had and what an app that only wants the cap gets by
	// saying `WithIdle(0)`.
	Idle time.Time
}

// Dead reports whether this session has stopped working at `at`, by either
// clock.
func (v Session) Dead(at time.Time) bool {
	if !v.Expires.IsZero() && !at.Before(v.Expires) {
		return true
	}

	return !v.Idle.IsZero() && !at.Before(v.Idle)
}

// Until is when this session stops working, whichever clock runs out first.
//
// It is what an [auth.Identity] carries, so that a **stream** ends when the
// session does rather than when somebody hangs up. The idle clock is the one
// that usually wins, and a stream being open is not use -- a Watch left running
// on a forgotten tab is exactly what an idle timeout is for.
func (v Session) Until() time.Time {
	switch {
	case v.Idle.IsZero():
		return v.Expires
	case v.Expires.IsZero():
		return v.Idle
	case v.Idle.Before(v.Expires):
		return v.Idle
	default:
		return v.Expires
	}
}

// Store is where sessions are kept.
//
// The methods take a context because a real one is a table or a cache and both
// can be slow. Get answers [ErrNoSession] for a key it does not hold, and a
// store may forget a session at any time -- expiry is checked here rather than
// trusted to it.
type Store interface {
	Put(ctx context.Context, v Session) error
	Get(ctx context.Context, key string) (Session, error)
	Del(ctx context.Context, key string) error
}

// Verify answers who somebody is, from whatever the request carries.
//
// **This is the seam.** payday has no idea what a login form contains -- a JSON
// body, a form post, a username and a password, an authenticator response --
// and it has no way to check any of them: the people are in the app's schema
// and the secrets are wherever that app keeps them. In a deployment with roster
// behind it this is one call to `VouchService.Verify` and nothing else.
//
// What comes back needs [Session.Id] and [Session.TenantId] and may set
// [Session.Grant] and [Session.Expires]. [Session.Key] is filled here; a value
// set there is overwritten, because a key a [Verify] chose is a key something
// other than `crypto/rand` chose.
//
// Refusing is an error. It becomes 401 unless it is one [http.Error] would say
// otherwise about; see [Sessions.Serve].
type Verify func(ctx context.Context, r *http.Request) (Session, error)

// Sessions is the two halves: the endpoint that mints a cookie and the handler
// that reads one.
type Sessions struct {
	store Store

	cookie   string
	path     string
	idle     time.Duration
	lifetime time.Duration
	sameSite http.SameSite
	secure   bool
}

type Option func(*Sessions)

// WithCookie names the cookie. Both halves use it, which is the point of them
// being one value.
func WithCookie(name string) Option { return func(s *Sessions) { s.cookie = name } }

// WithLifetime is how long a session lasts however much it is used.
func WithLifetime(d time.Duration) Option { return func(s *Sessions) { s.lifetime = d } }

// WithIdle is how long an unused session survives, and zero is no idle timeout
// at all -- a session that lives its whole [WithLifetime] whether anybody
// touches it or not.
//
// It is moved forward as the session is used, and never past the absolute one.
// That is what makes a short idle window usable: somebody working does not sign
// in again every half hour, and somebody who walked away is gone in one.
func WithIdle(d time.Duration) Option { return func(s *Sessions) { s.idle = d } }

// WithPath is what the cookie is pathed at, and `/` is the default.
//
// Narrowing it is not a security measure worth much -- a cookie is scoped by
// origin and not by path, and a page on the same origin can reach any path --
// and it breaks `__Host-`, which requires `/`. It is here for an app that
// mounts itself under a prefix.
func WithPath(p string) Option { return func(s *Sessions) { s.path = p } }

// WithSameSite overrides the cross-site rule, which is `Lax` by default.
//
// `Lax` is what makes this resistant to CSRF without a token: every RPC payday
// serves is a POST, and a cross-site POST carries no `Lax` cookie. What `Lax`
// still allows is a top-level GET navigation, which is what keeps a link in an
// email from landing the user signed out.
//
// `Strict` gives that up and buys little here, since there is no GET to
// protect. `None` gives up the CSRF defence entirely and needs a token instead;
// it is for an app deliberately embedded cross-site.
func WithSameSite(v http.SameSite) Option { return func(s *Sessions) { s.sameSite = v } }

// Insecure serves the cookie without `Secure`, for a checkout over plain HTTP.
//
// It says so in the log, once per process, for the reason [auth.Plain] does:
// the way this goes wrong is silence. A session cookie without `Secure` is one
// that a browser will send over plain HTTP, which is one that anybody between
// here and there can read and then be.
//
// It also drops the `__Host-` prefix, since a browser will not store one over
// HTTP -- so a deployment that turns this on and forgets to turn it off has a
// cookie by a different name, which is at least visible.
func Insecure() Option {
	return func(s *Sessions) {
		insecureSaid.Do(func() {
			slog.Warn("authsession: serving session cookies without Secure; " +
				"anybody on the network can read one and be whoever it names")
		})

		s.secure = false
		if s.cookie == DefaultCookie {
			s.cookie = InsecureCookie
		}
	}
}

var insecureSaid sync.Once

// New answers the pair.
func New(store Store, opts ...Option) *Sessions {
	s := &Sessions{
		store:    store,
		cookie:   DefaultCookie,
		path:     "/",
		idle:     DefaultIdle,
		lifetime: DefaultLifetime,
		sameSite: http.SameSiteLaxMode,
		secure:   true,
	}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Handler reads the cookie off a call and answers who it names.
//
// It is an [auth.Handler] like [auth.Plain] and [auth.MTLS], and goes in the
// same chain -- usually beside one of them, since a deployment with a browser
// in front of it generally has services behind it too:
//
//	auth.Seq(authsession.New(store).Handler(), auth.MTLS())
//
// The cookie arrives as ordinary gRPC metadata. A browser cannot speak gRPC, so
// its call came through `web`'s transcoder, and headers travel across that as
// metadata -- `cookie` among them.
func (s *Sessions) Handler() auth.Handler {
	return auth.HandlerFunc(func(ctx context.Context) (auth.Identity, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return auth.Identity{}, auth.ErrNoCredential
		}

		key := ""
		for _, line := range md.Get("cookie") {
			cs, err := http.ParseCookie(line)
			if err != nil {
				// A header this cannot read is not a credential. Something else
				// in the chain may still find one.
				continue
			}

			for _, c := range cs {
				if c.Name == s.cookie {
					key = c.Value
				}
			}
		}
		if key == "" {
			return auth.Identity{}, auth.ErrNoCredential
		}

		v, err := s.store.Get(ctx, key)
		if err != nil {
			if errors.Is(err, ErrNoSession) {
				// A cookie that is there and names nothing is a credential that
				// is wrong, which `auth` is explicit is not the same as none:
				// falling through would serve somebody with a dead session as
				// whatever the next handler makes of them.
				return auth.Identity{}, fmt.Errorf("%w", ErrNoSession)
			}

			// The store is the thing that could not be reached, and that is not
			// the caller's fault. Told unauthenticated, a browser throws away a
			// perfectly good cookie and sends the user to sign in again against
			// the store that is already down.
			return auth.Identity{}, fmt.Errorf("%w: %w", auth.ErrUnavailable, err)
		}

		// Both clocks are checked here rather than trusted to the store, because
		// a store is a cache as often as it is a table and "it will have
		// expired it" is not a thing this can know.
		now := time.Now()
		if v.Dead(now) {
			return auth.Identity{}, fmt.Errorf("%w", ErrNoSession)
		}

		v = s.used(ctx, v, now)

		return auth.Identity{
			Method:   Method,
			Id:       v.Id,
			TenantId: v.TenantId,
			Grant:    v.Grant,

			// Carried so that a **stream** ends when the session does, by
			// whichever clock runs out first. A call presents its cookie every
			// time and finds out at the next one; a Watch reads it once at the
			// handshake and would otherwise outlive the session by however long
			// somebody leaves the tab open -- which is what the idle clock is
			// for, and a stream being open is not use.
			Expires: v.Until(),
		}, nil
	})
}

// used moves the idle deadline forward, rarely.
//
// **Not on every request.** An idle timeout written every time somebody clicks
// is a write on the busiest path an app has -- a row version, an audit entry, a
// cache round trip -- for a value that is about to be written again. So it moves
// only once the session is more than halfway to going stale, which means at
// most one write per half-window per session and an idle deadline that is
// accurate to that half.
//
// A store that refuses is ignored. The session is valid, the request should be
// served, and what is lost is the deadline moving -- so the worst a failing
// store does is end a session at the idle timeout of somebody who was using it,
// which is the safe direction.
//
// It never moves past the absolute clock. That is what the pair is for: the
// idle one is a convenience and the other one is the limit.
func (s *Sessions) used(ctx context.Context, v Session, now time.Time) Session {
	if s.idle <= 0 || v.Idle.IsZero() {
		return v
	}
	if now.Before(v.Idle.Add(-s.idle / 2)) {
		return v
	}

	u := v
	u.Idle = now.Add(s.idle)
	if !u.Expires.IsZero() && u.Idle.After(u.Expires) {
		u.Idle = u.Expires
	}

	if err := s.store.Put(ctx, u); err != nil {
		return v
	}

	return u
}

// Serve is the endpoint a sign-in form posts to.
//
//	POST    mints a session and sets the cookie
//	DELETE  ends it and clears the cookie
//
// Both on one handler because they are one resource, and a mux routes by method:
//
//	mux.Handle("POST /session", sessions.Serve(login))
//	mux.Handle("DELETE /session", sessions.Serve(login))
//
// It answers 204 and no body. What a page needs to know is in the cookie, and
// what it needs *about the person* is a request it should make -- an answer
// composed here would be a second place that has to change when the app's idea
// of a person does.
func (s *Sessions) Serve(verify Verify) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.mint(w, r, verify)
		case http.MethodDelete:
			s.end(w, r)
		default:
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// ErrNobody is a session that names no actor.
//
// Minting one would make a cookie that resolves to nothing on every later call,
// and the sign-in would look like it worked -- which is the failure somebody
// spends an afternoon on, because every part of it succeeded.
var ErrNobody = errors.New("authsession: this session names nobody")

// Mint puts a session in the store and answers with the cookie that names it.
//
// # Why this is not only [Sessions.Serve]
//
// Because a sign-in is not necessarily an HTTP handler. It looked like one
// while payday served exactly one shape of it, and a deployment that defines
// its own `AuthService` -- so that every language gets the same sign-in from
// generated code, rather than one language reading a document -- needs these
// three lines and none of the request handling around them.
//
// It works over a transcoder, which is worth saying because it sounds as though
// it should not: a gRPC handler sets `set-cookie` as response metadata and
// `web.Transcode` hands it to the browser as a header like any other. Confirmed
// by running it.
//
// The caller owns the answer. `Serve` maps a refusal to 401 and a store that
// would not take it to 503; an RPC maps them to whatever its own contract says.
//
// `v` is what a [Verify] answered with, and this fills the rest: the key, and
// whichever of the two clocks it left unset.
func (s *Sessions) Mint(ctx context.Context, v Session) (Session, *http.Cookie, error) {
	if v.Id == "" {
		return Session{}, nil, ErrNobody
	}

	k, err := key()
	if err != nil {
		return Session{}, nil, fmt.Errorf("authsession: %w", err)
	}

	v.Key = k

	now := time.Now()
	if v.Expires.IsZero() {
		v.Expires = now.Add(s.lifetime)
	}
	if v.Idle.IsZero() && s.idle > 0 {
		v.Idle = now.Add(s.idle)
		if v.Idle.After(v.Expires) {
			v.Idle = v.Expires
		}
	}

	if err := s.store.Put(ctx, v); err != nil {
		return Session{}, nil, fmt.Errorf("authsession: %w", err)
	}

	return v, &http.Cookie{
		Name:  s.cookie,
		Value: v.Key,
		Path:  s.path,

		// `Expires` and not `MaxAge` alone, and both: the cookie should go when
		// the session does, so that a browser stops sending one that cannot
		// work. It is a courtesy and not the check -- the check is the store.
		Expires: v.Expires,

		// The three that matter, and none is optional. Without HttpOnly a
		// script reads the session and the whole argument at the top of this
		// file is undone.
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: s.sameSite,
	}, nil
}

// End deletes a session and answers with the cookie that clears it.
//
// A store that refuses is not reported, and that is the whole of the error
// handling: the caller asked to be signed out and the cookie clears either way.
// Answering with a failure would leave somebody looking at a page that says
// they are still signed in, and what the store kept expires on its own.
//
// An empty key is a caller with no cookie, which is somebody signing out twice.
// It is not an error; the cookie comes back regardless, so the browser is left
// in the state that was asked for.
func (s *Sessions) End(ctx context.Context, key string) *http.Cookie {
	if key != "" {
		_ = s.store.Del(ctx, key)
	}

	// Cleared with the same attributes it was set with. A browser matches a
	// cookie to overwrite by name, path and domain, so one cleared at a
	// different path leaves the original exactly where it was.
	return &http.Cookie{
		Name:     s.cookie,
		Value:    "",
		Path:     s.path,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: s.sameSite,
	}
}

func (s *Sessions) mint(w http.ResponseWriter, r *http.Request, verify Verify) {
	ctx := r.Context()

	v, err := verify(ctx, r)
	if err != nil {
		// One answer for every way a sign-in can fail, and no detail. Which
		// half was wrong is exactly what somebody working through a list of
		// addresses is asking.
		http.Error(w, "unauthorized", http.StatusUnauthorized)

		return
	}

	_, c, err := s.Mint(ctx, v)
	if err != nil {
		if errors.Is(err, ErrNobody) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)

			return
		}

		http.Error(w, "cannot sign in just now", http.StatusServiceUnavailable)

		return
	}

	http.SetCookie(w, c)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Sessions) end(w http.ResponseWriter, r *http.Request) {
	var was string
	if c, err := r.Cookie(s.cookie); err == nil {
		was = c.Value
	}

	http.SetCookie(w, s.End(r.Context(), was))
	w.WriteHeader(http.StatusNoContent)
}

// key is the value that goes in the cookie.
//
// 32 bytes from `crypto/rand`, which is the whole of the security of this
// scheme: the key is a bearer credential, so anything guessable about it is a
// way to be somebody. It is not derived from who they are, when they signed in
// or anything else -- a key that carries information is one that leaks it.
func key() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
