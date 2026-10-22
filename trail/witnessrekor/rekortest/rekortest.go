// Package rekortest is a Rekor log for a test (payday#115): v1's API or v2's
// write API and tiles, on httptest, over a real RFC 6962 tree, its heads
// signed by an ECDSA key of its own. It takes an entry as Rekor does -- a
// `hashedrekord` whose signature verifies over its digest, Ed25519 as
// Ed25519ph -- and never reaches the network.
package rekortest

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/transparency-dev/merkle/rfc6962"
	"github.com/transparency-dev/merkle/testonly"
	"golang.org/x/mod/sumdb/note"
	"golang.org/x/mod/sumdb/tlog"
)

// Log is a Rekor log in memory.
type Log struct {
	// URL is where it is served, and KeyFile the PEM of its key's public
	// half, which a witness is given as `key=`.
	URL     string
	KeyFile string
	Origin  string

	v2  bool
	key *ecdsa.PrivateKey

	mu      sync.Mutex
	tree    *testonly.Tree
	hashes  []tlog.Hash
	entries []logged
	down    bool
	asked   map[string]int
}

type logged struct {
	uuid string
	body []byte
	key  []byte
	at   time.Time
}

// NewV1 is a log that answers v1's API, which can be searched.
func NewV1(t testing.TB) *Log { return start(t, false) }

// NewV2 is a log on tiles, Rekor v2's, which cannot.
func NewV2(t testing.TB) *Log { return start(t, true) }

func start(t testing.TB, v2 bool) *Log {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "log.pub")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	l := &Log{KeyFile: file, v2: v2, key: k, tree: testonly.New(rfc6962.DefaultHasher), asked: map[string]int{}}
	s := httptest.NewServer(http.HandlerFunc(l.serve))
	t.Cleanup(s.Close)

	l.URL = s.URL
	l.Origin = strings.TrimPrefix(s.URL, "http://")
	if !v2 {
		l.Origin += " - 1"
	}

	return l
}

// Down is the log not answering, or answering again.
func (l *Log) Down(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.down = v
}

// Fork is the log's tree made again with the first entry something else: a
// log that went back on what it signed.
func (l *Log) Fork() {
	l.mu.Lock()
	defer l.mu.Unlock()

	bodies := [][]byte{[]byte(`{"forked":true}`)}
	for _, e := range l.entries[1:] {
		bodies = append(bodies, e.body)
	}
	l.tree, l.hashes = testonly.New(rfc6962.DefaultHasher), nil
	for _, b := range bodies {
		l.append(b)
	}
}

// Asked is how many requests it was sent since the last time it was asked,
// by method and path, tiles as one.
func (l *Log) Asked() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := l.asked
	l.asked = map[string]int{}

	return out
}

// Len is how many entries it keeps.
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.entries)
}

func (l *Log) append(body []byte) {
	n := int64(l.tree.Size())
	l.tree.AppendData(body)

	hr := tlog.HashReaderFunc(func(ix []int64) ([]tlog.Hash, error) {
		out := make([]tlog.Hash, len(ix))
		for i, v := range ix {
			out[i] = l.hashes[v]
		}
		return out, nil
	})
	hs, err := tlog.StoredHashes(n, body, hr)
	if err != nil {
		panic(err)
	}
	l.hashes = append(l.hashes, hs...)
}

// head is the tree's head now, signed.
func (l *Log) head() string {
	text := fmt.Sprintf("%s\n%d\n%s\n", l.Origin, l.tree.Size(), base64.StdEncoding.EncodeToString(l.tree.Hash()))
	b, err := note.Sign(&note.Note{Text: text}, l.signer())
	if err != nil {
		panic(err)
	}

	return string(b)
}

func (l *Log) signer() note.Signer {
	der, _ := x509.MarshalPKIXPublicKey(&l.key.PublicKey)
	sum := sha256.Sum256(der)

	// Signed by its host's name; a v1 log's origin is the host and the tree.
	name, _, _ := strings.Cut(l.Origin, " - ")
	return noteSigner{name: name, hash: binary.BigEndian.Uint32(sum[:]), key: l.key}
}

type noteSigner struct {
	name string
	hash uint32
	key  *ecdsa.PrivateKey
}

func (s noteSigner) Name() string    { return s.name }
func (s noteSigner) KeyHash() uint32 { return s.hash }
func (s noteSigner) Sign(msg []byte) ([]byte, error) {
	sum := sha256.Sum256(msg)
	return ecdsa.SignASN1(rand.Reader, s.key, sum[:])
}

func (l *Log) serve(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()

	what := r.Method + " " + r.URL.Path
	if strings.HasPrefix(r.URL.Path, "/tile/") {
		what = r.Method + " /tile/"
	}
	l.asked[what]++

	if l.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}

	switch {
	case !l.v2 && r.Method == http.MethodPost && r.URL.Path == "/api/v1/log/entries":
		l.addV1(w, r)
	case !l.v2 && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/log/entries/"):
		l.getV1(w, strings.TrimPrefix(r.URL.Path, "/api/v1/log/entries/"))
	case !l.v2 && r.Method == http.MethodPost && r.URL.Path == "/api/v1/index/retrieve":
		l.searchV1(w, r)
	case !l.v2 && r.Method == http.MethodPost && r.URL.Path == "/api/v1/log/entries/retrieve":
		l.retrieveV1(w, r)
	case !l.v2 && r.Method == http.MethodGet && r.URL.Path == "/api/v1/log":
		write(w, http.StatusOK, map[string]any{"rootHash": hex.EncodeToString(l.tree.Hash()), "signedTreeHead": l.head(), "treeID": "1", "treeSize": l.tree.Size()})
	case !l.v2 && r.Method == http.MethodGet && r.URL.Path == "/api/v1/log/proof":
		l.proofV1(w, r)
	case l.v2 && r.Method == http.MethodPost && r.URL.Path == "/api/v2/log/entries":
		l.addV2(w, r)
	case l.v2 && r.Method == http.MethodGet && r.URL.Path == "/checkpoint":
		_, _ = w.Write([]byte(l.head()))
	case l.v2 && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/tile/"):
		l.tile(w, r)
	default:
		http.Error(w, "no such thing", http.StatusNotFound)
	}
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// verified answers whether `sig` is over `digest` by the key, as Rekor's
// hashedrekord verifies one: Ed25519 as Ed25519ph.
func verified(der []byte, h crypto.Hash, digest, sig []byte) error {
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return err
	}

	ok := false
	switch k := k.(type) {
	case ed25519.PublicKey:
		ok = ed25519.VerifyWithOptions(k, digest, sig, &ed25519.Options{Hash: h}) == nil
	case *ecdsa.PublicKey:
		ok = ecdsa.VerifyASN1(k, digest, sig)
	case *rsa.PublicKey:
		ok = rsa.VerifyPKCS1v15(k, h, digest, sig) == nil
	}
	if !ok {
		return fmt.Errorf("verifying signature: a %T's signature does not verify over the digest", k)
	}

	return nil
}

var algorithms = map[string]crypto.Hash{"sha256": crypto.SHA256, "sha384": crypto.SHA384, "sha512": crypto.SHA512,
	"SHA2_256": crypto.SHA256, "SHA2_384": crypto.SHA384, "SHA2_512": crypto.SHA512}

func (l *Log) addV1(w http.ResponseWriter, r *http.Request) {
	var b struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Spec       struct {
			Data struct {
				Hash struct {
					Algorithm string `json:"algorithm"`
					Value     string `json:"value"`
				} `json:"hash"`
			} `json:"data"`
			Signature struct {
				Content   string `json:"content"`
				PublicKey struct {
					Content string `json:"content"`
				} `json:"publicKey"`
			} `json:"signature"`
		} `json:"spec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Kind != "hashedrekord" || b.APIVersion != "0.0.1" {
		http.Error(w, "not a hashedrekord 0.0.1", http.StatusBadRequest)
		return
	}

	h, ok := algorithms[b.Spec.Data.Hash.Algorithm]
	digest, err1 := hex.DecodeString(b.Spec.Data.Hash.Value)
	sig, err2 := base64.StdEncoding.DecodeString(b.Spec.Signature.Content)
	pemKey, err3 := base64.StdEncoding.DecodeString(b.Spec.Signature.PublicKey.Content)
	p, _ := pem.Decode(pemKey)
	if !ok || err1 != nil || err2 != nil || err3 != nil || p == nil || len(digest) != h.Size() {
		http.Error(w, "an entry that does not read", http.StatusBadRequest)
		return
	}
	if err := verified(p.Bytes, h, digest, sig); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, _ := json.Marshal(b)
	uuid := hex.EncodeToString(rfc6962.DefaultHasher.HashLeaf(body))
	for _, e := range l.entries {
		if e.uuid == uuid {
			w.Header().Set("Location", "/api/v1/log/entries/"+uuid)
			http.Error(w, "an equivalent entry already exists", http.StatusConflict)
			return
		}
	}

	l.entries = append(l.entries, logged{uuid: uuid, body: body, key: p.Bytes, at: time.Now()})
	l.append(body)
	write(w, http.StatusCreated, map[string]any{uuid: l.entryV1(len(l.entries) - 1)})
}

func (l *Log) entryV1(i int) map[string]any {
	e := l.entries[i]
	hashes, err := l.tree.InclusionProof(uint64(i), l.tree.Size())
	if err != nil {
		panic(err)
	}
	hs := make([]string, len(hashes))
	for j, v := range hashes {
		hs[j] = hex.EncodeToString(v)
	}

	return map[string]any{
		"body": base64.StdEncoding.EncodeToString(e.body), "integratedTime": e.at.Unix(), "logID": "fake", "logIndex": i,
		"verification": map[string]any{"inclusionProof": map[string]any{
			"checkpoint": l.head(), "hashes": hs, "logIndex": i, "rootHash": hex.EncodeToString(l.tree.Hash()), "treeSize": l.tree.Size(),
		}},
	}
}

func (l *Log) getV1(w http.ResponseWriter, uuid string) {
	for i, e := range l.entries {
		if e.uuid == uuid {
			write(w, http.StatusOK, map[string]any{uuid: l.entryV1(i)})
			return
		}
	}
	http.Error(w, "no such entry", http.StatusNotFound)
}

func (l *Log) searchV1(w http.ResponseWriter, r *http.Request) {
	var q struct {
		PublicKey struct {
			Content string `json:"content"`
		} `json:"publicKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		http.Error(w, "a search that does not read", http.StatusBadRequest)
		return
	}
	b, _ := base64.StdEncoding.DecodeString(q.PublicKey.Content)
	p, _ := pem.Decode(b)
	if p == nil {
		http.Error(w, "a key that does not read", http.StatusBadRequest)
		return
	}

	out := []string{}
	for _, e := range l.entries {
		if bytes.Equal(e.key, p.Bytes) {
			out = append(out, e.uuid)
		}
	}
	write(w, http.StatusOK, out)
}

func (l *Log) retrieveV1(w http.ResponseWriter, r *http.Request) {
	var q struct {
		EntryUUIDs []string `json:"entryUUIDs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		http.Error(w, "a retrieval that does not read", http.StatusBadRequest)
		return
	}

	out := []map[string]any{}
	for _, id := range q.EntryUUIDs {
		for i, e := range l.entries {
			if e.uuid == id {
				out = append(out, map[string]any{id: l.entryV1(i)})
			}
		}
	}
	write(w, http.StatusOK, out)
}

func (l *Log) proofV1(w http.ResponseWriter, r *http.Request) {
	a, err1 := strconv.ParseUint(r.URL.Query().Get("firstSize"), 10, 64)
	b, err2 := strconv.ParseUint(r.URL.Query().Get("lastSize"), 10, 64)
	if err1 != nil || err2 != nil || a > b || b > l.tree.Size() {
		http.Error(w, "no such proof", http.StatusBadRequest)
		return
	}

	hashes, err := l.tree.ConsistencyProof(a, b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hs := make([]string, len(hashes))
	for i, v := range hashes {
		hs[i] = hex.EncodeToString(v)
	}
	write(w, http.StatusOK, map[string]any{"hashes": hs, "rootHash": hex.EncodeToString(l.tree.HashAt(b))})
}

func (l *Log) addV2(w http.ResponseWriter, r *http.Request) {
	var q struct {
		HashedRekordRequestV002 struct {
			Digest    string `json:"digest"`
			Signature struct {
				Content  string `json:"content"`
				Verifier struct {
					PublicKey struct {
						RawBytes string `json:"rawBytes"`
					} `json:"publicKey"`
					KeyDetails string `json:"keyDetails"`
				} `json:"verifier"`
			} `json:"signature"`
		} `json:"hashedRekordRequestV002"`
	}
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		http.Error(w, "a request that does not read", http.StatusBadRequest)
		return
	}
	req := q.HashedRekordRequestV002
	digest, err1 := base64.StdEncoding.DecodeString(req.Digest)
	sig, err2 := base64.StdEncoding.DecodeString(req.Signature.Content)
	key, err3 := base64.StdEncoding.DecodeString(req.Signature.Verifier.PublicKey.RawBytes)
	if err1 != nil || err2 != nil || err3 != nil {
		http.Error(w, "a request that does not read", http.StatusBadRequest)
		return
	}

	var h crypto.Hash
	alg := ""
	switch d := req.Signature.Verifier.KeyDetails; {
	case d == "PKIX_ED25519_PH", strings.HasSuffix(d, "_SHA_512"):
		h, alg = crypto.SHA512, "SHA2_512"
	case strings.HasSuffix(d, "_SHA_384"):
		h, alg = crypto.SHA384, "SHA2_384"
	case strings.HasSuffix(d, "_SHA_256"), strings.HasSuffix(d, "_SHA256"):
		h, alg = crypto.SHA256, "SHA2_256"
	default:
		http.Error(w, "key details this log does not take: "+d, http.StatusBadRequest)
		return
	}
	if len(digest) != h.Size() {
		http.Error(w, "a digest of another size than the key details say", http.StatusBadRequest)
		return
	}
	if err := verified(key, h, digest, sig); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, _ := json.Marshal(map[string]any{"apiVersion": "0.0.2", "kind": "hashedrekord", "spec": map[string]any{"hashedRekordV002": map[string]any{
		"data":      map[string]any{"algorithm": alg, "digest": req.Digest},
		"signature": map[string]any{"content": req.Signature.Content, "verifier": map[string]any{"keyDetails": req.Signature.Verifier.KeyDetails, "publicKey": map[string]any{"rawBytes": req.Signature.Verifier.PublicKey.RawBytes}}},
	}}})

	l.entries = append(l.entries, logged{uuid: hex.EncodeToString(rfc6962.DefaultHasher.HashLeaf(body)), body: body, key: key, at: time.Now()})
	l.append(body)
	i := len(l.entries) - 1

	hashes, _ := l.tree.InclusionProof(uint64(i), l.tree.Size())
	hs := make([]string, len(hashes))
	for j, v := range hashes {
		hs[j] = base64.StdEncoding.EncodeToString(v)
	}
	write(w, http.StatusOK, map[string]any{
		"logIndex": strconv.Itoa(i), "kindVersion": map[string]string{"kind": "hashedrekord", "version": "0.0.2"},
		"inclusionProof": map[string]any{
			"logIndex": strconv.Itoa(i), "rootHash": base64.StdEncoding.EncodeToString(l.tree.Hash()), "treeSize": strconv.FormatUint(l.tree.Size(), 10),
			"hashes": hs, "checkpoint": map[string]string{"envelope": l.head()},
		},
		"canonicalizedBody": base64.StdEncoding.EncodeToString(body),
	})
}

func (l *Log) tile(w http.ResponseWriter, r *http.Request) {
	t, err := tlog.ParseTilePath("tile/8/" + strings.TrimPrefix(r.URL.Path, "/tile/"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	hr := tlog.HashReaderFunc(func(ix []int64) ([]tlog.Hash, error) {
		out := make([]tlog.Hash, len(ix))
		for i, v := range ix {
			if v >= int64(len(l.hashes)) {
				return nil, fmt.Errorf("no hash %d", v)
			}
			out[i] = l.hashes[v]
		}
		return out, nil
	})
	b, err := tlog.ReadTileData(t, hr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_, _ = w.Write(b)
}
