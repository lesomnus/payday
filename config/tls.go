package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type TlsConfig struct {
	// Enabled turns on TLS. It is implied by anything else in this block being
	// written down, so it is only worth saying to turn TLS on for a server
	// whose certificate is named somewhere else.
	Enabled bool `yaml:"enabled"`

	// CertFile is the path to the PEM-encoded server certificate.
	CertFile string `yaml:"cert_file"`
	// KeyFile is the path to the PEM-encoded server private key.
	KeyFile string `yaml:"key_file"`

	// ClientCAFile is the path to a PEM-encoded CA bundle used to verify
	// client certificates. Setting it enables mutual TLS.
	ClientCAFile string `yaml:"client_ca_file"`

	// ClientCertOptional lets a caller connect without a certificate. One that
	// presents a certificate is still verified against ClientCAFile, and one
	// that presents nothing is still a connection.
	//
	// It is for a server that accepts more than one way of saying who is
	// calling: with `auth.methods: [bearer, mtls]` and a certificate required,
	// a caller who has only a token is refused at the handshake, before the
	// method they meant to use was ever read. Leave it off for a server where
	// the certificate is the floor and everything else is said on top of it.
	ClientCertOptional bool `yaml:"client_cert_optional"`
}

// Active reports whether Tls should be used for the server.
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
	return c.Enabled || c.CertFile != "" || c.KeyFile != "" || c.ClientCAFile != ""
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
func (c TlsConfig) Server() (*tls.Config, error) {
	if !c.Active() {
		return nil, nil
	}
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, fmt.Errorf("both cert_file and key_file must be set to enable Tls")
	}

	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, z.Err(err, "load key pair")
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// Enable mutual TLS when a client CA bundle is provided.
	if c.ClientCAFile != "" {
		pem, err := os.ReadFile(c.ClientCAFile)
		if err != nil {
			return nil, z.Err(err, "read client ca")
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %q", c.ClientCAFile)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		if c.ClientCertOptional {
			cfg.ClientAuth = tls.VerifyClientCertIfGiven
		}
	}

	return cfg, nil
}

// Credentials builds gRPC transport credentials from the Tls configuration.
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

// DialConfig is Tls for a connection this app **makes**, which is the other
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

	// CAFile is a PEM bundle to verify the server against, instead of the
	// system roots. It is what a private CA needs, and naming it turns TLS on.
	CAFile string `yaml:"ca_file"`

	// CertFile and KeyFile are the certificate this client presents, for a
	// server that asks. Naming them turns TLS on.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`

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

// Active reports whether this connection should use Tls.
func (c DialConfig) Active() bool {
	return c.Enabled || c.CAFile != "" || c.CertFile != "" || c.KeyFile != ""
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
			slog.Warn("config: dialling without Tls; anything sent on this connection " +
				"is readable by whatever is between here and there")
		})

		return insecure.NewCredentials(), nil
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName}

	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, z.Err(err, "read ca")
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %q", c.CAFile)
		}

		cfg.RootCAs = pool
	}

	// One without the other is a configuration that cannot work, and saying so
	// here is better than a handshake failing against a server that asked.
	switch {
	case c.CertFile != "" && c.KeyFile == "", c.CertFile == "" && c.KeyFile != "":
		return nil, fmt.Errorf("both cert_file and key_file are needed to present a certificate")

	case c.CertFile != "":
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, z.Err(err, "load key pair")
		}

		cfg.Certificates = []tls.Certificate{cert}
	}

	return credentials.NewTLS(cfg), nil
}

var plainDialSaid sync.Once
