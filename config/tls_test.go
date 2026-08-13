package config_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
)

// keypair writes a self-signed certificate and its key, and answers with the
// two paths. It is its own certificate authority, so the same file serves as a
// client CA bundle.
func keypair(t *testing.T) (cert string, key string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	der_key, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cert = write(t, dir, "cert.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	key = write(t, dir, "key.pem", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der_key})))

	return cert, key
}

func TestTLSConfig(t *testing.T) {
	t.Run("nothing said is a connection anybody may open", func(t *testing.T) {
		x := require.New(t)

		c := config.TLSConfig{}
		x.False(c.Active())

		// Insecure credentials rather than nothing, so the wiring hands the
		// answer to grpc.Creds either way.
		v, err := c.Credentials()
		x.NoError(err)
		x.NotNil(v)
	})
	t.Run("a certificate and its key are TLS", func(t *testing.T) {
		x := require.New(t)

		cert, key := keypair(t)
		c := config.TLSConfig{CertFile: cert, KeyFile: key}
		x.True(c.Active())

		v, err := c.Credentials()
		x.NoError(err)
		x.Equal("tls", v.Info().SecurityProtocol)
	})
	t.Run("half of a key pair is refused", func(t *testing.T) {
		x := require.New(t)

		cert, _ := keypair(t)
		_, err := config.TLSConfig{CertFile: cert}.Credentials()
		x.ErrorContains(err, "key_file")
	})

	// The template this came from did not count a client CA bundle as TLS
	// being on. A configuration holding only that one is exactly what a server
	// meant to read who is calling out of a client certificate looks like, and
	// it would have been served over a connection with no handshake at all --
	// no certificate presented, none verified, and the method that reads them
	// answering "nobody said anything" for the life of the process, with
	// mutual TLS written down in the file the whole time.
	t.Run("a bundle to check callers against is TLS, and half-configured is refused", func(t *testing.T) {
		x := require.New(t)

		cert, _ := keypair(t)
		c := config.TLSConfig{ClientCAFile: cert}
		x.True(c.Active())

		_, err := c.Credentials()
		x.ErrorContains(err, "cert_file")
	})
	t.Run("a bundle with no certificate in it is refused", func(t *testing.T) {
		x := require.New(t)

		cert, key := keypair(t)
		bundle := write(t, t.TempDir(), "ca.pem", "this is not a certificate\n")

		_, err := config.TLSConfig{CertFile: cert, KeyFile: key, ClientCAFile: bundle}.Credentials()
		x.ErrorContains(err, "no certificates found")
	})
	t.Run("a bundle nobody wrote is refused", func(t *testing.T) {
		x := require.New(t)

		cert, key := keypair(t)
		_, err := config.TLSConfig{CertFile: cert, KeyFile: key, ClientCAFile: "nowhere.pem"}.Credentials()
		x.ErrorContains(err, "read client ca")
	})
}

// TestTheSameTlsServesEveryListener.
//
// `Credentials` is gRPC's shape and not every listener a payday app opens is a
// gRPC one -- a transcoder, a sign-in, a port robots present certificates to.
// Without a `*tls.Config` of its own, the app builds the mutual-TLS setup a
// second time by hand, and the second one is where the client CA is forgotten.
func TestTheSameTlsServesEveryListener(t *testing.T) {
	cert, key := keypair(t)

	t.Run("nothing said is nothing to serve with", func(t *testing.T) {
		x := require.New(t)

		got, err := config.TLSConfig{}.Server()
		x.NoError(err)
		x.Nil(got, "a plaintext listener has to be able to tell")
	})

	t.Run("a certificate and a client ca", func(t *testing.T) {
		x := require.New(t)

		got, err := config.TLSConfig{
			CertFile:     cert,
			KeyFile:      key,
			ClientCAFile: cert,
		}.Server()
		x.NoError(err)
		x.NotNil(got)
		x.Len(got.Certificates, 1)
		x.NotNil(got.ClientCAs)
		x.Equal(tls.RequireAndVerifyClientCert, got.ClientAuth)
	})

	t.Run("and a caller that may arrive without one", func(t *testing.T) {
		x := require.New(t)

		got, err := config.TLSConfig{
			CertFile:           cert,
			KeyFile:            key,
			ClientCAFile:       cert,
			ClientCertOptional: true,
		}.Server()
		x.NoError(err)
		x.Equal(tls.VerifyClientCertIfGiven, got.ClientAuth)
	})

	t.Run("half a key pair is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := config.TLSConfig{CertFile: cert}.Server()
		x.Error(err)
	})
}
