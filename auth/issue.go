package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/lesomnus/payday/frame"
)

// This file is a seam and not an implementation, and it is here now for one
// reason: laying it later changes every place that uses this package.
//
// # What payday does and does not do about logging in
//
// It reads credentials and enforces them. It does not mint them, and the
// reason is not that minting is hard -- it is that every way of minting one is
// a decision about an organisation rather than about a server. A password
// policy, an OIDC provider, how long a session lasts, whether a second factor
// is asked for, what a magic link does: none of those have a right answer that
// a framework can pick, and a framework that picks one is one that has to be
// fought.
//
// What it can do is say what the *shape* of an issuer is, so that the thing an
// app writes drops into a place that already exists. [Issuer] and [Sessions]
// are that shape. `payday/gate` is the same arrangement seen from the other
// side: the seam is the framework's, the judgement is the app's.
//
// # Why the seam has to exist before the implementation
//
// payday now ships a client. Before that, "does not issue credentials" cost an
// app nothing -- whatever was already handing out tokens went on doing it. With
// a TypeScript client in the box, every app built on payday writes logging in
// from nothing, which is the one part of an application least worth writing
// from nothing.
//
// The implementation is not here because it needs an HTTP endpoint that payday
// does not yet have -- an OIDC callback is a redirect, not an RPC -- and
// because it is large. The seam is here because adding [Identity.Expires]
// afterwards would touch every handler, every store and the interceptor.

// ErrNoIssuer is what an app that has no issuer wired answers with.
//
// It is a named error rather than an Unimplemented status so that a caller of
// [Issuer] in Go can tell "this deployment does not issue credentials" from
// "this deployment refused you one", which are different things to show
// somebody.
var ErrNoIssuer = errors.New("auth: this deployment issues no credentials")

// Subject is who an issuer has satisfied itself about.
//
// What satisfied it is not here and is deliberately not a type: a password
// checked against a hash, an OIDC token verified against a JWKS, a link
// clicked. All of that happens before this, in the app, and what reaches an
// issuer is the conclusion.
type Subject struct {
	// Tenant and Alias name the actor, and Id names it directly. The same
	// three ways an [Identity] can, and for the same reason -- an issuer
	// writes an identity, so it has to be able to say what an identity says.
	Tenant string
	Alias  string
	Id     string

	// Grant is what the credential should be narrowed to, and [frame.Whole]
	// for one that is not narrowed.
	//
	// This is where an attenuated token comes from, and it is the issuer's
	// caller that decides: a session for a browser is whole, a token minted for
	// a script is two methods and one tenant. Nothing downstream can widen it.
	Grant frame.Grant

	// How is what convinced whoever built this -- "password", "oidc:acme",
	// "invite". It is for the record and for nothing else: a rule that turns on
	// the way somebody authenticated is a rule that will be wrong one day, and
	// [Identity.Method] says the same thing about the other end for the same
	// reason.
	How string
}

// Credential is what an issuer answers with: the secret, and when it stops
// working.
type Credential struct {
	// Token is the secret the caller sends back, and it is a secret -- never
	// logged, never in an error, never part of an [Identity].
	Token string

	// Expires is when it stops working, and the zero time is one that does not.
	// A credential that never expires is a thing to mean rather than a thing to
	// end up with, which is why it is the zero value of a field an issuer
	// writes rather than a default it inherits.
	Expires time.Time
}

// Issuer turns a subject into a credential.
//
// Nothing in payday implements it and nothing in payday calls it. What calls it
// is the app's own login RPC -- which is an RPC the app wrote, on a service the
// app declared, because who may ask for a credential and on what evidence is
// exactly the kind of thing this framework says an app should have to write
// down.
type Issuer interface {
	Issue(ctx context.Context, s Subject) (Credential, error)
}

// Sessions is where issued credentials live, and what [Bearer] reads.
//
// It **is** a [TokenStore] rather than having one, which is the whole of how
// this seam connects to what already exists: an app that implements this hands
// it to `auth.Bearer` and the read path is done.
//
//	auth.Bearer(sessions)
//
// What it adds is revocation, which a store does not have because reading a
// credential and taking one away are different privileges -- a server that only
// reads should not be handed the ability to invalidate.
type Sessions interface {
	TokenStore

	// Revoke takes a credential away. A token that was already gone is not an
	// error: what the caller wanted is true.
	Revoke(ctx context.Context, token string) error
}

// Expiring is a [TokenStore] answer that says when it stops being good.
//
// It is [Identity.Expires] and it is on the identity rather than beside it,
// because what needs it is a **stream**: a call carries its credential every
// time and finds out at the next one, and a stream carries it once at the
// handshake and would otherwise go on being served forever. See
// [Identity.Valid].
type Expiring interface {
	Valid(at time.Time) bool
}

var _ Expiring = Identity{}

// Valid reports whether this credential is still good at `at`.
//
// An identity with no expiry is always good, which is what a header and a
// certificate are: neither has anywhere to carry one, and a certificate's own
// expiry is the transport's business rather than this package's.
func (v Identity) Valid(at time.Time) bool {
	return v.Expires.IsZero() || at.Before(v.Expires)
}

// MemIssuer is [Issuer] and [Sessions] over a [MemTokenStore]: a reference
// rather than a thing to deploy.
//
// It is here for the reason `MemTokenStore` is -- so that the shape is
// something you can read and run rather than only something described -- and
// what it deliberately does **not** do is the part that is an organisation's
// decision. It checks nothing. Whatever satisfied itself about the subject
// happened before this, and that is where a password, a provider or a link
// lives.
//
// Sessions are in memory, so a restart logs everybody out and two replicas do
// not share them. A real one is a table, and writing it is writing
// [Sessions.Lookup] and [Sessions.Revoke] against the app's own schema.
type MemIssuer struct {
	*MemTokenStore

	// For is how long a credential lasts, and zero is [DefaultSession].
	For time.Duration

	// Token makes the secret, and nil is 32 bytes of randomness spelled as
	// hex. It is a field so that a test can be deterministic, and for no other
	// reason -- a deployment that wants a different shape of token is a
	// deployment writing its own issuer.
	Token func() (string, error)
}

// DefaultSession is how long a credential from [MemIssuer] lasts.
//
// Twelve hours, which is a working day and a bit. There is a default at all
// because the alternative -- a credential that never expires -- is the one
// answer that cannot be recovered from: a token with no expiry that gets away
// is good until somebody notices and revokes it, and nobody notices.
const DefaultSession = 12 * time.Hour

func NewMemIssuer() *MemIssuer {
	return &MemIssuer{MemTokenStore: NewMemTokenStore()}
}

var (
	_ Issuer   = (*MemIssuer)(nil)
	_ Sessions = (*MemIssuer)(nil)
)

func (s *MemIssuer) Issue(_ context.Context, sub Subject) (Credential, error) {
	token, err := s.token()
	if err != nil {
		return Credential{}, err
	}

	d := s.For
	if d <= 0 {
		d = DefaultSession
	}

	at := s.now().Add(d)
	s.Add(token, Identity{
		Method: MethodBearer,
		Tenant: sub.Tenant,
		Alias:  sub.Alias,
		Id:     sub.Id,

		// Whatever the caller asked to narrow it to, and nothing else. An
		// issuer that answered [frame.Whole] for a subject that asked for less
		// would be widening a credential at the one moment it is being made.
		Grant: sub.Grant,
	}, at)

	return Credential{Token: token, Expires: at}, nil
}

// Revoke takes a credential away, and a token that was already gone is not an
// error: what the caller wanted is true.
func (s *MemIssuer) Revoke(_ context.Context, token string) error {
	s.Remove(token)
	return nil
}

func (s *MemIssuer) token() (string, error) {
	if s.Token != nil {
		return s.Token()
	}

	// 32 bytes, which is past the point where guessing is a strategy. Since Go
	// 1.24 this cannot fail -- it fills the buffer or the process stops -- so
	// the error is here for whoever replaces it rather than for this.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}
