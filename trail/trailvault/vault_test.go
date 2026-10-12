package trailvault

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/trail/internal/signed"
)

// quiet is an environment that says nothing of Vault, so that a desk's own
// VAULT_ADDR is not what a test reads.
func quiet(t *testing.T) {
	for _, k := range []string{"VAULT_ADDR", "VAULT_TOKEN", "VAULT_NAMESPACE", "VAULT_CACERT", "TRANSIT_SECRET_ENGINE_PATH"} {
		t.Setenv(k, "")
	}
}

// TestAURINamesAKeyAndTheEnvironmentTheRest: what Vault's CLI reads is read,
// and what the URI says wins.
func TestAURINamesAKeyAndTheEnvironmentTheRest(t *testing.T) {
	x := require.New(t)
	quiet(t)

	t.Setenv("VAULT_ADDR", "https://vault.internal:8200/")
	t.Setenv("VAULT_TOKEN", "hvs.env")
	t.Setenv("VAULT_NAMESPACE", "team")
	t.Setenv("TRANSIT_SECRET_ENGINE_PATH", "/signing/")
	u, err := ParseURI("hashivault://checkpoints")
	x.NoError(err)
	x.Equal(&URI{Key: "checkpoints", Addr: "https://vault.internal:8200", Mount: "signing", Namespace: "team", Token: "hvs.env"}, u)

	u, err = ParseURI("HashiVault://checkpoints?addr=http://127.0.0.1:8100&mount=team/transit&namespace=&version=3&token-file=/run/vault/token&ca-cert=/etc/ca.pem")
	x.NoError(err)
	x.Equal(&URI{Key: "checkpoints", Addr: "http://127.0.0.1:8100", Mount: "team/transit", Namespace: "", CACert: "/etc/ca.pem", Version: 3, TokenFile: "/run/vault/token"}, u,
		"a token file is the token, not the environment's")

	quiet(t)
	u, err = ParseURI("hashivault://checkpoints?addr=https://vault:8200")
	x.NoError(err)
	x.Equal("transit", u.Mount)

	for v, want := range map[string]string{
		"hashivault://checkpoints":                               "VAULT_ADDR",
		"hashivault://checkpoints?addr=vault:8200":               "http",
		"hashivault://root:hvs.x@checkpoints?addr=https://v":     "VAULT_TOKEN",
		"hashivault://transit/keys/checkpoints?addr=https://v":   "nothing else",
		"hashivault://?addr=https://v":                           "nothing else",
		"hashivault://checkpoints?addr=https://v&version=0":      "version",
		"hashivault://checkpoints?addr=https://v&mont=transit":   "mont",
		"hashivault://checkpoints?addr=https://v&addr=https://w": "said twice",
		"vault://checkpoints?addr=https://v":                     "hashivault://",
	} {
		_, err := ParseURI(v)
		x.ErrorContains(err, want, v)
		x.NotContains(err.Error(), "hvs.x")
	}
}

// fake is Transit as far as this package can tell, over a key of its own, for
// what a real Vault is not easily made to do.
type fake struct {
	mu        sync.Mutex
	key       crypto.Signer
	kind      string
	token     string
	namespace string
	answer    string // a version prefix to answer with instead of the right one
	asked     map[string]any
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []string{msg}})
	}
	if r.Header.Get("X-Vault-Token") != f.token {
		fail(http.StatusForbidden, "permission denied")
		return
	}
	if r.Header.Get("X-Vault-Namespace") != f.namespace {
		fail(http.StatusNotFound, "no handler for route")
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/transit/keys/k":
		der, _ := x509.MarshalPKIXPublicKey(f.key.Public())
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"type": f.kind, "latest_version": 1, "supports_signing": true,
			"keys": map[string]any{"1": map[string]any{"public_key": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}},
		}})

	case r.Method == http.MethodPost && r.URL.Path == "/v1/transit/sign/k":
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.asked = req
		in, _ := base64.StdEncoding.DecodeString(req["input"].(string))
		sig, err := f.key.Sign(rand.Reader, in, crypto.SHA256)
		if err != nil {
			fail(http.StatusBadRequest, err.Error())
			return
		}
		prefix := "vault:v1:"
		if f.answer != "" {
			prefix = f.answer
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"signature": prefix + base64.StdEncoding.EncodeToString(sig), "key_version": 1}})

	default:
		fail(http.StatusNotFound, "no handler for route")
	}
}

func newFake(t *testing.T) *fake {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	return &fake{key: k, kind: "ecdsa-p256", token: "hvs.a"}
}

// TestATokenFileIsReadAtEveryRequest, as a Vault Agent sink is renewed and
// replaced under a running process.
func TestATokenFileIsReadAtEveryRequest(t *testing.T) {
	x := require.New(t)
	quiet(t)

	f := newFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()

	tok := filepath.Join(t.TempDir(), "token")
	x.NoError(os.WriteFile(tok, []byte("hvs.a\n"), 0o600))
	s, err := Open("hashivault://k?addr=" + srv.URL + "&token-file=" + tok)
	x.NoError(err)
	x.NoError(trail.TrySigner(s))

	f.mu.Lock()
	f.token = "hvs.b"
	f.mu.Unlock()
	x.NoError(os.WriteFile(tok, []byte("hvs.b\n"), 0o600))
	x.NoError(trail.TrySigner(s), "the agent's new token")

	x.NoError(os.WriteFile(tok, []byte("hvs.expired\n"), 0o600))
	err = trail.TrySigner(s)
	x.ErrorContains(err, "403: permission denied")
	x.NotContains(err.Error(), "hvs.")
}

// TestWhatIsAskedIsWhatTheKeyIs: a digest, prehashed, ASN.1 for ECDSA, in the
// namespace, of the version opened.
func TestWhatIsAskedIsWhatTheKeyIs(t *testing.T) {
	x := require.New(t)
	quiet(t)

	f := newFake(t)
	f.namespace = "team"
	srv := httptest.NewServer(f)
	defer srv.Close()

	t.Setenv("VAULT_TOKEN", "hvs.a")
	_, err := Open("hashivault://k?addr=" + srv.URL)
	x.ErrorContains(err, "404", "outside the namespace")

	s, err := Open("hashivault://k?addr=" + srv.URL + "&namespace=team")
	x.NoError(err)
	x.NoError(trail.TrySigner(s))
	x.Equal(map[string]any{
		"input": f.asked["input"], "key_version": float64(1),
		"prehashed": true, "hash_algorithm": "sha2-256", "marshaling_algorithm": "asn1",
	}, f.asked)

	f.answer = "vault:v2:"
	x.ErrorContains(trail.TrySigner(s), "is not vault:v1's")
}

// TestOnlyTheCAItIsToldOfIsTrusted when it is told of one.
func TestOnlyTheCAItIsToldOfIsTrusted(t *testing.T) {
	x := require.New(t)
	quiet(t)

	f := newFake(t)
	srv := httptest.NewUnstartedServer(f)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the handshake refused, on purpose
	srv.StartTLS()
	defer srv.Close()
	t.Setenv("VAULT_TOKEN", "hvs.a")

	_, err := Open("hashivault://k?addr=" + srv.URL)
	x.ErrorContains(err, "certificate", "a CA nobody named")

	ca := filepath.Join(t.TempDir(), "ca.pem")
	x.NoError(os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	t.Setenv("VAULT_CACERT", ca)
	s, err := Open("hashivault://k?addr=" + srv.URL)
	x.NoError(err)
	x.NoError(trail.TrySigner(s))

	x.NoError(os.WriteFile(ca, []byte("not a certificate"), 0o600))
	_, err = Open("hashivault://k?addr=" + srv.URL)
	x.ErrorContains(err, "no certificate")
}

// A real one, as CI runs it:
//
//	docker run -d -p 8200:8200 -e VAULT_DEV_ROOT_TOKEN_ID=root hashicorp/vault
//	PDTEST_VAULT=http://127.0.0.1:8200 go test ./trail/trailvault/
//
// OpenBao is tried the same way. The token is `root` unless PDTEST_VAULT_TOKEN
// says otherwise; it enables Transit at `transit` and makes keys there.
const envVault = "PDTEST_VAULT"

type vault struct {
	t     *testing.T
	addr  string
	token string
}

func newVault(t *testing.T) *vault {
	t.Helper()
	quiet(t)

	addr := os.Getenv(envVault)
	if addr == "" {
		t.Skipf("%s is not set; these make keys in Vault's Transit", envVault)
	}
	token := os.Getenv("PDTEST_VAULT_TOKEN")
	if token == "" {
		token = "root"
	}

	v := &vault{t: t, addr: strings.TrimRight(addr, "/"), token: token}
	// Already enabled is a second test, or a second run.
	code, body := v.do(http.MethodPost, "sys/mounts/transit", map[string]any{"type": "transit"})
	if code/100 != 2 && !strings.Contains(body, "path is already in use") {
		t.Fatalf("enabling transit: %d %s", code, body)
	}
	t.Setenv("VAULT_ADDR", v.addr)
	t.Setenv("VAULT_TOKEN", v.token)

	return v
}

func (v *vault) do(method, path string, body any) (int, string) {
	v.t.Helper()

	var r *bytes.Reader
	if body == nil {
		r = bytes.NewReader(nil)
	} else {
		b, err := json.Marshal(body)
		require.NoError(v.t, err)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, v.addr+"/v1/"+path, r)
	require.NoError(v.t, err)
	req.Header.Set("X-Vault-Token", v.token)
	rsp, err := http.DefaultClient.Do(req)
	require.NoError(v.t, err)
	defer rsp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(rsp.Body)

	return rsp.StatusCode, buf.String()
}

func (v *vault) must(method, path string, body any) {
	v.t.Helper()
	if code, out := v.do(method, path, body); code/100 != 2 {
		v.t.Fatalf("%s %s: %d %s", method, path, code, out)
	}
}

// key makes a key of a test's own.
func (v *vault) key(kind string, extra map[string]any) string {
	v.t.Helper()

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := fmt.Sprintf("%s-%x", kind, b)
	body := map[string]any{"type": kind}
	for k, x := range extra {
		body[k] = x
	}
	v.must(http.MethodPost, "transit/keys/"+name, body)

	return name
}

// TestEachKindOfKeySignsACheckpointThatVerifies: what Transit answers is what a
// checkpoint's verifier reads, for each kind of key a checkpoint is signed
// with.
func TestEachKindOfKeySignsACheckpointThatVerifies(t *testing.T) {
	v := newVault(t)

	for _, kind := range []string{"ed25519", "ecdsa-p256", "ecdsa-p384", "ecdsa-p521", "rsa-2048", "rsa-3072", "rsa-4096"} {
		t.Run(kind, func(t *testing.T) {
			x := require.New(t)

			s, err := Open("hashivault://" + v.key(kind, nil))
			x.NoError(err)
			x.NoError(trail.TrySigner(s))

			if r, ok := s.Public().(*rsa.PublicKey); ok {
				d := sha256.Sum256([]byte("a checkpoint"))
				sig, err := s.Sign(rand.Reader, d[:], crypto.SHA256)
				x.NoError(err)
				x.NoError(rsa.VerifyPKCS1v15(r, crypto.SHA256, d[:], sig), "PKCS #1 v1.5, as asked")

				sig, err = s.Sign(rand.Reader, d[:], &rsa.PSSOptions{Hash: crypto.SHA256})
				x.NoError(err)
				x.NoError(rsa.VerifyPSS(r, crypto.SHA256, d[:], sig, nil))
				x.Error(rsa.VerifyPKCS1v15(r, crypto.SHA256, d[:], sig), "PSS, as asked")
			}
		})
	}
}

// TestAKeyTransitWouldHandOutIsRefused: exportable, or backed up in the clear,
// is a key the process's token -- or another's -- can take.
func TestAKeyTransitWouldHandOutIsRefused(t *testing.T) {
	x := require.New(t)
	v := newVault(t)

	_, err := Open("hashivault://" + v.key("ecdsa-p256", map[string]any{"exportable": true}))
	x.ErrorContains(err, "exportable")
	_, err = Open("hashivault://" + v.key("ecdsa-p256", map[string]any{"allow_plaintext_backup": true}))
	x.ErrorContains(err, "plaintext backup")
	_, err = Open("hashivault://" + v.key("aes256-gcm96", nil))
	x.ErrorContains(err, "does not sign")
	_, err = Open("hashivault://nothing-of-the-name")
	x.ErrorContains(err, "404")
}

// TestAKeyRotatedUnderAProcessSignsWithTheVersionItWasOpenedAt, and the next
// process with the next.
func TestAKeyRotatedUnderAProcessSignsWithTheVersionItWasOpenedAt(t *testing.T) {
	x := require.New(t)
	v := newVault(t)

	name := v.key("ecdsa-p256", nil)
	first, err := Open("hashivault://" + name)
	x.NoError(err)

	v.must(http.MethodPost, "transit/keys/"+name+"/rotate", nil)
	x.NoError(trail.TrySigner(first), "after the rotation, at version 1")

	second, err := Open("hashivault://" + name)
	x.NoError(err)
	x.NoError(trail.TrySigner(second))
	x.False(second.Public().(*ecdsa.PublicKey).Equal(first.Public()), "version 2")

	pinned, err := Open("hashivault://" + name + "?version=1")
	x.NoError(err)
	x.True(pinned.Public().(*ecdsa.PublicKey).Equal(first.Public()))
	_, err = Open("hashivault://" + name + "?version=3")
	x.ErrorContains(err, "no version 3")
}

// TestAKeyMadeAgainUnderItsNameIsRefused: deleted and made again, it is
// another key at version 1, and what it signs is refused rather than handed
// over as a checkpoint nobody can verify.
func TestAKeyMadeAgainUnderItsNameIsRefused(t *testing.T) {
	x := require.New(t)
	v := newVault(t)

	name := v.key("ecdsa-p256", nil)
	s, err := Open("hashivault://" + name)
	x.NoError(err)
	x.NoError(trail.TrySigner(s))

	v.must(http.MethodPost, "transit/keys/"+name+"/config", map[string]any{"deletion_allowed": true})
	v.must(http.MethodDelete, "transit/keys/"+name, nil)
	v.must(http.MethodPost, "transit/keys/"+name, map[string]any{"type": "ecdsa-p256"})

	d := sha256.Sum256([]byte("a checkpoint"))
	_, err = s.Sign(rand.Reader, d[:], crypto.SHA256)
	x.ErrorIs(err, signed.ErrMismatch)
}
