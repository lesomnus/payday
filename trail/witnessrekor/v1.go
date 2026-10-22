package witnessrekor

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lesomnus/payday/trail"
)

// entryV1 is an entry as Rekor's v1 API answers with it.
type entryV1 struct {
	Body           string `json:"body"`
	IntegratedTime int64  `json:"integratedTime"`
	LogID          string `json:"logID"`
	LogIndex       int64  `json:"logIndex"`
	Verification   struct {
		InclusionProof struct {
			Checkpoint string   `json:"checkpoint"`
			Hashes     []string `json:"hashes"`
			LogIndex   int64    `json:"logIndex"`
			RootHash   string   `json:"rootHash"`
			TreeSize   int64    `json:"treeSize"`
		} `json:"inclusionProof"`
		SignedEntryTimestamp string `json:"signedEntryTimestamp,omitempty"`
	} `json:"verification"`
}

// bodyV1 is a `hashedrekord` v0.0.1 entry, as proposed and as the log keeps
// it, canonicalized.
type bodyV1 struct {
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

var algorithmsV1 = map[crypto.Hash]string{crypto.SHA256: "sha256", crypto.SHA384: "sha384", crypto.SHA512: "sha512"}

func hashOfV1(v string) (crypto.Hash, bool) {
	for h, s := range algorithmsV1 {
		if s == v {
			return h, true
		}
	}

	return 0, false
}

// keyPEM is a key, PKIX, as Rekor's v1 takes it.
func keyPEM(der []byte) string {
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// addV1 proposes the entry, and reads it back from where the log says it is
// when it is there already: a checkpoint told again is the entry it was.
func (w *Witness) addV1(ctx context.Context, s trail.Signed, digest []byte) (receipt, error) {
	alg, ok := algorithmsV1[s.Prehash]
	if !ok {
		return receipt{}, fmt.Errorf("a digest of %s, and a v1 log takes SHA-256, SHA-384 or SHA-512", s.Prehash)
	}

	var b bodyV1
	b.APIVersion, b.Kind = "0.0.1", "hashedrekord"
	b.Spec.Data.Hash.Algorithm = alg
	b.Spec.Data.Hash.Value = hex.EncodeToString(digest)
	b.Spec.Signature.Content = base64.StdEncoding.EncodeToString(s.PrehashSig)
	b.Spec.Signature.PublicKey.Content = keyPEM(s.PrehashKey)

	added := map[string]json.RawMessage{}
	where, err := w.call(ctx, http.MethodPost, "/api/v1/log/entries", b, &added, http.StatusCreated)
	var se *statusError
	if errors.As(err, &se) && se.code == http.StatusConflict && where != "" {
		where, err = w.call(ctx, http.MethodGet, where, nil, &added)
	}
	if err != nil {
		return receipt{}, err
	}
	_ = where

	for uuid, e := range added {
		return receipt{V: 1, UUID: uuid, Entry: e}, nil
	}

	return receipt{}, errors.New("the log answered with no entry")
}

// readV1 is an entry of a receipt, read for what a check of it needs.
func readV1(raw json.RawMessage) (entry, error) {
	var e entryV1
	if err := json.Unmarshal(raw, &e); err != nil {
		return entry{}, err
	}
	body, err := base64.StdEncoding.DecodeString(e.Body)
	if err != nil {
		return entry{}, fmt.Errorf("its body: %w", err)
	}

	p := e.Verification.InclusionProof
	root, err := hex.DecodeString(p.RootHash)
	if err != nil {
		return entry{}, fmt.Errorf("its root: %w", err)
	}
	hashes := make([][]byte, len(p.Hashes))
	for i, v := range p.Hashes {
		if hashes[i], err = hex.DecodeString(v); err != nil {
			return entry{}, fmt.Errorf("its proof: %w", err)
		}
	}

	return entry{index: p.LogIndex, body: body, proof: proof{index: p.LogIndex, size: p.TreeSize, root: root, hashes: hashes, head: p.Checkpoint}}, nil
}

// boundV1 says what is wrong with an entry as the entry of a checkpoint:
// nothing, or how its digest or its key is another's.
func boundV1(c trail.Checked, body []byte) string {
	var b bodyV1
	if err := json.Unmarshal(body, &b); err != nil || b.Kind != "hashedrekord" {
		return "the entry its receipt names is no hashedrekord"
	}

	h, ok := hashOfV1(b.Spec.Data.Hash.Algorithm)
	if !ok || b.Spec.Data.Hash.Value != hex.EncodeToString(c.Sums[h]) {
		return "the entry its receipt names is of another statement"
	}

	key, err := base64.StdEncoding.DecodeString(b.Spec.Signature.PublicKey.Content)
	if err != nil {
		return "the entry its receipt names has a key that does not read"
	}
	if p, _ := pem.Decode(key); p != nil {
		key = p.Bytes
	}
	if !holds(c.Keys, key) {
		return "the entry its receipt names is signed by a key the deployment does not sign with"
	}

	return ""
}

// logV1 is what Rekor's v1 answers of its tree now.
type logV1 struct {
	RootHash       string `json:"rootHash"`
	SignedTreeHead string `json:"signedTreeHead"`
	TreeID         string `json:"treeID"`
	TreeSize       int64  `json:"treeSize"`
}

func (w *Witness) freshV1(ctx context.Context) (head, error) {
	var v logV1
	if _, err := w.call(ctx, http.MethodGet, "/api/v1/log", nil, &v); err != nil {
		return head{}, err
	}

	return w.readHead(v.SignedTreeHead)
}

// consistentV1 asks the log to prove its tree of `b` goes on from `a`.
func (w *Witness) consistentV1(ctx context.Context, a, b head) error {
	q := fmt.Sprintf("/api/v1/log/proof?firstSize=%d&lastSize=%d", a.size, b.size)
	if _, id, ok := strings.Cut(a.origin, " - "); ok {
		q += "&treeID=" + id
	}

	var rsp struct {
		Hashes   []string `json:"hashes"`
		RootHash string   `json:"rootHash"`
	}
	if _, err := w.call(ctx, http.MethodGet, q, nil, &rsp); err != nil {
		return err
	}

	hashes := make([][]byte, len(rsp.Hashes))
	for i, v := range rsp.Hashes {
		var err error
		if hashes[i], err = hex.DecodeString(v); err != nil {
			return fmt.Errorf("its proof: %w", err)
		}
	}

	return verifyConsistency(a, b, hashes)
}

// searchedV1 is every entry the log keeps signed by a key the deployment signs
// with, each that is no checkpoint the store keeps a finding: the head cut
// short, or a count wound back. An entry is read once a process.
func (w *Witness) searchedV1(ctx context.Context, kept []trail.Checked) ([]trail.Finding, error) {
	if len(kept) == 0 {
		return nil, nil
	}

	ours := map[string]bool{}
	for _, c := range kept {
		for _, sum := range c.Sums {
			ours[hex.EncodeToString(sum)] = true
		}
	}

	uuids := []string{}
	for _, k := range kept[0].Keys {
		var ids []string
		q := map[string]any{"publicKey": map[string]string{"format": "x509", "content": keyPEM(k)}}
		if _, err := w.call(ctx, http.MethodPost, "/api/v1/index/retrieve", q, &ids); err != nil {
			return nil, fmt.Errorf("searching the log for the deployment's entries: %w", err)
		}
		uuids = append(uuids, ids...)
	}

	unread := []string{}
	for _, id := range uuids {
		if _, ok := w.entries.Load(id); !ok {
			unread = append(unread, id)
		}
	}
	for i := 0; i < len(unread); i += 10 {
		var got []map[string]entryV1
		q := map[string]any{"entryUUIDs": unread[i:min(i+10, len(unread))]}
		if _, err := w.call(ctx, http.MethodPost, "/api/v1/log/entries/retrieve", q, &got); err != nil {
			return nil, fmt.Errorf("reading the deployment's entries: %w", err)
		}
		for _, m := range got {
			for id, e := range m {
				w.entries.Store(id, e)
			}
		}
	}

	fs := []trail.Finding{}
	for _, id := range uuids {
		v, ok := w.entries.Load(id)
		if !ok {
			continue
		}
		e := v.(entryV1)

		body, err := base64.StdEncoding.DecodeString(e.Body)
		var b bodyV1
		if err == nil {
			err = json.Unmarshal(body, &b)
		}
		if err != nil || b.Kind != "hashedrekord" || ours[b.Spec.Data.Hash.Value] {
			continue
		}

		fs = append(fs, trail.Finding{Kind: "unkept", Note: fmt.Sprintf(
			"the log keeps an entry the deployment's key signed, at %d and integrated at %s, that is no checkpoint the store keeps: the chain cut short, or a count wound back",
			e.LogIndex, time.Unix(e.IntegratedTime, 0).UTC().Format(time.RFC3339))})
	}

	return fs, nil
}
