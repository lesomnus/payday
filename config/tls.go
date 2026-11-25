package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/lesomnus/xli/cfg/ref"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type TlsConfig struct {
	// Enabled turns on TLS. It is implied by anything else in this block being
	// written down, so it is only worth saying to turn TLS on for a server
	// whose certificate is named somewhere else.
	Enabled bool `yaml:"enabled"`

	// Cert and Key are the server's certificate and its private key, as PEM.
	//
	// They are values, written as every other value of a configuration that is
	// kept somewhere else: `${file:/path}`, `${env:NAME}`, or the PEM itself.
	// They used to be paths
	// (`cert_file`, `key_file`), which made a file say one thing two ways, and
	// an app copying the first way for a key of its own.
	//
	// They are read when the listener is built, and only then: a certificate
	// that is replaced on disk takes a restart, as it did when these were
	// paths. A [ref.String] written `${file:}` reads its file again when it
	// changed, so a server that asked for the certificate on every handshake
	// would pick a rotation up without one; that is not built.
	Cert ref.String `yaml:"cert"`
	Key  ref.String `yaml:"key"`

	// ClientCA is a PEM bundle used to verify client certificates, read as
	// Cert is, and of a file no more than 1 MiB is read -- room for a system's
	// whole bundle. Setting it enables mutual TLS.
	ClientCA ref.String `yaml:"client_ca"`

	// ClientCertOptional lets a caller connect without a certificate. One that
	// presents a certificate is still verified against ClientCA, and one
	// that presents nothing is still a connection.
	//
	// It is for a server that accepts more than one way of saying who is
	// calling: with `auth.methods: [bearer, mtls]` and a certificate required,
	// a caller who has only a token is refused at the handshake, before the
	// method they meant to use was ever read. Leave it off for a server where
	// the certificate is the floor and everything else is said on top of it.
	ClientCertOptional bool `yaml:"client_cert_optional"`
}

// Active reports whether TLS should be used for the server.
//
// A client CA bundle counts, and that is not obvious: it names nothing the
// server presents, so a configuration holding only that one looks like a
// configuration that said nothing about TLS. It is not -- there is no way to
// verify a client certificate over a connection that has no handshake, so a
// server told to check them and nothing else is a server that would answer
// "nobody said anything" about every caller, for its whole life, with mutual
// TLS written down in its file. Counting it here turns that into
// [TlsConfig.Credentials] refusing to build, which is a server that does not
// start.
func (c TlsConfig) Active() bool {
	return c.Enabled || !c.Cert.IsZero() || !c.Key.IsZero() || !c.ClientCA.IsZero()
}

// Server is what this configuration says, as a `*tls.Config` for a server.
//
// **Nil when nothing said anything**, which is the answer that keeps a caller
// from having to ask twice: `if cfg == nil` is a plaintext listener, and
// everything else is the handshake this block described.
//
// It is separate from [TlsConfig.Credentials] because not every listener an app
// opens is a gRPC one. A payday app that serves HTTP as well -- a transcoder, a
// sign-in, a port robots present certificates to -- would otherwise build the
// same mutual-TLS setup a second time by hand, and the second one is where the
// client CA is forgotten.
//
// An error names the key as the file writes it, under the `tls` every block of
// payday's that holds one calls it, and never what the key holds: one of them
// is a private key, and an error is something a deployment logs.
func (c TlsConfig) Server() (*tls.Config, error) {
	if !c.Active() {
		return nil, nil
	}
	if c.Cert.IsZero() || c.Key.IsZero() {
		return nil, errors.New("tls: both cert and key must be set")
	}

	cert, err := keyPair(c.Cert, c.Key)
	if err != nil {
		return nil, err
	}

	v := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// Enable mutual TLS when a client CA bundle is provided.
	if !c.ClientCA.IsZero() {
		pool, err := certPool("client_ca", c.ClientCA)
		if err != nil {
			return nil, err
		}
		v.ClientCAs = pool
		v.ClientAuth = tls.RequireAndVerifyClientCert
		if c.ClientCertOptional {
			v.ClientAuth = tls.VerifyClientCertIfGiven
		}
	}

	return v, nil
}

// Credentials builds gRPC transport credentials from the TLS configuration.
// It returns insecure credentials when TLS is not configured, so the caller
// can pass the result to grpc.Creds unconditionally.
func (c TlsConfig) Credentials() (credentials.TransportCredentials, error) {
	cfg, err := c.Server()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return insecure.NewCredentials(), nil
	}

	return credentials.NewTLS(cfg), nil
}

// DialConfig is TLS for a connection this app **makes**, which is the other
// half of [TlsConfig] and a different shape.
//
// A server proves who it is and decides whether to ask the same of a caller. A
// client checks that proof and decides whether to offer one. So the fields are
// not the same fields, and one struct doing both would have every deployment
// leaving half of them empty and wondering which half.
//
// # The zero value is not secure, and says so
//
// Nothing written down is a plaintext connection, which is right for a service
// reaching another one over a loopback in a checkout and wrong the moment
// either of them moves. Whatever a client sends on that connection -- an API
// key, a token, a password on its way to be checked -- is readable by anything
// between them.
//
// It is the default anyway, for the reason `auth.Plain` is: an app that cannot
// be run until certificates exist is an app nobody runs. What it is not is
// quiet -- see [DialConfig.Credentials].
type DialConfig struct {
	// Enabled turns TLS on with the system roots, which is what a public
	// certificate authority needs and all it needs.
	Enabled bool `yaml:"enabled"`

	// Ca is a PEM bundle to verify the server against, instead of the system
	// roots. It is what a private CA needs, and writing it turns TLS on. Of a
	// file no more than 1 MiB is read, which a system's whole bundle fits in.
	//
	// It and Cert and Key are values, read as [TlsConfig.Cert] is and when it
	// is: when the connection's credentials are built, so a certificate that
	// is replaced takes a restart.
	Ca ref.String `yaml:"ca"`

	// Cert and Key are the certificate this client presents, for a server
	// that asks. Writing them turns TLS on.
	Cert ref.String `yaml:"cert"`
	Key  ref.String `yaml:"key"`

	// ServerName is the name to verify the server's certificate against, when
	// it is not the one in the address -- a connection made to a pod's IP, or
	// through a tunnel.
	//
	// It is **not** a way to skip verification. There is deliberately no field
	// for that: a client that does not check the certificate is a client that
	// cannot tell the server from whoever answered, which is the whole of what
	// TLS was for.
	ServerName string `yaml:"server_name"`
}

// Active reports whether this connection should use TLS.
func (c DialConfig) Active() bool {
	return c.Enabled || !c.Ca.IsZero() || !c.Cert.IsZero() || !c.Key.IsZero()
}

// Credentials is what to dial with, and insecure credentials when nothing was
// configured -- so a caller passes the result to `grpc.WithTransportCredentials`
// without asking.
//
// The plaintext case warns, once per process, for the reason [auth.Plain] does:
// the way it goes wrong is silence. A deployment sending an API key over a
// cleartext connection between two machines has given the key away, and nothing
// about it looks unusual -- it connects, it answers, the tests pass.
func (c DialConfig) Credentials() (credentials.TransportCredentials, error) {
	if !c.Active() {
		plainDialSaid.Do(func() {
			slog.Warn("config: dialling without TLS; anything sent on this connection " +
				"is readable by whatever is between here and there")
		})

		return insecure.NewCredentials(), nil
	}

	v := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName}

	if !c.Ca.IsZero() {
		pool, err := certPool("ca", c.Ca)
		if err != nil {
			return nil, err
		}

		v.RootCAs = pool
	}

	// One without the other is a configuration that cannot work, and saying so
	// here is better than a handshake failing against a server that asked.
	switch {
	case c.Cert.IsZero() != c.Key.IsZero():
		return nil, errors.New("tls: both cert and key are needed to present a certificate")

	case !c.Cert.IsZero():
		cert, err := keyPair(c.Cert, c.Key)
		if err != nil {
			return nil, err
		}

		v.Certificates = []tls.Certificate{cert}
	}

	return credentials.NewTLS(v), nil
}

var plainDialSaid sync.Once

// keyPair reads a certificate and its key. What `crypto/tls` says of a pair it
// cannot use names no part of either, so it is passed on as it is.
func keyPair(cert ref.String, key ref.String) (tls.Certificate, error) {
	c, err := cert.Value()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tls.cert: %w", err)
	}
	k, err := key.Value()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tls.key: %w", err)
	}

	v, err := tls.X509KeyPair([]byte(c), []byte(k))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tls.cert and tls.key: %w", err)
	}

	return v, nil
}

// certPool reads a bundle of certificates, refused when it holds none: a pool
// that is empty verifies nobody, which as a client CA is every caller refused
// and as a CA is every server.
func certPool(name string, bundle ref.String) (*x509.CertPool, error) {
	pem, err := bundle.Value()
	if err != nil {
		return nil, fmt.Errorf("tls.%s: %w", name, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		return nil, fmt.Errorf("tls.%s: no certificate in it", name)
	}

	return pool, nil
}
