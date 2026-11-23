package config_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/cfg/ref"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"

	"github.com/lesomnus/payday/config"
)

// keypair writes a self-signed certificate for the name "test" and its key,
// and answers with the two paths. It is its own certificate authority, so the
// same file serves as a client CA bundle.
func keypair(t *testing.T) (cert string, key string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		DNSNames:              []string{"test"},
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

// literal is text as the environment would hand it over: a literal, or one
// reference.
func literal(t *testing.T, text string) ref.String {
	t.Helper()

	var s ref.String
	require.NoError(t, s.UnmarshalText([]byte(text)))
	return s
}

// fileRef is the file at path, as `${file:}` reads it.
func fileRef(t *testing.T, path string) ref.String {
	t.Helper()

	var s ref.String
	require.NoError(t, s.SetFile(path))
	return s
}

// read is what the file at path holds.
func read(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// indent is a PEM as a YAML block scalar under a key indented by n.
func indent(v string, n int) string {
	pad := strings.Repeat(" ", n+2)
	return "|\n" + pad + strings.ReplaceAll(strings.TrimSuffix(v, "\n"), "\n", "\n"+pad)
}

// handshake completes a TLS handshake between a server and a client, each
// with the credentials gRPC would be given, and answers with what the server
// learned of it.
func handshake(t *testing.T, s credentials.TransportCredentials, c credentials.TransportCredentials) (tls.ConnectionState, error) {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })

	type served struct {
		info credentials.AuthInfo
		err  error
	}
	got := make(chan served, 1)
	go func() {
		raw, err := l.Accept()
		if err != nil {
			got <- served{err: err}
			return
		}
		conn, info, err := s.ServerHandshake(raw)
		if err == nil {
			conn.Close()
		}
		got <- served{info, err}
	}()

	raw, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, _, err := c.ClientHandshake(ctx, "test", raw); err != nil {
		return tls.ConnectionState{}, err
	}

	v := <-got
	if v.err != nil {
		return tls.ConnectionState{}, v.err
	}
	return v.info.(credentials.TLSInfo).State, nil
}

func TestTlsConfig(t *testing.T) {
	t.Run("nothing said is a connection anybody may open", func(t *testing.T) {
		x := require.New(t)

		c := config.TlsConfig{}
		x.False(c.Active())

		// Insecure credentials rather than nothing, so the wiring hands the
		// answer to grpc.Creds either way.
		v, err := c.Credentials()
		x.NoError(err)
		x.NotNil(v)
	})
	t.Run("a certificate and its key are Tls", func(t *testing.T) {
		x := require.New(t)

		cert, key := keypair(t)
		c := config.TlsConfig{Cert: fileRef(t, cert), Key: fileRef(t, key)}
		x.True(c.Active())

		v, err := c.Credentials()
		x.NoError(err)
		x.Equal("tls", v.Info().SecurityProtocol)
	})

	// Each of the two alone, and the refusal names both as the file writes
	// them: the one that is missing is the one the reader has to go and find.
	t.Run("half of a key pair is refused", func(t *testing.T) {
		x := require.New(t)

		cert, key := keypair(t)
		for _, c := range []config.TlsConfig{
			{Cert: fileRef(t, cert)},
			{Key: literal(t, read(t, key))},
		} {
			_, err := c.Credentials()
			x.EqualError(err, "tls: both cert and key must be set")
		}
	})

	// The template this came from did not count a client CA bundle as TLS
	// being on. A configuration holding only that one is exactly what a server
	// meant to read who is calling out of a client certificate looks like, and
	// it would have been served over a connection with no handshake at all --
	// no certificate presented, none verified, and the method that reads them
	// answering "nobody said anything" for the life of the process, with
	// mutual TLS written down in the file the whole time.
	t.Run("a bundle to check callers against is Tls, and half-configured is refused", func(t *testing.T) {
		x := require.New(t)

		cert, _ := keypair(t)
		c := config.TlsConfig{ClientCA: fileRef(t, cert)}
		x.True(c.Active())

		_, err := c.Credentials()
		x.ErrorContains(err, "cert")
		x.ErrorContains(err, "key")
	})

	// Named by its key and not by what it holds, which a refusal would
	// otherwise put in a log: the value next to it in the file is a private
	// key, and the next refusal written by copying this one might be about
	// that one.
	t.Run("a bundle with no certificate in it is refused, and not repeated", func(t *testing.T) {
		x := require.New(t)

		cert, key := keypair(t)
		const junk = "this is not a certificate"
		for _, ca := range []ref.String{
			literal(t, junk),
			fileRef(t, write(t, t.TempDir(), "ca.pem", junk+"\n")),
		} {
			_, err := config.TlsConfig{Cert: fileRef(t, cert), Key: fileRef(t, key), ClientCA: ca}.Credentials()
			x.EqualError(err, "tls.client_ca: no certificate in it")
		}
	})
	t.Run("a bundle nobody wrote is refused", func(t *testing.T) {
		x := require.New(t)

		cert, key := keypair(t)
		_, err := config.TlsConfig{Cert: fileRef(t, cert), Key: fileRef(t, key), ClientCA: fileRef(t, "/nowhere/ca.pem")}.Credentials()
		x.ErrorContains(err, "tls.client_ca")
	})
	t.Run("a pair that is not one is refused without repeating either", func(t *testing.T) {
		x := require.New(t)

		cert, _ := keypair(t)
		_, other := keypair(t)
		pem := read(t, other)
		_, err := config.TlsConfig{Cert: fileRef(t, cert), Key: literal(t, pem)}.Credentials()
		x.ErrorContains(err, "tls.cert and tls.key")
		x.NotContains(err.Error(), "PRIVATE KEY")
		x.NotContains(err.Error(), strings.Split(pem, "\n")[1])
	})
}

// TestTlsIsAValueOfTheFile is the block as an app's file writes it, read by
// what reads an app's file, and served: `${file:}` like every other secret, or
// the PEM itself.
func TestTlsIsAValueOfTheFile(t *testing.T) {
	cert, key := keypair(t)

	load := func(t *testing.T, tlsBlock string) (App, error) {
		var c App
		p := write(t, t.TempDir(), "acme.yaml", "server:\n  tls:\n"+tlsBlock)
		_, err := cfg.New("acme", &c).Load(p, nil)
		return c, err
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{"as references", "" +
			"    cert: ${file:" + cert + "}\n" +
			"    key: ${file:" + key + "}\n"},
		{"as the PEM", "" +
			"    cert: " + indent(read(t, cert), 4) + "\n" +
			"    key: " + indent(read(t, key), 4) + "\n"},
	} {
		t.Run(tc.name+" a server completes a handshake", func(t *testing.T) {
			x := require.New(t)

			c, err := load(t, tc.body+"    client_ca: ${file:"+cert+"}\n")
			x.NoError(err)

			s, err := c.Server.Tls.Credentials()
			x.NoError(err)

			pair, err := tls.LoadX509KeyPair(cert, key)
			x.NoError(err)

			// The client is payday's own dial block, said the same way.
			var d struct {
				Tls config.DialConfig `yaml:"tls"`
			}
			p := write(t, t.TempDir(), "dial.yaml", "tls:\n"+tc.body+"    ca: ${file:"+cert+"}\n")
			_, err = cfg.New("acme", &d).Load(p, nil)
			x.NoError(err)

			creds, err := d.Tls.Credentials()
			x.NoError(err)

			got, err := handshake(t, s, creds)
			x.NoError(err)
			x.True(got.HandshakeComplete)
			x.Len(got.PeerCertificates, 1, "the client presented the certificate its block holds")
			x.Equal(pair.Certificate[0], got.PeerCertificates[0].Raw)
		})
	}

	// What `<app> config` prints is pasted into tickets: a key written in is
	// left out of it by its name, and the certificate beside it, which is no
	// secret, is shown.
	t.Run("config prints the certificate and not the key", func(t *testing.T) {
		x := require.New(t)

		var c App
		p := write(t, t.TempDir(), "acme.yaml", "server:\n  tls:\n"+
			"    cert: "+indent(read(t, cert), 4)+"\n"+
			"    key: "+indent(read(t, key), 4)+"\n")
		s, err := cfg.New("acme", &c).Load(p, nil)
		x.NoError(err)

		b := &strings.Builder{}
		x.NoError(s.Print(b))
		x.Contains(b.String(), "BEGIN CERTIFICATE")
		x.NotContains(b.String(), "PRIVATE KEY")
		x.NotContains(b.String(), strings.Split(read(t, key), "\n")[1])
	})

	// The keys these replaced are refused as any key nothing reads is, by the
	// loader, before anything is served: a file that still says `cert_file`
	// is a deployment that meant TLS, and the other outcome is plaintext.
	t.Run("the keys these replaced are refused", func(t *testing.T) {
		x := require.New(t)

		_, err := load(t, "    cert_file: "+cert+"\n    key_file: "+key+"\n")
		x.ErrorContains(err, "cert_file")
		x.ErrorContains(err, "nothing reads this key")

		var d struct {
			Tls config.DialConfig `yaml:"tls"`
		}
		p := write(t, t.TempDir(), "dial.yaml", "tls:\n    ca_file: "+cert+"\n")
		_, err = cfg.New("acme", &d).Load(p, nil)
		x.ErrorContains(err, "ca_file")
		x.ErrorContains(err, "nothing reads this key")
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

		got, err := config.TlsConfig{}.Server()
		x.NoError(err)
		x.Nil(got, "a plaintext listener has to be able to tell")
	})

	t.Run("a certificate and a client ca", func(t *testing.T) {
		x := require.New(t)

		got, err := config.TlsConfig{
			Cert:     fileRef(t, cert),
			Key:      fileRef(t, key),
			ClientCA: fileRef(t, cert),
		}.Server()
		x.NoError(err)
		x.NotNil(got)
		x.Len(got.Certificates, 1)
		x.NotNil(got.ClientCAs)
		x.Equal(tls.RequireAndVerifyClientCert, got.ClientAuth)
	})

	t.Run("and a caller that may arrive without one", func(t *testing.T) {
		x := require.New(t)

		got, err := config.TlsConfig{
			Cert:               fileRef(t, cert),
			Key:                fileRef(t, key),
			ClientCA:           fileRef(t, cert),
			ClientCertOptional: true,
		}.Server()
		x.NoError(err)
		x.Equal(tls.VerifyClientCertIfGiven, got.ClientAuth)
	})

	t.Run("half a key pair is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := config.TlsConfig{Cert: fileRef(t, cert)}.Server()
		x.Error(err)
	})
}
