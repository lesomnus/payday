package authsession

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdpb"
)

// Sealed is the session in the cookie: a [Store] with nothing to store.
//
// [Sessions.Mint] asks it for the key and it answers with the session
// encrypted -- AES-256-GCM under the first key it was given, a fresh nonce each
// time, the whole thing base64url so it is a cookie value. Reading the key
// back is opening it: any key this was given may open one, which is what
// rotation is -- a new first key, the old ones kept until every cookie sealed
// under them has expired, then dropped.
//
// # What it costs, said once
//
// Nothing on the server can end a sealed session: there is no row to delete,
// and a cookie already in a browser keeps opening until [Session.Expires].
// [Sessions.End] clears the cookie in that browser and nothing else. So this is
// for an app whose session is a handle to something ended elsewhere -- which
// it says by putting that thing in [Session.Held] -- and the lifetime is the
// only clock; see [Sealer].
//
// A cookie that opens is trusted as opened. What GCM promises is that nobody
// without the key made it or changed it, and that is the whole of the check:
// there is no store to disagree with it.
type Sealed struct {
	seal  cipher.AEAD
	opens []cipher.AEAD
}

var _ Sealer = (*Sealed)(nil)

// KeySize is what a key has to be: AES-256.
const KeySize = 32

// NewSealed takes one key or several, the first of which seals.
func NewSealed(keys ...[]byte) (*Sealed, error) {
	if len(keys) == 0 {
		return nil, errors.New("authsession: sealed: a key, or nothing can be sealed")
	}

	s := &Sealed{}
	for i, k := range keys {
		if len(k) != KeySize {
			return nil, fmt.Errorf("authsession: sealed: key %d is %d bytes, and it has to be %d", i, len(k), KeySize)
		}
		b, err := aes.NewCipher(k)
		if err != nil {
			return nil, fmt.Errorf("authsession: sealed: %w", err)
		}
		g, err := cipher.NewGCM(b)
		if err != nil {
			return nil, fmt.Errorf("authsession: sealed: %w", err)
		}
		s.opens = append(s.opens, g)
	}
	s.seal = s.opens[0]

	return s, nil
}

// sealed is what goes under the seal. The grant is the wire form `auth` already
// has for it, so a Grant round-trips without this package learning its shape.
type sealed struct {
	Id       string            `json:"id"`
	TenantId string            `json:"tenant,omitempty"`
	Grant    []byte            `json:"grant,omitempty"`
	Expires  int64             `json:"exp,omitempty"`
	Held     map[string]string `json:"held,omitempty"`
}

// Seal answers with the session as a key.
func (s *Sealed) Seal(v Session) (string, error) {
	id, err := auth.Introspection(auth.Identity{Grant: v.Grant})
	if err != nil {
		return "", err
	}
	grant, err := proto.Marshal(id.GetGrant())
	if err != nil {
		return "", err
	}

	body := sealed{Id: v.Id, TenantId: v.TenantId, Grant: grant, Held: v.Held}
	if !v.Expires.IsZero() {
		body.Expires = v.Expires.Unix()
	}
	plain, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, s.seal.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.seal.Seal(nonce, nonce, plain, nil)

	return base64.RawURLEncoding.EncodeToString(out), nil
}

// Put has nothing to do: the session went out with the key.
func (s *Sealed) Put(ctx context.Context, v Session) error { return nil }

// Del has nothing to do either, which is the cost the type comment names.
func (s *Sealed) Del(ctx context.Context, key string) error { return nil }

// Get opens the key, with whichever of its keys made it.
func (s *Sealed) Get(ctx context.Context, key string) (Session, error) {
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		return Session{}, ErrNoSession
	}

	var plain []byte
	for _, g := range s.opens {
		n := g.NonceSize()
		if len(raw) < n {
			continue
		}
		if plain, err = g.Open(nil, raw[:n], raw[n:], nil); err == nil {
			break
		}
	}
	if plain == nil {
		return Session{}, ErrNoSession
	}

	var body sealed
	if err := json.Unmarshal(plain, &body); err != nil {
		return Session{}, ErrNoSession
	}

	v := Session{Key: key, Id: body.Id, TenantId: body.TenantId, Held: body.Held}
	if body.Expires != 0 {
		v.Expires = time.Unix(body.Expires, 0)
	}
	if len(body.Grant) > 0 {
		g := &pdpb.Grant{}
		if err := proto.Unmarshal(body.Grant, g); err != nil {
			return Session{}, ErrNoSession
		}
		id, err := auth.IdentityFrom(pdpb.TokenIntrospectResponse_builder{Grant: g}.Build())
		if err != nil {
			return Session{}, ErrNoSession
		}
		v.Grant = id.Grant
	}

	return v, nil
}
