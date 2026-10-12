// Package trailvault signs the trail's checkpoints with a key in HashiCorp
// Vault's Transit secrets engine, or OpenBao's, which speaks the same API
// (#52).
//
// It is named in `audit.checkpoints.key` the way sigstore names one, once
// `config/signervault` is imported:
//
//	hashivault://checkpoints
//	hashivault://checkpoints?addr=https://vault.internal:8200&mount=transit&version=3
//
// What the URI does not say is read from Vault's own environment, as its CLI
// reads it: `VAULT_ADDR`, `VAULT_TOKEN`, `VAULT_NAMESPACE`, `VAULT_CACERT`,
// and `TRANSIT_SECRET_ENGINE_PATH` for the mount, `transit` when nothing says.
// The URI's `addr`, `mount`, `namespace` and `ca-cert` say the same and win.
//
// # The token
//
// A token in `VAULT_TOKEN` is read once. `token-file=<path>` is read at every
// request, which is what a Vault Agent sink is: the agent keeps a token there,
// renewed and replaced, and nothing here has to. Neither is a request with no
// token, which is right for an agent's API proxy that adds its own
// (`use_auto_auth_token`). A token that expires is a signer that stops, so a
// process that runs for longer than its token lives wants one of the two
// agents rather than a token of its own.
//
// # What signs
//
//   - `ed25519`, over the checkpoint itself, which is sent whole: within the
//     request Vault allows (`max_request_size`, 32 MiB unless told) the
//     checkpoint of an archive of hundreds of thousands of chunks;
//   - `ecdsa-p256`, `ecdsa-p384`, `ecdsa-p521`, over a SHA-256 digest,
//     answered in ASN.1;
//   - `rsa-2048`, `rsa-3072`, `rsa-4096`, PKCS #1 v1.5 over SHA-256, or PSS
//     when asked.
//
// The key's version is the one that was latest at Open, or `version=`, and
// every signature is asked of that version: a key rotated under a running
// process goes on signing with what it said it was until the process is
// started again, and then with the next, whose public half verifications are
// told of in `audit.checkpoints.trust`. Whatever Vault answers is verified
// against the public half before it is handed over.
//
// # What it cannot give away
//
// A key here is refused if Transit would hand it out: `exportable`, or
// `allow_plaintext_backup`. Neither can be unset once set, so such a key is
// one to replace. Whoever holds the process holds a token that can sign while
// they hold it -- give it a policy of `update` on `transit/sign/<key>` and
// `read` on `transit/keys/<key>`, and nothing else -- and not the key. Vault's
// operators, with its storage and its unseal keys, are another matter: that is
// the trust Vault is.
package trailvault

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lesomnus/payday/trail/internal/signed"
)

// Scheme is what `audit.checkpoints.key` begins with to name a key here.
const Scheme = "hashivault"

// Timeout is how long a request to Vault may take.
const Timeout = 30 * time.Second

// URI is a key as `audit.checkpoints.key` names it here, with what the
// environment says filled in.
type URI struct {
	Key       string
	Addr      string
	Mount     string
	Namespace string
	CACert    string
	Version   int
	Token     string
	TokenFile string
}

// ParseURI reads a key's URI and the environment around it. An error never
// carries a token.
func ParseURI(v string) (*URI, error) {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return nil, errors.New("not a URI")
	}
	if !strings.EqualFold(u.Scheme, Scheme) {
		return nil, fmt.Errorf("not a %s:// URI", Scheme)
	}
	if u.User != nil {
		return nil, errors.New("a token goes in VAULT_TOKEN or token-file=, not in the URI")
	}
	if u.Host == "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("name the key and nothing else: hashivault://<key>")
	}

	r := &URI{
		Key:       u.Host,
		Addr:      os.Getenv("VAULT_ADDR"),
		Mount:     os.Getenv("TRANSIT_SECRET_ENGINE_PATH"),
		Namespace: os.Getenv("VAULT_NAMESPACE"),
		CACert:    os.Getenv("VAULT_CACERT"),
		Token:     os.Getenv("VAULT_TOKEN"),
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("the query is not one")
	}
	for k, vs := range q {
		if len(vs) != 1 {
			return nil, fmt.Errorf("%s is said twice", k)
		}
		switch v := vs[0]; k {
		case "addr":
			r.Addr = v
		case "mount":
			r.Mount = v
		case "namespace":
			r.Namespace = v
		case "ca-cert":
			r.CACert = v
		case "token-file":
			r.TokenFile, r.Token = v, ""
		case "version":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("version=%s is not a key version", v)
			}
			r.Version = n
		default:
			return nil, fmt.Errorf("%s is not something a hashivault:// URI says; it says addr=, mount=, namespace=, ca-cert=, token-file= and version=", k)
		}
	}

	if r.Addr == "" {
		return nil, errors.New("no address: VAULT_ADDR, or addr=")
	}
	if a, err := url.Parse(r.Addr); err != nil || (a.Scheme != "https" && a.Scheme != "http") || a.Host == "" {
		return nil, errors.New("the address is not an http:// or https:// URL")
	}
	r.Addr = strings.TrimRight(r.Addr, "/")
	r.Mount = strings.Trim(r.Mount, "/")
	if r.Mount == "" {
		r.Mount = "transit"
	}

	return r, nil
}

// Signer is a key in Transit, at one version, which signs and does not give
// its private half out.
type Signer struct {
	u       *URI
	client  *http.Client
	kind    string
	version int
	pub     crypto.PublicKey
}

var _ crypto.Signer = (*Signer)(nil)

// Open answers with the key `uri` names, at the version it was latest at, or
// the one the URI says, with its public half read.
func Open(uri string) (*Signer, error) {
	u, err := ParseURI(uri)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: Timeout}
	if u.CACert != "" {
		pem, err := os.ReadFile(u.CACert)
		if err != nil {
			return nil, fmt.Errorf("the CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no certificate", u.CACert)
		}
		client.Transport = &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}
	}

	s := &Signer{u: u, client: client}
	if err := s.read(); err != nil {
		return nil, err
	}

	return s, nil
}

// key is what Transit says of a key.
type key struct {
	Type                 string `json:"type"`
	LatestVersion        int    `json:"latest_version"`
	Exportable           bool   `json:"exportable"`
	AllowPlaintextBackup bool   `json:"allow_plaintext_backup"`
	SupportsSigning      bool   `json:"supports_signing"`

	// By version: what an asymmetric key has, and for any other the time it
	// was made, which is not a thing to read until the type is known.
	Keys map[string]json.RawMessage `json:"keys"`
}

func (s *Signer) read() error {
	var k key
	if err := s.call(http.MethodGet, "keys", nil, &k); err != nil {
		return err
	}

	switch {
	case k.Exportable:
		return errors.New("the key is exportable: Transit would hand it to anybody whose token may ask, and a key here is for the process not to have. Make one that is not")
	case k.AllowPlaintextBackup:
		return errors.New("the key allows a plaintext backup, which is the key: Transit would hand it out. Make one that does not")
	case !k.SupportsSigning:
		return fmt.Errorf("a key of type %s, which does not sign", k.Type)
	}
	switch k.Type {
	case "ed25519", "ecdsa-p256", "ecdsa-p384", "ecdsa-p521", "rsa-2048", "rsa-3072", "rsa-4096":
	default:
		return fmt.Errorf("a key of type %s, and a checkpoint is signed with ed25519, ecdsa-p256, -p384, -p521 or rsa-2048 and up", k.Type)
	}

	v := s.u.Version
	if v == 0 {
		v = k.LatestVersion
	}
	var kv struct {
		PublicKey string `json:"public_key"`
	}
	raw, ok := k.Keys[strconv.Itoa(v)]
	if !ok {
		return fmt.Errorf("the key has no version %d it will sign with", v)
	}
	if err := json.Unmarshal(raw, &kv); err != nil {
		return fmt.Errorf("the key's version %d: %w", v, err)
	}

	var pub crypto.PublicKey
	if k.Type == "ed25519" {
		b, err := base64.StdEncoding.DecodeString(kv.PublicKey)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return errors.New("the key's public half is not an Ed25519 one")
		}
		pub = ed25519.PublicKey(b)
	} else {
		b, _ := pem.Decode([]byte(kv.PublicKey))
		if b == nil {
			return errors.New("the key's public half is not a PEM document")
		}
		var err error
		if pub, err = x509.ParsePKIXPublicKey(b.Bytes); err != nil {
			return fmt.Errorf("the key's public half: %w", err)
		}
	}

	s.kind, s.version, s.pub = k.Type, v, pub
	return nil
}

// Public is the key's public half at the version it signs with.
func (s *Signer) Public() crypto.PublicKey { return s.pub }

var hashes = map[crypto.Hash]string{
	crypto.SHA256: "sha2-256",
	crypto.SHA384: "sha2-384",
	crypto.SHA512: "sha2-512",
}

// Sign signs as [crypto.Signer] does: Ed25519 over the message itself, ECDSA
// over a digest answered in ASN.1, RSA with PKCS #1 v1.5 or, given
// [rsa.PSSOptions], PSS. What Vault answers is verified against
// [Signer.Public] before it is handed over.
func (s *Signer) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	h, err := signed.Hash(s.pub, digest, opts)
	if err != nil {
		return nil, err
	}

	req := map[string]any{
		"input":       base64.StdEncoding.EncodeToString(digest),
		"key_version": s.version,
	}
	if s.kind != "ed25519" {
		req["prehashed"] = true
		req["hash_algorithm"] = hashes[h]
	}
	if strings.HasPrefix(s.kind, "ecdsa-") {
		req["marshaling_algorithm"] = "asn1"
	}
	if strings.HasPrefix(s.kind, "rsa-") {
		req["signature_algorithm"] = "pkcs1v15"
		if _, ok := opts.(*rsa.PSSOptions); ok {
			req["signature_algorithm"] = "pss"
		}
	}

	var rsp struct {
		Signature string `json:"signature"`
	}
	if err := s.call(http.MethodPost, "sign", req, &rsp); err != nil {
		return nil, err
	}

	prefix := fmt.Sprintf("vault:v%d:", s.version)
	b64, ok := strings.CutPrefix(rsp.Signature, prefix)
	if !ok {
		return nil, fmt.Errorf("the signature Vault answered with is not %s's: %.12s…", strings.TrimSuffix(prefix, ":"), rsp.Signature)
	}
	sig, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("the signature Vault answered with is not base64")
	}
	if err := signed.Check(s.pub, digest, opts, sig); err != nil {
		return nil, err
	}

	return sig, nil
}

// Close has nothing to let go of.
func (s *Signer) Close() error { return nil }

// call asks Transit one thing about the key, and reads `data` from what it
// answers.
func (s *Signer) call(method, op string, body any, data any) error {
	token := s.u.Token
	if s.u.TokenFile != "" {
		b, err := os.ReadFile(s.u.TokenFile)
		if err != nil {
			return fmt.Errorf("token-file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}

	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, s.u.Addr+"/v1/"+s.u.Mount+"/"+op+"/"+url.PathEscape(s.u.Key), r)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Request", "true")
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if s.u.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", s.u.Namespace)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rsp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	defer rsp.Body.Close()

	var out struct {
		Data   json.RawMessage `json:"data"`
		Errors []string        `json:"errors"`
	}
	b, err := io.ReadAll(io.LimitReader(rsp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	if err := json.Unmarshal(b, &out); err != nil && rsp.StatusCode/100 == 2 {
		return fmt.Errorf("vault: %s %s answered what is not JSON", method, op)
	}
	if rsp.StatusCode/100 != 2 {
		if len(out.Errors) == 0 {
			out.Errors = []string{http.StatusText(rsp.StatusCode)}
		}
		return fmt.Errorf("vault: %s %s/%s/%s: %d: %s", method, s.u.Mount, op, s.u.Key, rsp.StatusCode, strings.Join(out.Errors, "; "))
	}
	if len(out.Data) == 0 {
		return fmt.Errorf("vault: %s %s answered no data", method, op)
	}

	return json.Unmarshal(out.Data, data)
}
