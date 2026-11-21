package cmd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/config"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Use serveHttp itself: httptest.StartTLS would add the missing handshake for
// the app and pass even when the listener the app actually opened was plaintext.
func TestHTTPListenerUsesConfiguredTLS(t *testing.T) {
	cert, key, pair, roots := httpCertificate(t)
	_, _, stranger, _ := httpCertificate(t)
	for _, tc := range []struct {
		name string
		tls  config.TlsConfig
	}{
		{"plaintext", config.TlsConfig{}},
		{"TLS", config.TlsConfig{CertFile: cert, KeyFile: key}},
		{"required client", config.TlsConfig{CertFile: cert, KeyFile: key, ClientCAFile: cert}},
		{"optional client", config.TlsConfig{CertFile: cert, KeyFile: key, ClientCAFile: cert, ClientCertOptional: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			g := grpc.NewServer()
			healthpb.RegisterHealthServer(g, health.NewServer())
			t.Cleanup(g.Stop)
			var said bytes.Buffer
			ctx := log.Into(t.Context(), slog.New(slog.NewJSONHandler(&said, nil)))
			c := Config{Server: config.ServerConfig{
				Tls: tc.tls, Http: config.HttpConfig{Addr: "127.0.0.1:0", AllowWeb: true},
			}}
			stop, err := (&Server{}).serveHttp(ctx, c, g)
			x.NoError(err)
			t.Cleanup(stop)
			var record struct{ Addr string }
			x.NoError(json.Unmarshal(said.Bytes(), &record))
			x.NotEmpty(record.Addr)
			endpoint := "http://" + record.Addr
			cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			if tc.tls.Active() {
				endpoint = "https://" + record.Addr
				cfg.Certificates = []tls.Certificate{pair}
			}
			transport := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			check := func(client *http.Client, base string) (*http.Response, error) {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
					base+"/grpc.health.v1.Health/Check", strings.NewReader(`{}`))
				x.NoError(err)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Connect-Protocol-Version", "1")
				return client.Do(req)
			}
			res, err := check(client, endpoint)
			x.NoError(err)
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			x.NoError(err)
			x.Equal(http.StatusOK, res.StatusCode, string(body))
			x.Contains(string(body), "SERVING")
			if !tc.tls.Active() {
				x.Nil(res.TLS)
				return
			}
			x.Equal(2, res.ProtoMajor, "ServeTLS must negotiate HTTP/2 through ALPN")
			x.NotNil(res.TLS)

			// Native gRPC must use this very HTTP port, not the first listener.
			cc, err := grpc.NewClient(record.Addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
			x.NoError(err)
			t.Cleanup(func() { cc.Close() })
			got, err := healthpb.NewHealthClient(cc).Check(t.Context(), &healthpb.HealthCheckRequest{})
			x.NoError(err)
			x.Equal(healthpb.HealthCheckResponse_SERVING, got.GetStatus())

			res, err = check(client, "http://"+record.Addr)
			x.NoError(err)
			res.Body.Close()
			x.Equal(http.StatusBadRequest, res.StatusCode, "plaintext must not reach the RPC")

			if tc.tls.ClientCAFile == "" {
				return
			}
			for _, identity := range []struct {
				certs []tls.Certificate
				ok    bool
			}{
				{nil, tc.tls.ClientCertOptional},
				{[]tls.Certificate{stranger}, false},
			} {
				peer := cfg.Clone()
				peer.Certificates = identity.certs
				// Offer even the untrusted pair, so optional mTLS checks what was
				// presented instead of silently treating this client as anonymous.
				if len(identity.certs) > 0 {
					peer.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
						return &identity.certs[0], nil
					}
				}
				tr := &http.Transport{TLSClientConfig: peer, ForceAttemptHTTP2: true}
				t.Cleanup(tr.CloseIdleConnections)
				res, err := check(&http.Client{Transport: tr, Timeout: 5 * time.Second}, endpoint)
				if identity.ok {
					x.NoError(err)
					res.Body.Close()
					x.Equal(http.StatusOK, res.StatusCode)
				} else {
					x.Error(err, "client certificate policy was not enforced")
					if res != nil {
						res.Body.Close()
					}
				}
			}
		})
	}
}

func TestHTTPListenerRejectsBrokenTLSBeforeListening(t *testing.T) {
	for _, cfg := range []config.TlsConfig{
		{Enabled: true}, {CertFile: "missing.pem", KeyFile: "missing.key"}, {ClientCAFile: "missing.pem"},
	} {
		g := grpc.NewServer()
		stop, err := (&Server{}).serveHttp(t.Context(), Config{Server: config.ServerConfig{
			Tls: cfg, Http: config.HttpConfig{Addr: "127.0.0.1:0"},
		}}, g)
		if stop != nil {
			stop()
		}
		g.Stop()
		require.Error(t, err)
	}
}

func httpCertificate(t *testing.T) (string, string, tls.Certificate, *x509.CertPool) {
	t.Helper()
	x := require.New(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, priv)
	x.NoError(err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	x.NoError(err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, key := filepath.Join(t.TempDir(), "cert.pem"), filepath.Join(t.TempDir(), "key.pem")
	x.NoError(os.WriteFile(cert, certPEM, 0600))
	x.NoError(os.WriteFile(key, keyPEM, 0600))
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	x.NoError(err)
	roots := x509.NewCertPool()
	x.True(roots.AppendCertsFromPEM(certPEM))
	return cert, key, pair, roots
}
