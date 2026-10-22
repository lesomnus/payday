// Package witnessrekor is a witness that logs every checkpoint to a Rekor
// transparency log (payday#115; see trail.Witness): one of the deployment's
// own, or -- by the operator's choice, and see payday's runtime docs first --
// the public instance Sigstore runs.
//
// # What goes to the log
//
// A `hashedrekord` entry: the statement's digest, a signature over it by the
// deployment's key, and the public half of that key -- and never the
// statement, which names the tenants whose chunks the archive held. So the
// entries say nothing of a tenant, and nothing but that the deployment took a
// checkpoint, and when. A signature over a digest is the statement's own for
// an ECDSA or RSA key, which signs the SHA-256 of what it signs, and the
// deployment's key once more for one that does not (trail.Prehashed): Ed25519
// as Ed25519ph, which RFC 8032's domain separation keeps apart from the pure
// Ed25519 that signs the statement (section 5.1), so that neither signature
// stands for the other.
//
// What the log answers -- where the entry is, the proof that it is in the
// tree, and the tree's head the log signed -- is the receipt the trail keeps
// beside the checkpoint.
//
// # What a verification asks of it
//
// An entry carries no number, so Latest has nothing to answer
// (trail.ErrDeaf), and the witness holds the chain to itself instead
// (trail.Checker): every receipt is the entry of its checkpoint, proved in
// the tree the log signed when it answered, and that tree is one the log's
// tree now goes on from; and on a log that can be searched (v1), every entry
// the deployment's key signed is a checkpoint the store keeps -- one that is
// not is the head cut short, or a count wound back. A log that cannot be
// asked is a witness that did not answer.
//
// v2 logs (Rekor on tiles) cannot be searched: a receipt deleted with its
// checkpoint at the head is nothing the deployment can see, and the head is
// the operator's monitor of the log to watch; see payday's runtime docs.
package witnessrekor

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
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
	"sync"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/lesomnus/payday/trail"
)

// Scheme is what `audit.checkpoints.witnesses` names one by: `rekor:`, the
// log's URL, `key=` the PEM file of its key, and `v=2` for a log on tiles --
// `rekor:https://rekor.internal.example?v=2&key=/run/secrets/rekor.pub`.
const Scheme = "rekor"

// Witness logs what it hears to a Rekor log.
type Witness struct {
	base   string
	v2     bool
	origin string
	log    crypto.PublicKey
	http   *http.Client

	// entries is the entries of v1's search read before, by UUID: an entry
	// is what it is once it is in the log. proved is the heads a process has
	// proved one goes on from another.
	entries sync.Map
	proved  sync.Map
}

var (
	_ trail.Witness   = (*Witness)(nil)
	_ trail.Prehashed = (*Witness)(nil)
	_ trail.Checker   = (*Witness)(nil)
)

// Config is a log, in code.
type Config struct {
	// URL is where the log is: for a v2 log the base it serves its write API
	// and its tiles under.
	URL string

	// V2 is a log on tiles, Rekor v2, which cannot be searched.
	V2 bool

	// Key is the log's public key, which signs its trees' heads. It is the
	// deployment's to know -- a log that answered with its own would be
	// believed for whatever it said -- so it is given, never asked for.
	Key crypto.PublicKey

	// Origin is the name the log signs its trees' heads under: its host when
	// empty.
	Origin string

	// Client is how it is asked; a client with a timeout of its own when nil.
	Client *http.Client
}

// New is a witness that logs to `c`.
func New(c Config) (*Witness, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%q is no log's URL", c.URL)
	}
	if c.Key == nil {
		return nil, errors.New("no key of the log's: its trees' heads would be believed for whatever they said")
	}
	if _, err := keyHash(c.Origin, c.Key); err != nil {
		return nil, err
	}

	w := &Witness{base: strings.TrimSuffix(c.URL, "/"), v2: c.V2, origin: c.Origin, log: c.Key, http: c.Client}
	if w.origin == "" {
		w.origin = u.Host
	}
	if w.http == nil {
		w.http = &http.Client{Timeout: 30 * time.Second}
	}

	return w, nil
}

// Open is the witness `rekor:<url>` names, for config's `RegisterWitness`:
// `key=` the PEM file of the log's public key, `v=2` a log on tiles, and
// `origin=` the name its heads are signed under when it is not its host.
func Open(v string) (*Witness, error) {
	raw, ok := strings.CutPrefix(v, Scheme+":")
	if !ok {
		return nil, fmt.Errorf("%s: names no %s witness", v, Scheme)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("rekor: %w", err)
	}
	q := u.Query()
	u.RawQuery = ""

	path := q.Get("key")
	if path == "" {
		return nil, errors.New("rekor: key= names the PEM file of the log's public key, which signs its trees' heads")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rekor: the log's key: %w", err)
	}
	k, err := parsePublicKey(b)
	if err != nil {
		return nil, fmt.Errorf("rekor: the log's key: %w", err)
	}

	switch q.Get("v") {
	case "", "1":
	case "2":
	default:
		return nil, fmt.Errorf("rekor: v=%s, and a log is v=1 or v=2", q.Get("v"))
	}

	return New(Config{URL: u.String(), V2: q.Get("v") == "2", Key: k, Origin: q.Get("origin")})
}

func parsePublicKey(b []byte) (crypto.PublicKey, error) {
	if p, _ := pem.Decode(b); p != nil {
		b = p.Bytes
	}

	return x509.ParsePKIXPublicKey(b)
}

// Prehash is SHA-512 for an Ed25519 key, whose signature over it is
// Ed25519ph, and for ECDSA the curve's own hash on a v2 log, which holds a key
// to it; SHA-256 otherwise, which an ECDSA or RSA checkpoint is signed over
// already.
func (w *Witness) Prehash(key crypto.PublicKey) crypto.Hash {
	switch k := key.(type) {
	case ed25519.PublicKey:
		return crypto.SHA512
	case *ecdsa.PublicKey:
		if w.v2 {
			switch k.Curve {
			case elliptic.P384():
				return crypto.SHA384
			case elliptic.P521():
				return crypto.SHA512
			}
		}
	}

	return crypto.SHA256
}

// Latest is trail.ErrDeaf: an entry carries no number. See Check.
func (*Witness) Latest(context.Context, string) (trail.Heard, error) {
	return trail.Heard{}, trail.ErrDeaf
}

// receipt is what Hear answers, kept beside the checkpoint: the log's
// version, and the entry as it answered with it.
type receipt struct {
	V     int             `json:"v"`
	UUID  string          `json:"uuid,omitempty"`
	Entry json.RawMessage `json:"entry"`
}

// Hear logs a statement's digest, signed, and answers where the entry is.
// A tenant's chain is not logged here: what a public log keeps of a tenant
// says it exists and when it was active, and a tenant's statements go to a
// witness of its own.
func (w *Witness) Hear(ctx context.Context, s trail.Signed) (trail.Witnessed, error) {
	if s.Chain != "" {
		return trail.Witnessed{}, errors.New("a tenant's chain is not told to a log the deployment names; a tenant's witness is its own")
	}
	if s.Prehash == 0 || len(s.PrehashSig) == 0 {
		return trail.Witnessed{}, errors.New("no signature over the statement's digest to log")
	}

	h := s.Prehash.New()
	h.Write(s.Statement)
	digest := h.Sum(nil)

	var r receipt
	var err error
	if w.v2 {
		r, err = w.addV2(ctx, s, digest)
	} else {
		r, err = w.addV1(ctx, s, digest)
	}
	if err != nil {
		return trail.Witnessed{}, err
	}

	b, err := json.Marshal(r)
	if err != nil {
		return trail.Witnessed{}, err
	}

	return trail.Witnessed{Receipt: b}, nil
}

// entry is an entry as a receipt says it was answered, read for what a
// check of it needs.
type entry struct {
	index int64 // in the tree the proof is of
	body  []byte
	proof proof
}

// proof is an inclusion proof, and the head of the tree it is of as the log
// signed it.
type proof struct {
	index, size int64
	root        []byte
	hashes      [][]byte
	head        string
}

// head is a tree's head, as the log signed it.
type head struct {
	origin string
	size   int64
	root   []byte
}

// readHead verifies a tree's head the log signed, and reads it: its origin,
// its size and its root.
func (w *Witness) readHead(signed string) (head, error) {
	v, err := w.verifier()
	if err != nil {
		return head{}, err
	}

	n, err := note.Open([]byte(signed), note.VerifierList(v))
	if err != nil {
		return head{}, fmt.Errorf("the tree's head is not one the log's key signed: %w", err)
	}

	lines := strings.Split(n.Text, "\n")
	if len(lines) < 4 {
		return head{}, errors.New("a tree's head that says no tree")
	}
	size, err := strconv.ParseInt(lines[1], 10, 64)
	if err != nil {
		return head{}, fmt.Errorf("a tree's head whose size does not read: %w", err)
	}
	root, err := base64.StdEncoding.DecodeString(lines[2])
	if err != nil {
		return head{}, fmt.Errorf("a tree's head whose root does not read: %w", err)
	}

	return head{origin: lines[0], size: size, root: root}, nil
}

// verifier is the log's key as a signed note's: the name its heads are signed
// under, its key's hash, and the signature over the note.
func (w *Witness) verifier() (note.Verifier, error) {
	h, err := keyHash(w.origin, w.log)
	if err != nil {
		return nil, err
	}

	return logVerifier{name: w.origin, hash: h, key: w.log}, nil
}

type logVerifier struct {
	name string
	hash uint32
	key  crypto.PublicKey
}

func (v logVerifier) Name() string    { return v.name }
func (v logVerifier) KeyHash() uint32 { return v.hash }

func (v logVerifier) Verify(msg, sig []byte) bool {
	switch k := v.key.(type) {
	case ed25519.PublicKey:
		return ed25519.Verify(k, msg, sig)
	case *ecdsa.PublicKey:
		h := hashOf(k.Curve).New()
		h.Write(msg)
		return ecdsa.VerifyASN1(k, h.Sum(nil), sig)
	}

	return false
}

// hashOf is the hash a curve's signatures are over, as Sigstore signs.
func hashOf(c elliptic.Curve) crypto.Hash {
	switch c {
	case elliptic.P384():
		return crypto.SHA384
	case elliptic.P521():
		return crypto.SHA512
	}

	return crypto.SHA256
}

// keyHash is a log key's hash, as a signed note names its signature by: for
// ECDSA the SHA-256 of the key, PKIX, as Rekor names it; for Ed25519 the
// SHA-256 of the name, a newline, 0x01 and the key, as C2SP's signed-note
// does.
func keyHash(name string, key crypto.PublicKey) (uint32, error) {
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		der, err := x509.MarshalPKIXPublicKey(k)
		if err != nil {
			return 0, err
		}
		sum := sha256.Sum256(der)
		return binary.BigEndian.Uint32(sum[:]), nil
	case ed25519.PublicKey:
		h := sha256.New()
		h.Write([]byte(name))
		h.Write([]byte{'\n', 0x01})
		h.Write(k)
		return binary.BigEndian.Uint32(h.Sum(nil)), nil
	case *rsa.PublicKey:
		return 0, errors.New("a log's key that is RSA, and a log here signs its heads with ECDSA or Ed25519")
	}

	return 0, fmt.Errorf("a log's key that is a %T", key)
}

// call asks the log, and reads what it answers into `out`, which a status
// in `ok` is; the answer's location, for one that says where it is.
func (w *Witness) call(ctx context.Context, method, path string, in, out any, ok ...int) (string, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return "", err
		}
		body = bytes.NewReader(b)
	}

	u := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		u = w.base + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return "", err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "payday-witnessrekor")

	rsp, err := w.http.Do(req)
	if err != nil {
		return "", err
	}
	defer rsp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(rsp.Body, 64<<20))
	if err != nil {
		return "", err
	}
	if len(ok) == 0 {
		ok = []int{http.StatusOK}
	}
	matched := false
	for _, v := range ok {
		matched = matched || rsp.StatusCode == v
	}
	if !matched {
		return rsp.Header.Get("Location"), &statusError{code: rsp.StatusCode, body: string(b[:min(len(b), 300)])}
	}
	if out != nil {
		if raw, isRaw := out.(*[]byte); isRaw {
			*raw = b
		} else if err := json.Unmarshal(b, out); err != nil {
			return "", fmt.Errorf("what the log answered does not read: %w", err)
		}
	}

	return rsp.Header.Get("Location"), nil
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("the log answered %d: %s", e.code, e.body)
}
