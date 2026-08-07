package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type TLSConfig struct {
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

// Active reports whether TLS should be used for the server.
//
// A client CA bundle counts, and that is not obvious: it names nothing the
// server presents, so a configuration holding only that one looks like a
// configuration that said nothing about TLS. It is not -- there is no way to
// verify a client certificate over a connection that has no handshake, so a
// server told to check them and nothing else is a server that would answer
// "nobody said anything" about every caller, for its whole life, with mutual
// TLS written down in its file. Counting it here turns that into
// [TLSConfig.Credentials] refusing to build, which is a server that does not
// start.
func (c TLSConfig) Active() bool {
	return c.Enabled || c.CertFile != "" || c.KeyFile != "" || c.ClientCAFile != ""
}

// Credentials builds gRPC transport credentials from the TLS configuration.
// It returns insecure credentials when TLS is not configured, so the caller
// can pass the result to grpc.Creds unconditionally.
func (c TLSConfig) Credentials() (credentials.TransportCredentials, error) {
	if !c.Active() {
		return insecure.NewCredentials(), nil
	}
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, fmt.Errorf("both cert_file and key_file must be set to enable TLS")
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

	return credentials.NewTLS(cfg), nil
}
