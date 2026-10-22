package witnessrekor

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/lesomnus/payday/trail"
)

// entryV2 is an entry as Rekor's v2 answers with it: a TransparencyLogEntry,
// in protobuf's JSON, whose 64-bit numbers are strings.
type entryV2 struct {
	LogIndex       string `json:"logIndex"`
	InclusionProof struct {
		LogIndex   string   `json:"logIndex"`
		RootHash   string   `json:"rootHash"`
		TreeSize   string   `json:"treeSize"`
		Hashes     []string `json:"hashes"`
		Checkpoint struct {
			Envelope string `json:"envelope"`
		} `json:"checkpoint"`
	} `json:"inclusionProof"`
	CanonicalizedBody string `json:"canonicalizedBody"`
}

// bodyV2 is a `hashedrekord` v0.0.2 entry, as the log keeps it.
type bodyV2 struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		HashedRekordV002 struct {
			Data struct {
				Algorithm string `json:"algorithm"`
				Digest    string `json:"digest"`
			} `json:"data"`
			Signature struct {
				Content  string `json:"content"`
				Verifier struct {
					KeyDetails string `json:"keyDetails"`
					PublicKey  struct {
						RawBytes string `json:"rawBytes"`
					} `json:"publicKey"`
				} `json:"verifier"`
			} `json:"signature"`
		} `json:"hashedRekordV002"`
	} `json:"spec"`
}

var algorithmsV2 = map[crypto.Hash]string{crypto.SHA256: "SHA2_256", crypto.SHA384: "SHA2_384", crypto.SHA512: "SHA2_512"}

// keyDetails is how a v2 log names a key and what it signs: Ed25519 as
// Ed25519ph, ECDSA over its curve's own hash, RSA PKCS #1 v1.5 over SHA-256.
func keyDetails(der []byte) (string, error) {
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return "", err
	}

	switch k := k.(type) {
	case ed25519.PublicKey:
		return "PKIX_ED25519_PH", nil
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256():
			return "PKIX_ECDSA_P256_SHA_256", nil
		case elliptic.P384():
			return "PKIX_ECDSA_P384_SHA_384", nil
		case elliptic.P521():
			return "PKIX_ECDSA_P521_SHA_512", nil
		}
	case *rsa.PublicKey:
		switch n := k.N.BitLen(); n {
		case 2048, 3072, 4096:
			return "PKIX_RSA_PKCS1V15_" + strconv.Itoa(n) + "_SHA256", nil
		}
	}

	return "", fmt.Errorf("a %T, which a v2 log does not take", k)
}

func (w *Witness) addV2(ctx context.Context, s trail.Signed, digest []byte) (receipt, error) {
	details, err := keyDetails(s.PrehashKey)
	if err != nil {
		return receipt{}, err
	}

	req := map[string]any{"hashedRekordRequestV002": map[string]any{
		"digest": base64.StdEncoding.EncodeToString(digest),
		"signature": map[string]any{
			"content": base64.StdEncoding.EncodeToString(s.PrehashSig),
			"verifier": map[string]any{
				"publicKey":  map[string]string{"rawBytes": base64.StdEncoding.EncodeToString(s.PrehashKey)},
				"keyDetails": details,
			},
		},
	}}

	var raw []byte
	if _, err := w.call(ctx, http.MethodPost, "/api/v2/log/entries", req, &raw, http.StatusOK, http.StatusCreated); err != nil {
		return receipt{}, err
	}
	if _, err := readV2(raw); err != nil {
		return receipt{}, fmt.Errorf("what the log answered: %w", err)
	}

	return receipt{V: 2, Entry: raw}, nil
}

func readV2(raw json.RawMessage) (entry, error) {
	var e entryV2
	if err := json.Unmarshal(raw, &e); err != nil {
		return entry{}, err
	}
	body, err := base64.StdEncoding.DecodeString(e.CanonicalizedBody)
	if err != nil {
		return entry{}, fmt.Errorf("its body: %w", err)
	}

	p := e.InclusionProof
	index, err1 := strconv.ParseInt(p.LogIndex, 10, 64)
	size, err2 := strconv.ParseInt(p.TreeSize, 10, 64)
	root, err3 := base64.StdEncoding.DecodeString(p.RootHash)
	if err := errors.Join(err1, err2, err3); err != nil {
		return entry{}, fmt.Errorf("its proof: %w", err)
	}
	hashes := make([][]byte, len(p.Hashes))
	for i, v := range p.Hashes {
		if hashes[i], err = base64.StdEncoding.DecodeString(v); err != nil {
			return entry{}, fmt.Errorf("its proof: %w", err)
		}
	}

	return entry{index: index, body: body, proof: proof{index: index, size: size, root: root, hashes: hashes, head: p.Checkpoint.Envelope}}, nil
}

func boundV2(c trail.Checked, body []byte) string {
	var b bodyV2
	if err := json.Unmarshal(body, &b); err != nil || b.Kind != "hashedrekord" {
		return "the entry its receipt names is no hashedrekord"
	}

	d := b.Spec.HashedRekordV002.Data
	var sum []byte
	for h, s := range algorithmsV2 {
		if s == d.Algorithm {
			sum = c.Sums[h]
		}
	}
	if sum == nil || d.Digest != base64.StdEncoding.EncodeToString(sum) {
		return "the entry its receipt names is of another statement"
	}

	key, err := base64.StdEncoding.DecodeString(b.Spec.HashedRekordV002.Signature.Verifier.PublicKey.RawBytes)
	if err != nil || !holds(c.Keys, key) {
		return "the entry its receipt names is signed by a key the deployment does not sign with"
	}

	return ""
}

func (w *Witness) freshV2(ctx context.Context) (head, error) {
	var raw []byte
	if _, err := w.call(ctx, http.MethodGet, "/checkpoint", nil, &raw); err != nil {
		return head{}, err
	}

	return w.readHead(string(raw))
}

// consistentV2 proves from the log's tiles that its tree of `b` goes on from
// `a`: the tiles are read against `b`'s root, so that what was read is the
// tree the log signed.
func (w *Witness) consistentV2(ctx context.Context, a, b head) error {
	tree := tlog.Tree{N: b.size}
	copy(tree.Hash[:], b.root)
	hr := tlog.TileHashReader(tree, tiles{ctx, w})

	p, err := tlog.ProveTree(b.size, a.size, hr)
	if err != nil {
		return fmt.Errorf("reading the log's tiles: %w", err)
	}

	var th, h tlog.Hash
	copy(th[:], b.root)
	copy(h[:], a.root)

	return tlog.CheckTree(p, b.size, th, a.size, h)
}

// tiles reads a v2 log's tiles, at the paths C2SP's tlog-tiles names them by.
type tiles struct {
	ctx context.Context
	w   *Witness
}

func (tiles) Height() int { return 8 }

func (t tiles) ReadTiles(ts []tlog.Tile) ([][]byte, error) {
	out := make([][]byte, len(ts))
	for i, v := range ts {
		path, ok := strings.CutPrefix(v.Path(), "tile/8/")
		if !ok {
			return nil, fmt.Errorf("a tile of height %d", v.H)
		}

		var b []byte
		if _, err := t.w.call(t.ctx, http.MethodGet, "/tile/"+path, nil, &b); err != nil {
			return nil, err
		}
		out[i] = b
	}

	return out, nil
}

func (tiles) SaveTiles([]tlog.Tile, [][]byte) {}
