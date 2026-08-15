package config_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/grpcx"
)

func TestServerConfig(t *testing.T) {
	t.Run("nothing said is nothing given, so gRPC keeps its own", func(t *testing.T) {
		x := require.New(t)

		c := config.ServerConfig{}
		vs, err := c.GrpcOptions()
		x.NoError(err)

		// One: the credentials, which are the insecure ones when nothing said
		// anything -- the same server `grpc.NewServer` builds on its own, and
		// passed unconditionally so that the branch which would decide cannot
		// be wrong.
		x.Len(vs, 1)

		// And what a caller may reach for is off: an unwritten field is a
		// deployment that did not ask, and neither of these is served to
		// somebody who did not.
		x.False(c.AllowReflection)
		x.NotNil(c.Closed())
	})
	t.Run("what is said is given", func(t *testing.T) {
		x := require.New(t)

		c := config.ServerConfig{
			MaxRecvMsgSize:       1 << 20,
			MaxConcurrentStreams: 100,
			Keepalive: config.KeepaliveConfig{
				MaxConnectionAge: 30 * time.Minute,
				MinTime:          10 * time.Second,
			},
		}

		// The credentials, a size, a stream limit, the parameters and the policy.
		vs, err := c.GrpcOptions()
		x.NoError(err)
		x.Len(vs, 5)
	})
	t.Run("an address nobody gave is one somebody can reach", func(t *testing.T) {
		x := require.New(t)

		// The alternative to a default here is a listener on whichever port
		// the kernel handed out, which serves and answers and is reachable by
		// nobody.
		x.Equal(config.DefaultAddr, config.ServerConfig{}.ListenAddr())
		x.Equal(":1234", config.ServerConfig{Addr: ":1234"}.ListenAddr())
	})
	t.Run("the usual cap is what nothing written down means", func(t *testing.T) {
		x := require.New(t)

		// Zero is the ordinary answer, so a field nobody filled in is the
		// ordinary answer -- and saying "cap nothing" is a thing said, which
		// is what a negative one is for.
		x.Equal(grpcx.DefaultTimeout, config.ServerConfig{}.CallTimeout())
		x.Equal(time.Minute, config.ServerConfig{Timeout: time.Minute}.CallTimeout())
		x.Negative(config.ServerConfig{Timeout: -1}.CallTimeout())
	})
	t.Run("no rate is no limiter", func(t *testing.T) {
		x := require.New(t)

		// Not a limiter that allows everything -- nothing, so the chain a
		// deployment that said nothing is served with is the one it had.
		x.Nil(config.ServerConfig{}.Limiter())
		x.Nil(config.ServerConfig{Limit: config.LimitConfig{Burst: 10}}.Limiter())
	})
	t.Run("a rate with no burst is one second's worth", func(t *testing.T) {
		x := require.New(t)

		x.Equal(20, config.LimitConfig{Rate: 20}.BurstOr())
		// Rounded up, since a burst is a whole number of calls and rounding
		// down a rate below one would refuse everything.
		x.Equal(1, config.LimitConfig{Rate: 0.5}.BurstOr())
		x.Equal(40, config.LimitConfig{Rate: 20, Burst: 40}.BurstOr())

		x.NotNil(config.ServerConfig{Limit: config.LimitConfig{Rate: 20}}.Limiter())
	})
	t.Run("the environment says the same things the file does", func(t *testing.T) {
		x := require.New(t)

		type Config struct {
			Server config.ServerConfig `yaml:"server"`
		}

		var c Config
		_, err := acme.OverrideFromEnv(&c, []string{
			"ACME_SERVER_ALLOW_REFLECTION=true",
			"ACME_SERVER_MAX_RECV_MSG_SIZE=1048576",
			"ACME_SERVER_KEEPALIVE_MAX_CONNECTION_AGE=30m",
			"ACME_SERVER_LIMIT_RATE=50",
		})
		x.NoError(err)

		x.True(c.Server.AllowReflection)
		x.Equal(1<<20, c.Server.MaxRecvMsgSize)
		x.Equal(30*time.Minute, c.Server.Keepalive.MaxConnectionAge)
		// The number a deployment is most likely to want to move without
		// building an image, which is why it is worth a line here.
		x.Equal(float64(50), c.Server.Limit.Rate)
	})
}

func TestHttpConfig(t *testing.T) {
	t.Run("no address is no second listener", func(t *testing.T) {
		x := require.New(t)

		x.False(config.HttpConfig{}.Serves())
		x.True(config.HttpConfig{Addr: ":8080"}.Serves())
	})
	t.Run("a page nobody named is not answered for", func(t *testing.T) {
		x := require.New(t)

		// Nothing rather than a rule that says no, since that is what tells
		// the wiring there is no cross-origin story here at all -- which is
		// not "nothing works": a page on this same origin needs no answer.
		x.Nil(config.HttpConfig{}.Origin())

		is := config.HttpConfig{Origins: []string{"http://localhost:5173"}}.Origin()
		x.NotNil(is)
		x.True(is("http://localhost:5173"))
		x.False(is("https://somewhere.else"))
	})
	t.Run("a star is every page on the internet", func(t *testing.T) {
		x := require.New(t)

		is := config.HttpConfig{Origins: []string{"*"}}.Origin()
		x.True(is("https://anywhere.at.all"))
	})
}

// TestTLSIsActuallyServed is the whole of why `GrpcOptions` answers with an
// error.
//
// `ServerConfig.TLS` was a block a deployment could write and nothing read: not
// this function, not the template `pd new` writes, not payday's own test app. A
// deployment that configured a certificate got a plaintext listener and no
// complaint -- which is the direction nobody notices, because the port answers
// and the calls work.
func TestTLSIsActuallyServed(t *testing.T) {
	t.Run("a certificate that cannot be read is a server that does not start", func(t *testing.T) {
		x := require.New(t)

		c := config.ServerConfig{TLS: config.TLSConfig{
			CertFile: filepath.Join(t.TempDir(), "nowhere.pem"),
			KeyFile:  filepath.Join(t.TempDir(), "nowhere.key"),
		}}

		_, err := c.GrpcOptions()
		x.Error(err, "it used to answer with options that quietly served plaintext")
		x.Contains(err.Error(), "tls")
	})

	t.Run("and one that can be read is served", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
		writeCert(t, cert, key)

		c := config.ServerConfig{TLS: config.TLSConfig{CertFile: cert, KeyFile: key}}

		vs, err := c.GrpcOptions()
		x.NoError(err)
		x.Len(vs, 1)

		// The server it builds speaks TLS, which is the thing a count of
		// options cannot say. A plaintext dial gets nowhere.
		g := grpc.NewServer(vs...)
		t.Cleanup(g.Stop)

		l, err := net.Listen("tcp", "127.0.0.1:0")
		x.NoError(err)
		go func() { _ = g.Serve(l) }()

		conn, err := grpc.NewClient(l.Addr().String(),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		x.NoError(err)
		t.Cleanup(func() { conn.Close() })

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()

		err = conn.Invoke(ctx, "/nothing.Service/Nothing", &emptypb.Empty{}, &emptypb.Empty{})
		x.Error(err, "a plaintext caller reached a TLS listener and was answered")
		x.NotEqual(codes.Unimplemented, status.Code(err),
			"Unimplemented would mean the handshake succeeded and the method was missing")
	})
}

// writeCert puts a self-signed certificate and its key on disk.
func writeCert(t *testing.T, certFile, keyFile string) {
	t.Helper()
	x := require.New(t)

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	x.NoError(err)

	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	x.NoError(err)

	x.NoError(os.WriteFile(certFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	x.NoError(os.WriteFile(keyFile,
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600))
}
