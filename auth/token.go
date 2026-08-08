package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"
)

// MethodBearer is what [Bearer] calls itself.
const MethodBearer = "bearer"

// BearerScheme is the authorization scheme [Bearer] reads and [BearerProvider]
// writes.
const BearerScheme = "Bearer"

// TokenStore says who a token stands for.
//
// This is the whole of what makes an opaque token different. A header and a
// certificate carry a name; a token carries nothing, so somebody has to be
// asked, and being asked can go wrong in a way the caller did not cause.
type TokenStore interface {
	// Lookup returns who the token stands for and what it allows of what that
	// actor allows. What it fills is the name and the grant: [Bearer] writes
	// [Identity.Method] itself, because the answer to "how did this caller
	// authenticate" is not the store's to give.
	//
	// A token that narrows nothing answers [frame.Whole]; the zero Grant
	// allows nothing, so a store that forgets hands out a credential that can
	// do nothing rather than one that can do everything.
	//
	// A token that is not known, or no longer good, is an error wrapping
	// neither [ErrNoCredential] nor [ErrUnavailable]: it is a credential that
	// is present and wrong, and it must stop the search rather than fall
	// through to whatever would have been asked next. Whatever it says about
	// which of those it was is for whoever reads it here -- [Bearer] does not
	// pass it on, and answers every one of them the same way.
	//
	// A store that cannot answer -- unreachable, timed out -- wraps
	// [ErrUnavailable], and the caller is told to come back rather than told
	// their token is bad. That one is passed on, because it is not about them.
	Lookup(ctx context.Context, token string) (Identity, error)
}

type TokenStoreFunc func(ctx context.Context, token string) (Identity, error)

func (f TokenStoreFunc) Lookup(ctx context.Context, token string) (Identity, error) {
	return f(ctx, token)
}

// Bearer reads an opaque token and exchanges it for a name:
//
//	authorization: Bearer <token>
//
// The token is a secret and is never part of the answer. It is not in the
// [Identity], not in the frame of the request, and not in any message this
// package logs -- a credential that reaches a log has been given away.
func Bearer(store TokenStore) Handler {
	return HandlerFunc(func(ctx context.Context) (Identity, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return Identity{}, ErrNoCredential
		}

		for _, v := range md.Get("authorization") {
			rest, ok := strings.CutPrefix(v, BearerScheme+" ")
			if !ok {
				continue
			}

			token := strings.TrimSpace(rest)
			if token == "" {
				return Identity{}, fmt.Errorf("%s: says nothing", BearerScheme)
			}

			id, err := store.Lookup(ctx, token)
			if err != nil {
				// A store that could not answer is not the caller's fault, and
				// saying which it was is the whole reason for the third answer.
				if errors.Is(err, ErrUnavailable) {
					return Identity{}, fmt.Errorf("%s: %w", BearerScheme, err)
				}

				// Anything else is about the token, and the caller hears the
				// same "no" however it was wrong. What the store said is not
				// passed on: expired and never-existed, told apart, are an
				// oracle for "this string was a real token once", which is
				// worth more to somebody guessing than to anybody else.
				return Identity{}, fmt.Errorf("%s: %w", BearerScheme, ErrUnknownToken)
			}
			if id.NamesNobody() {
				// A store that answered without saying who. Whatever it meant
				// by that, it is not a caller, and serving one would be
				// serving nobody as somebody.
				return Identity{}, fmt.Errorf("%s: %w", BearerScheme, ErrUnknownToken)
			}

			id.Method = MethodBearer

			return id, nil
		}

		return Identity{}, ErrNoCredential
	})
}

// ErrUnknownToken is a token that is not one this server knows, or not one it
// still honours. It says no more than that on purpose: which of the two, and
// how long ago, is not something a caller who guessed needs to learn.
var ErrUnknownToken = fmt.Errorf("no such token")

// BearerProvider says who the caller is in the way [Bearer] reads.
func BearerProvider(token string) Provider {
	return ProviderFunc(func(ctx context.Context) context.Context {
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = metadata.MD{}
		} else {
			md = md.Copy()
		}

		md.Set("authorization", BearerScheme+" "+token)
		return metadata.NewOutgoingContext(ctx, md)
	})
}

// MemTokenStore is a [TokenStore] held in memory. It is a sample.
//
// What it does show is worth keeping in a real one: the tokens are held as
// digests rather than as themselves, so the store cannot give away what it was
// told, and each has a life of its own that is not the actor's. Looking one up
// is a lookup by digest, so how long it takes does not depend on how much of a
// guess was right -- a store that instead fetches a row and compares owes that
// comparison a constant-time comparison.
//
// What it does not show is everything else: there is no persistence, no
// revocation beyond forgetting, and no way to be unavailable, because a map
// cannot be.
//
// It is safe for concurrent use.
type MemTokenStore struct {
	mu sync.RWMutex
	vs map[[sha256.Size]byte]memToken
	// Now is read instead of time.Now when it is set, which is how a test
	// reaches an expiry without waiting for one.
	Now func() time.Time
}

type memToken struct {
	id Identity
	// exp is when the token stops being honoured; zero means never. A
	// certificate and a header have no such thing -- they are good for as long
	// as the actor is -- and this is the difference a store has to carry.
	exp time.Time
}

func NewMemTokenStore() *MemTokenStore {
	return &MemTokenStore{vs: map[[sha256.Size]byte]memToken{}}
}

// Add records that `token` stands for whoever `id` names and allows what its
// [Identity.Grant] allows, until `exp`. A zero `exp` never expires.
func (s *MemTokenStore) Add(token string, id Identity, exp time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.vs == nil {
		s.vs = map[[sha256.Size]byte]memToken{}
	}
	s.vs[sha256.Sum256([]byte(token))] = memToken{id: id, exp: exp}
}

// Remove forgets a token, which is what revoking one is here.
func (s *MemTokenStore) Remove(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.vs, sha256.Sum256([]byte(token)))
}

// Len is how many tokens are held, for a test that wants to say so.
func (s *MemTokenStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.vs)
}

func (s *MemTokenStore) Lookup(_ context.Context, token string) (Identity, error) {
	sum := sha256.Sum256([]byte(token))

	s.mu.RLock()
	v, ok := s.vs[sum]
	s.mu.RUnlock()

	if !ok {
		return Identity{}, ErrUnknownToken
	}
	if !v.exp.IsZero() && !s.now().Before(v.exp) {
		return Identity{}, fmt.Errorf("expired: %w", ErrUnknownToken)
	}

	return v.id, nil
}

func (s *MemTokenStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
