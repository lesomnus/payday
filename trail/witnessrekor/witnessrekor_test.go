package witnessrekor_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/trail/witnessrekor"
	"github.com/lesomnus/payday/trail/witnessrekor/rekortest"
)

// told is a checkpoint, `k`, signed as the trail signs one and told as the
// trail tells a witness that keeps digests: a signature over the digest the
// witness asks for.
type told struct {
	signed trail.Signed
	sums   map[crypto.Hash][]byte
}

func tell(t *testing.T, w *witnessrekor.Witness, key crypto.Signer, k int) told {
	x := require.New(t)

	statement := []byte(fmt.Sprintf(`{"k":%d,"blobs":[{"ns":"a tenant","digest":"sha256:%064d"}]}`, k, k))
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	x.NoError(err)

	h := w.Prehash(key.Public())
	d := h.New()
	d.Write(statement)
	sum := d.Sum(nil)
	var opts crypto.SignerOpts = h
	if _, ok := key.Public().(ed25519.PublicKey); ok {
		opts = &ed25519.Options{Hash: h}
	}
	sig, err := key.Sign(rand.Reader, sum, opts)
	x.NoError(err)

	a, b, c := sha256.Sum256(statement), sha512.Sum384(statement), sha512.Sum512(statement)
	return told{
		signed: trail.Signed{K: k, Digest: flob.DigestFromBytes(statement), Statement: statement, Key: der,
			Prehash: h, PrehashKey: der, PrehashSig: sig},
		sums: map[crypto.Hash][]byte{crypto.SHA256: a[:], crypto.SHA384: b[:], crypto.SHA512: c[:]},
	}
}

func open(t *testing.T, l *rekortest.Log) *witnessrekor.Witness {
	v := "rekor:" + l.URL + "?key=" + l.KeyFile
	if strings.HasSuffix(t.Name(), "v2") || strings.Contains(t.Name(), "v2/") {
		v += "&v=2"
	}

	w, err := witnessrekor.Open(v)
	require.NoError(t, err)

	return w
}

// heard is what the store would keep: each checkpoint told, with its receipt,
// and what each was told as.
func heard(t *testing.T, w *witnessrekor.Witness, key crypto.Signer, n int) ([]trail.Checked, []trail.Signed) {
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	require.NoError(t, err)

	out, as := []trail.Checked{}, []trail.Signed{}
	for k := 1; k <= n; k++ {
		c := tell(t, w, key, k)
		r, err := w.Hear(t.Context(), c.signed)
		require.NoError(t, err, "checkpoint %d", k)
		out = append(out, trail.Checked{K: k, Digest: c.signed.Digest, Key: der, Sums: c.sums, Receipt: r.Receipt, Keys: [][]byte{der}})
		as = append(as, c.signed)
	}

	return out, as
}

func keys(t *testing.T) map[string]crypto.Signer {
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	r, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	return map[string]crypto.Signer{"ed25519": ed, "ecdsa-p256": p256, "ecdsa-p384": p384, "rsa-2048": r}
}

// TestEveryCheckpointIsLogged (payday#115): a checkpoint's digest goes to the
// log signed by the deployment's key -- Ed25519 as Ed25519ph, ECDSA and RSA
// over the digest the log takes, which the log verifies as Rekor does -- and
// what the log answers is a receipt that holds: the entry of its checkpoint,
// proved in a tree the log signed, which goes on to the tree now. Nothing is
// found, and Latest has nothing to answer.
func TestEveryCheckpointIsLogged(t *testing.T) {
	for _, v := range []string{"v1", "v2"} {
		for name, key := range keys(t) {
			t.Run(name+"/"+v, func(t *testing.T) {
				x := require.New(t)
				l := rekortest.NewV1(t)
				if v == "v2" {
					l = rekortest.NewV2(t)
				}
				w := open(t, l)

				kept, as := heard(t, w, key, 3)
				x.Equal(3, l.Len())

				fs, err := w.Check(t.Context(), "", kept, true)
				x.NoError(err)
				x.Empty(fs)

				// Told again, it is the entry it was.
				if v == "v1" {
					_, err = w.Hear(t.Context(), as[0])
					x.NoError(err)
					x.Equal(3, l.Len())
				}

				_, err = w.Latest(t.Context(), "")
				x.ErrorIs(err, trail.ErrDeaf)
			})
		}
	}
}

// TestWhatTheLogSaysIsHeldToIt (payday#115): a receipt that does not prove
// its entry, or is the entry of another statement, is `forged`; a log whose
// tree does not go on from what it signed is `rewritten`; on a v1 log, an
// entry the deployment's key signed that is no checkpoint the store keeps is
// `unkept` -- the head cut short; and a log that does not answer is an error.
// A tenant's chain is no log's the deployment names.
func TestWhatTheLogSaysIsHeldToIt(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	for _, v := range []string{"v1", "v2"} {
		t.Run("a receipt changed/"+v, func(t *testing.T) {
			x := require.New(t)
			l := rekortest.NewV1(t)
			if v == "v2" {
				l = rekortest.NewV2(t)
			}
			w := open(t, l)
			kept, _ := heard(t, w, key, 3)

			var r map[string]any
			x.NoError(json.Unmarshal(kept[1].Receipt, &r))
			kept[1].Receipt, kept[2].Receipt = kept[2].Receipt, kept[1].Receipt

			fs, err := w.Check(t.Context(), "", kept, true)
			x.NoError(err)
			x.Len(fs, 2, "%v", fs)
			for _, f := range fs {
				x.Equal("forged", f.Kind)
				x.Contains(f.Note, "another statement")
			}
		})
		t.Run("a log that went back/"+v, func(t *testing.T) {
			x := require.New(t)
			l := rekortest.NewV1(t)
			if v == "v2" {
				l = rekortest.NewV2(t)
			}
			w := open(t, l)
			kept, _ := heard(t, w, key, 3)

			l.Fork()
			fs, err := w.Check(t.Context(), "", kept, false)
			x.NoError(err)
			x.NotEmpty(fs)
			x.Equal("rewritten", fs[0].Kind, "%v", fs)
		})
		t.Run("a log that does not answer/"+v, func(t *testing.T) {
			x := require.New(t)
			l := rekortest.NewV1(t)
			if v == "v2" {
				l = rekortest.NewV2(t)
			}
			w := open(t, l)
			kept, _ := heard(t, w, key, 1)

			l.Down(true)
			_, err := w.Check(t.Context(), "", kept, false)
			x.Error(err)
		})
	}
	t.Run("the head cut short/v1", func(t *testing.T) {
		x := require.New(t)
		l := rekortest.NewV1(t)
		w := open(t, l)
		kept, _ := heard(t, w, key, 3)

		fs, err := w.Check(t.Context(), "", kept[:2], false)
		x.NoError(err)
		x.Len(fs, 1, "%v", fs)
		x.Equal("unkept", fs[0].Kind)
		x.Contains(fs[0].Note, "no checkpoint the store keeps")
	})
	t.Run("a tenant's chain", func(t *testing.T) {
		x := require.New(t)
		w := open(t, rekortest.NewV1(t))
		s := tell(t, w, key, 1).signed
		s.Chain = "a tenant"
		_, err := w.Hear(t.Context(), s)
		x.Error(err)
	})
}

// TestAVerificationAsksTheLogLittle (payday#115): however long the history,
// a verification asks the log for its tree now and a proof or two -- the
// receipts carry the rest -- and a full verification asks again what the
// receipts' trees say of one another.
func TestAVerificationAsksTheLogLittle(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	for _, v := range []string{"v1", "v2"} {
		t.Run(v, func(t *testing.T) {
			x := require.New(t)
			l := rekortest.NewV1(t)
			if v == "v2" {
				l = rekortest.NewV2(t)
			}
			w := open(t, l)
			kept, _ := heard(t, w, key, 30)
			l.Asked()

			count := func(m map[string]int, what string) int { return m[what] }

			for range 2 {
				fs, err := w.Check(t.Context(), "", kept, false)
				x.NoError(err)
				x.Empty(fs)
			}
			asked := l.Asked()
			if v == "v1" {
				x.Equal(2, count(asked, "GET /api/v1/log"), "%v", asked)
				x.LessOrEqual(count(asked, "GET /api/v1/log/proof"), 4, "%v", asked)
				x.LessOrEqual(count(asked, "POST /api/v1/log/entries/retrieve"), 3, "%v", asked)
			} else {
				x.Equal(2, count(asked, "GET /checkpoint"), "%v", asked)
				x.LessOrEqual(count(asked, "GET /tile/"), 16, "%v", asked)
			}

			fs, err := w.Check(t.Context(), "", kept, true)
			x.NoError(err)
			x.Empty(fs)
			asked = l.Asked()
			if v == "v1" {
				x.GreaterOrEqual(count(asked, "GET /api/v1/log/proof"), 29, "a full verification did not prove each tree to the next: %v", asked)
			} else {
				x.Greater(count(asked, "GET /tile/"), 16, "a full verification did not prove each tree to the next: %v", asked)
			}
		})
	}
}

// TestALogsKeyIsTheDeployments (payday#115): the log's key is given, never
// asked of the log, and a tree's head another key signed is nobody's word --
// a log that answers with one is a witness that did not answer.
func TestALogsKeyIsTheDeployments(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	for _, v := range []string{"v1", "v2"} {
		t.Run(v, func(t *testing.T) {
			x := require.New(t)
			l, other := rekortest.NewV1(t), rekortest.NewV1(t)
			q := ""
			if v == "v2" {
				l, other, q = rekortest.NewV2(t), rekortest.NewV2(t), "&v=2"
			}

			w, err := witnessrekor.Open("rekor:" + l.URL + "?key=" + other.KeyFile + q)
			x.NoError(err)
			kept, _ := heard(t, w, key, 2)

			_, err = w.Check(t.Context(), "", kept, false)
			x.ErrorContains(err, "not one the log's key signed")

			_, err = witnessrekor.Open("rekor:" + l.URL)
			x.ErrorContains(err, "key=")
		})
	}
}
