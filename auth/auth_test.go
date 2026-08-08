package auth_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// The domains an app would declare, which is what generated code does at init.
// Only [slug] reads them -- a "#word" is looked up here -- so two are enough.
const (
	Tenant pdid.Domain = 1
	Holder pdid.Domain = 2
)

func TestMain(m *testing.M) {
	pdid.Register("test.Tenant", Tenant, "tenant")
	pdid.Register("test.Holder", Holder, "holder")

	m.Run()
}

// tlsState is a connection state with the given verified chains, and the given
// certificates as what the peer merely sent.
func tlsState(verified [][]*x509.Certificate, sent ...*x509.Certificate) tls.ConnectionState {
	return tls.ConnectionState{VerifiedChains: verified, PeerCertificates: sent}
}

// incoming is a request carrying the given authorization header.
func incoming(v ...string) context.Context {
	md := metadata.MD{}
	if len(v) > 0 {
		md.Set("authorization", v...)
	}
	return metadata.NewIncomingContext(context.Background(), md)
}

// verified is a connection whose certificate this server checked. Only a chain
// the server verified is one the handler may read, which is why the test puts
// the certificate there rather than in PeerCertificates.
func verified(cert *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tlsState([][]*x509.Certificate{{cert}})},
	})
}

// presented is a connection whose certificate this server did NOT verify: the
// other end sent it and nothing checked it.
func presented(cert *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tlsState(nil, cert)},
	})
}

func certOf(cn string, uris ...string) *x509.Certificate {
	v := &x509.Certificate{Subject: pkix.Name{CommonName: cn}}
	for _, u := range uris {
		p, err := url.Parse(u)
		if err != nil {
			panic(err)
		}
		v.URIs = append(v.URIs, p)
	}
	return v
}

// admin is who most of these are about.
func admin() auth.Identity {
	return auth.Identity{Tenant: "acme", Alias: "admin"}
}

func TestPlain(t *testing.T) {
	x := require.New(t)

	v, err := auth.Plain().Handle(incoming("Plain @acme/admin"))
	x.NoError(err)
	x.Equal(auth.MethodPlain, v.Method)
	x.Equal("acme", v.Tenant)
	x.Equal("admin", v.Alias)
	x.Empty(v.Id)

	// A header has nowhere to carry an attenuation, so it says it narrows
	// nothing rather than leaving the zero Grant, which allows nothing at all.
	x.True(v.Grant.IsWhole())

	// Nothing said is not the same as something wrong.
	_, err = auth.Plain().Handle(incoming())
	x.ErrorIs(err, auth.ErrNoCredential)

	_, err = auth.Plain().Handle(incoming("Plain "))
	x.Error(err)
	x.NotErrorIs(err, auth.ErrNoCredential)
}

func TestMTLS(t *testing.T) {
	t.Run("the name is read from the verified chain", func(t *testing.T) {
		x := require.New(t)

		v, err := auth.MTLS().Handle(verified(certOf("@acme/admin")))
		x.NoError(err)
		x.Equal(auth.MethodMTLS, v.Method)
		x.Equal("acme", v.Tenant)
		x.Equal("admin", v.Alias)
		x.True(v.Grant.IsWhole())
	})

	t.Run("a URI is preferred over the common name", func(t *testing.T) {
		x := require.New(t)

		v, err := auth.MTLS().Handle(verified(certOf("@hooli/erlich", "spiffe://example.com/@acme/admin")))
		x.NoError(err)
		x.Equal("acme", v.Tenant)
		x.Equal("admin", v.Alias)
	})

	// The one that matters: what the peer sent is not what this server
	// checked, and only the second is a claim about anybody.
	t.Run("a certificate nobody verified says nothing", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.MTLS().Handle(presented(certOf("@acme/admin")))
		x.ErrorIs(err, auth.ErrNoCredential)
	})

	t.Run("no connection, no TLS, no name", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.MTLS().Handle(context.Background())
		x.ErrorIs(err, auth.ErrNoCredential)

		_, err = auth.MTLS().Handle(peer.NewContext(context.Background(), &peer.Peer{}))
		x.ErrorIs(err, auth.ErrNoCredential)

		// Verified, and carrying no name at all: another handler may know them.
		_, err = auth.MTLS().Handle(verified(certOf("")))
		x.ErrorIs(err, auth.ErrNoCredential)
	})

	t.Run("a name that is not one is wrong rather than absent", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.MTLS().Handle(verified(certOf("Acme Corporation, Inc.")))
		x.Error(err)
		x.NotErrorIs(err, auth.ErrNoCredential)
	})
}

func TestBearer(t *testing.T) {
	store := func(t *testing.T) *auth.MemTokenStore {
		t.Helper()
		s := auth.NewMemTokenStore()

		v := admin()
		v.Grant = frame.Whole()
		s.Add("s3cret", v, time.Time{})

		return s
	}

	t.Run("a token is exchanged for a name", func(t *testing.T) {
		x := require.New(t)

		v, err := auth.Bearer(store(t)).Handle(incoming("Bearer s3cret"))
		x.NoError(err)
		x.Equal(auth.MethodBearer, v.Method)
		x.Equal("acme", v.Tenant)
		x.Equal("admin", v.Alias)
	})

	// The only handler that can, because a header and a certificate name
	// somebody and stop.
	t.Run("a token carries what it was narrowed to", func(t *testing.T) {
		x := require.New(t)

		v := admin()
		v.Grant = frame.Whole().To("/app.RobotService/Get")

		s := auth.NewMemTokenStore()
		s.Add("read-only", v, time.Time{})

		u, err := auth.Bearer(s).Handle(incoming("Bearer read-only"))
		x.NoError(err)
		x.False(u.Grant.IsWhole())
		x.True(u.Grant.Allows("/app.RobotService/Get"))
		x.False(u.Grant.Allows("/app.RobotService/Erase"))
	})

	// A store that fills in the name and forgets the grant hands out a
	// credential that can do nothing, which somebody notices immediately; the
	// other way round it hands out one that can do everything.
	t.Run("a store that forgets to say what a token allows allows nothing", func(t *testing.T) {
		x := require.New(t)

		s := auth.NewMemTokenStore()
		s.Add("forgot", admin(), time.Time{})

		v, err := auth.Bearer(s).Handle(incoming("Bearer forgot"))
		x.NoError(err)
		x.False(v.Grant.Allows("/app.RobotService/Get"))
	})

	t.Run("the token is never in the answer", func(t *testing.T) {
		x := require.New(t)

		v, err := auth.Bearer(store(t)).Handle(incoming("Bearer s3cret"))
		x.NoError(err)
		x.NotContains(v.Method, "s3cret")
		x.NotContains(v.Name(), "s3cret")

		// Nor in what is said when it is refused.
		_, err = auth.Bearer(store(t)).Handle(incoming("Bearer wrong-one"))
		x.Error(err)
		x.NotContains(err.Error(), "wrong-one")
	})

	t.Run("no token said is not a bad token", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.Bearer(store(t)).Handle(incoming())
		x.ErrorIs(err, auth.ErrNoCredential)

		// A scheme this handler does not read is also nothing said.
		_, err = auth.Bearer(store(t)).Handle(incoming("Plain @acme/admin"))
		x.ErrorIs(err, auth.ErrNoCredential)
	})

	t.Run("an unknown token is refused and does not fall through", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.Bearer(store(t)).Handle(incoming("Bearer nope"))
		x.ErrorIs(err, auth.ErrUnknownToken)
		x.NotErrorIs(err, auth.ErrNoCredential)
		x.NotErrorIs(err, auth.ErrUnavailable)
	})

	// A token has a life of its own, which a header and a certificate do not.
	t.Run("an expired token is not honoured", func(t *testing.T) {
		x := require.New(t)

		at := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
		s := auth.NewMemTokenStore()
		s.Now = func() time.Time { return at }
		s.Add("s3cret", admin(), at.Add(time.Hour))

		_, err := auth.Bearer(s).Handle(incoming("Bearer s3cret"))
		x.NoError(err)

		s.Now = func() time.Time { return at.Add(time.Hour) }
		_, err = auth.Bearer(s).Handle(incoming("Bearer s3cret"))
		x.ErrorIs(err, auth.ErrUnknownToken)
	})

	// Told apart, "expired" and "never existed" are an oracle: a guesser learns
	// that a string was a real token once, which is the hard half of having one.
	t.Run("expired and unknown are the same no", func(t *testing.T) {
		x := require.New(t)

		at := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
		s := auth.NewMemTokenStore()
		s.Now = func() time.Time { return at.Add(time.Hour) }
		s.Add("was-real", admin(), at)

		_, expired := auth.Bearer(s).Handle(incoming("Bearer was-real"))
		_, unknown := auth.Bearer(s).Handle(incoming("Bearer never-was"))
		x.ErrorIs(expired, auth.ErrUnknownToken)
		x.ErrorIs(unknown, auth.ErrUnknownToken)
		x.Equal(unknown.Error(), expired.Error())
	})

	// What the store said about a bad token stays with the store; what it said
	// about itself does not.
	t.Run("only unavailable is passed on", func(t *testing.T) {
		x := require.New(t)

		chatty := auth.TokenStoreFunc(func(context.Context, string) (auth.Identity, error) {
			return auth.Identity{}, fmt.Errorf("row 41 of tokens: revoked by admin on tuesday")
		})
		_, err := auth.Bearer(chatty).Handle(incoming("Bearer s3cret"))
		x.ErrorIs(err, auth.ErrUnknownToken)
		x.NotContains(err.Error(), "tuesday")

		down := auth.TokenStoreFunc(func(context.Context, string) (auth.Identity, error) {
			return auth.Identity{}, fmt.Errorf("dial tcp: %w", auth.ErrUnavailable)
		})
		_, err = auth.Bearer(down).Handle(incoming("Bearer s3cret"))
		x.ErrorIs(err, auth.ErrUnavailable)
		x.Contains(err.Error(), "dial tcp")
	})

	// A store may answer without an error and without a name, and that is not
	// somebody; serving it would serve nobody as somebody.
	t.Run("a token that stands for nobody is no token", func(t *testing.T) {
		x := require.New(t)

		empty := auth.TokenStoreFunc(func(context.Context, string) (auth.Identity, error) {
			return auth.Identity{Tenant: "acme", Grant: frame.Whole()}, nil
		})

		_, err := auth.Bearer(empty).Handle(incoming("Bearer s3cret"))
		x.ErrorIs(err, auth.ErrUnknownToken)
	})

	t.Run("a forgotten token is a revoked one", func(t *testing.T) {
		x := require.New(t)

		s := store(t)
		s.Remove("s3cret")
		x.Equal(0, s.Len())

		_, err := auth.Bearer(s).Handle(incoming("Bearer s3cret"))
		x.ErrorIs(err, auth.ErrUnknownToken)
	})

	t.Run("a store that cannot answer says so", func(t *testing.T) {
		x := require.New(t)

		down := auth.TokenStoreFunc(func(context.Context, string) (auth.Identity, error) {
			return auth.Identity{}, auth.ErrUnavailable
		})

		_, err := auth.Bearer(down).Handle(incoming("Bearer s3cret"))
		x.ErrorIs(err, auth.ErrUnavailable)
		x.NotErrorIs(err, auth.ErrNoCredential)
	})
}

func TestSeq(t *testing.T) {
	cert := certOf("@hooli/erlich")

	// The stack the configuration builds for `methods: [bearer, mtls]`, on a
	// connection that has a certificate.
	fallback := func(store auth.TokenStore) auth.Handler {
		return auth.Seq(auth.Bearer(store), auth.MTLS())
	}
	with := func(header string) context.Context {
		ctx := verified(cert)
		md := metadata.MD{}
		if header != "" {
			md.Set("authorization", header)
		}
		return metadata.NewIncomingContext(ctx, md)
	}

	good := auth.NewMemTokenStore()
	good.Add("s3cret", admin(), time.Time{})

	t.Run("the token answers when there is one", func(t *testing.T) {
		x := require.New(t)

		v, err := fallback(good).Handle(with("Bearer s3cret"))
		x.NoError(err)
		x.Equal(auth.MethodBearer, v.Method)
		x.Equal("@acme/admin", v.Name())
	})

	t.Run("the certificate answers when there is not", func(t *testing.T) {
		x := require.New(t)

		v, err := fallback(good).Handle(with(""))
		x.NoError(err)
		x.Equal(auth.MethodMTLS, v.Method)
		x.Equal("@hooli/erlich", v.Name())
	})

	// The point of the whole arrangement: a bad token must not quietly become
	// somebody else. The certificate on this connection names a different
	// actor, and answering as them would be answering a question nobody asked.
	t.Run("a bad token does not fall through to the certificate", func(t *testing.T) {
		x := require.New(t)

		_, err := fallback(good).Handle(with("Bearer nope"))
		x.ErrorIs(err, auth.ErrUnknownToken)
	})

	t.Run("a store that is down does not fall through either", func(t *testing.T) {
		x := require.New(t)

		down := auth.TokenStoreFunc(func(context.Context, string) (auth.Identity, error) {
			return auth.Identity{}, auth.ErrUnavailable
		})

		_, err := fallback(down).Handle(with("Bearer s3cret"))
		x.ErrorIs(err, auth.ErrUnavailable)
	})

	t.Run("nothing at all is nothing said", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.Seq(auth.Bearer(good), auth.MTLS()).Handle(incoming())
		x.ErrorIs(err, auth.ErrNoCredential)

		// And no handler at all is the same.
		_, err = auth.Seq().Handle(incoming("Bearer s3cret"))
		x.ErrorIs(err, auth.ErrNoCredential)
	})
}

func TestName(t *testing.T) {
	t.Run("a name and an identifier read back as what they were written as", func(t *testing.T) {
		x := require.New(t)

		v, err := auth.ParseName(admin().Name())
		x.NoError(err)
		x.Equal(admin(), v)

		id := pdid.New(Holder)
		u, err := auth.ParseName(auth.Identity{Id: id.String()}.Name())
		x.NoError(err)
		x.Equal(id.String(), u.Id)
		x.Empty(u.Alias)
	})

	// An identifier is the one that names a row; an alias is a name a row was
	// given and may be given to another once it is gone.
	t.Run("an identity that names both spells the identifier", func(t *testing.T) {
		x := require.New(t)

		id := pdid.New(Holder)
		v := admin()
		v.Id = id.String()

		x.Equal(id.String(), v.Name())
	})

	t.Run("an identity that names nobody spells nothing", func(t *testing.T) {
		x := require.New(t)

		x.Empty(auth.Identity{}.Name())
		x.True(auth.Identity{}.NamesNobody())

		// A tenant is where an alias would have been read, and there is no
		// alias.
		x.Empty(auth.Identity{Tenant: "acme"}.Name())
		x.True(auth.Identity{Tenant: "acme"}.NamesNobody())
	})

	// The bytes of an identifier are what something dumping them writes, and
	// they name the same row as the UUID does.
	t.Run("an identifier is read whichever way it was spelled", func(t *testing.T) {
		x := require.New(t)

		id := pdid.New(Holder)
		v, err := auth.ParseName(hex.EncodeToString(id.Bytes()))
		x.NoError(err)
		x.Equal(id.String(), v.Id)
	})

	// The header form changed with this, and it is the whole of what the "@"
	// buys: the two kinds of reference are not told apart by looking at them.
	t.Run("a name without the mark is not a name", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.ParseName("acme/admin")
		x.Error(err)
	})

	t.Run("a UUID that is not one of ours names a row this app never wrote", func(t *testing.T) {
		x := require.New(t)

		// A v4, which says nothing about what kind of thing it names.
		_, err := auth.ParseName("f81d4fae-7dec-11d0-a765-00a0c91e6bf6")
		x.Error(err)
	})

	t.Run("what is not a name is refused rather than half read", func(t *testing.T) {
		for _, s := range []string{
			"",
			"@",
			"@/",
			"@acme/",
			"@/admin",
			"not a name",
			// An alias with no tenant names one row in every tenant there is.
			"@admin",
			// A credential says who is calling and not what kind of thing they
			// are, and an assertion nothing here can check would be dropped
			// rather than disagreed with.
			"@acme/admin#holder",
			"@acme/admin#robto",
		} {
			x := require.New(t)

			_, err := auth.ParseName(s)
			x.Error(err, "%q", s)
		}
	})
}
