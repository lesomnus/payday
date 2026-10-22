package trail

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"
)

// ear is a witness that keeps what it hears in memory. `away` is one that
// neither hears nor answers, `hard` one that answers and hears nothing, and
// `deaf` one that only tells.
type ear struct {
	mu    sync.Mutex
	heard []Heard
	deaf  bool
	away  error
	hard  error

	// hide is the numbers it does not say it heard twice -- an answer that
	// left them out -- for a test of what an acceptance takes in.
	hide func(k int) bool
}

func (e *ear) Hear(_ context.Context, s Signed) (Witnessed, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch {
	case e.away != nil:
		return Witnessed{}, e.away
	case e.hard != nil:
		return Witnessed{}, e.hard
	}

	e.heard = append(e.heard, Heard{Signed: s, At: time.Now()})

	return Witnessed{Receipt: json.RawMessage(`{"n":` + strconv.Itoa(len(e.heard)) + `}`)}, nil
}

func (e *ear) Latest(_ context.Context, chain string) (Heard, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch {
	case e.deaf:
		return Heard{}, ErrDeaf
	case e.away != nil:
		return Heard{}, e.away
	}

	out := Heard{}
	for _, h := range e.heard {
		if h.Chain == chain && (h.K > out.K || h.K == out.K && h.At.Before(out.At)) {
			out = h
		}
	}

	// Every number heard as two statements or more: its first, and each other
	// it was heard as after it, once.
	firsts := map[int]Heard{}
	for _, h := range e.heard {
		if f, ok := firsts[h.K]; h.Chain == chain && (!ok || h.At.Before(f.At)) {
			firsts[h.K] = h
		}
	}
	said := map[flob.Digest]bool{}
	for _, h := range e.heard {
		f := firsts[h.K]
		if h.Chain != chain || h.Digest == f.Digest || said[h.Digest] || (e.hide != nil && e.hide(h.K)) {
			continue
		}

		said[h.Digest] = true
		out.Twice = append(out.Twice, Twice{First: f, Again: h})
	}
	slices.SortFunc(out.Twice, func(a, b Twice) int { return cmp.Compare(a.First.K, b.First.K) })

	return out, nil
}

// all is what it heard, in the order it heard it.
func (e *ear) all() []Heard {
	e.mu.Lock()
	defer e.mu.Unlock()

	return slices.Clone(e.heard)
}

// set is what it is, from now on.
func (e *ear) set(fn func(e *ear)) {
	e.mu.Lock()
	defer e.mu.Unlock()

	fn(e)
}

// heardBy is a deployment `n` passes old, each pass's checkpoint told to
// `ws`.
func heardBy(t *testing.T, n int, ws map[string]Witness) (*fakeStore, Policy) {
	s := &fakeStore{}
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: newKey(t), Witnesses: ws}
	for i := range n {
		s.add(fakeRow{tenant: newTenant().Uuid(), domain: person, created: ago(40 * day), value: strconv.Itoa(i)})
		p.Pass(t.Context(), s)
	}

	return s, p
}

// TestEveryCheckpointIsHeard (#115): each checkpoint is told to every witness
// as it is taken -- its number, what the store keeps it under, the one before
// it, and the statement as it was signed -- and what each answers is kept
// beside it in the checkpoint store. A verification asks each what it heard
// last, and says.
func TestEveryCheckpointIsHeard(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	e := &ear{}
	s, p := heardBy(t, 3, map[string]Witness{"ear": e})
	x.Equal(3, latestOf(t, s))

	ch, err := p.readChain(ctx)
	x.NoError(err)
	hs := e.all()
	x.Len(hs, 3, "each checkpoint was not told once")
	for i, h := range hs {
		v := numbered(t, p, i+1)
		x.Equal(i+1, h.K)
		x.Empty(h.Chain)
		x.Equal(v.d, h.Digest)
		x.Equal(v.c.Prev, h.Prev)
		x.True(verified(h.Key, h.Statement, h.Sig), "checkpoint %d was told unsigned", h.K)
		x.True(ch.heard[v.d]["ear"], "the store keeps no receipt of checkpoint %d", h.K)
	}

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(3, v.Heard["ear"].K)
	x.Contains(p.String(), "heard by ear")
}

// TestAWitnessThatDidNotHearIsToldAgain (#115): a witness that cannot hear
// does not stop the trail -- the acts take their checkpoints -- and the next
// pass tells it what it missed, oldest first, before its own.
func TestAWitnessThatDidNotHearIsToldAgain(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	e := &ear{away: errors.New("the witness is away")}
	s, p := heardBy(t, 2, map[string]Witness{"ear": e})
	x.Equal(2, latestOf(t, s), "a witness that could not hear stopped the trail")
	x.Empty(e.all())

	// The next pass tells it the two it missed, and then the one it takes.
	e.set(func(e *ear) { e.away = nil })
	p.Pass(ctx, s)

	ks := []int{}
	for _, h := range e.all() {
		ks = append(ks, h.K)
		x.True(verified(h.Key, h.Statement, h.Sig), "checkpoint %d was told again without the statement it was signed as", h.K)
	}
	x.Equal([]int{1, 2, 3}, ks)

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
}

// TestAChainCutShortIsFoundByItsWitness (#115).
//
// The newest checkpoint taken away from the store and the database's count
// wound back: nothing the database or the store keeps says it was there, and
// a verification that asks nobody finds nothing. A witness heard it. The
// store does not keep what it heard, and no act takes a checkpoint under a
// number it heard until an operator accepts -- numbered past it.
func TestAChainCutShortIsFoundByItsWitness(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	e := &ear{}
	s, p := heardBy(t, 3, map[string]Witness{"ear": e})
	x.NoError(p.checkpoints().Erase(ctx, numbered(t, p, 3).d))
	s.mu.Lock()
	s.counter = 2
	s.mu.Unlock()

	blind := p
	blind.Witnesses = nil
	v, err := blind.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"unkept"}, kinds(v))
	x.Equal(3, v.Findings[0].Checkpoint)
	x.Contains(v.Findings[0].Note, "cut short")

	x.ErrorIs(oneMore(t, s, p), errHeardAhead)
	x.Equal(2, latestOf(t, s), "a checkpoint was taken under a number a witness heard")

	_, err = accept(t, p, s, Acceptance{Why: "the store's disk lost the newest checkpoint", Restored: restored(2)})
	x.NoError(err)
	x.Equal(4, latestOf(t, s), "the acceptance was not numbered past what the witness heard")

	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
}

// windBack is somebody who takes the newest `n` checkpoints away from the
// store and winds the count back while every witness is away, and the
// deployment numbering again what they took: `n` acts, each a checkpoint, and
// a pass once the witness is back, which tells it what it missed.
func windBack(t *testing.T, s *fakeStore, p Policy, e *ear, n int) {
	x := require.New(t)
	ctx := t.Context()

	e.set(func(e *ear) { e.away = errors.New("the witness is away") })
	latest := latestOf(t, s)
	for k := latest - n + 1; k <= latest; k++ {
		x.NoError(p.checkpoints().Erase(ctx, numbered(t, p, k).d))
	}
	s.mu.Lock()
	s.counter = latest - n
	s.mu.Unlock()
	for range n + 1 {
		x.NoError(oneMore(t, s, p))
	}

	e.set(func(e *ear) { e.away = nil })
	p.Pass(ctx, s)
}

// at is a verification's findings by the checkpoint each is about, and the
// digest it names.
func at(v Verified) map[int]flob.Digest {
	out := map[int]flob.Digest{}
	for _, f := range v.Findings {
		out[f.Checkpoint] = f.Digest
	}

	return out
}

// TestANumberTakenAgainIsFoundByItsWitness (#115).
//
// Every witness away while the newest checkpoints are taken away and the count
// wound back, and the deployment numbers them again. Told them when it is
// back, the witness's newest is the deployment's newest -- and it heard each
// of those numbers as two statements, which is `rewritten` at each, below the
// head. An acceptance takes in the pairs it was shown and no others: one that
// a witness's answer left out is found after it, as one an attacker wound back
// before a rewind they meant to be accepted would be; and a third statement
// of a number taken in is a pair of its own.
func TestANumberTakenAgainIsFoundByItsWitness(t *testing.T) {
	ctx := t.Context()

	t.Run("every number taken again", func(t *testing.T) {
		x := require.New(t)
		e := &ear{}
		s, p := heardBy(t, 3, map[string]Witness{"ear": e})
		two, three := numbered(t, p, 2).d, numbered(t, p, 3).d
		windBack(t, s, p, e, 2)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"rewritten"}, kinds(v))
		x.Equal(map[int]flob.Digest{2: two, 3: three}, at(v), "%v", v.Findings)
		x.Contains(v.Findings[0].Note, "wound back")

		_, err = accept(t, p, s, Acceptance{Why: "the store's disk was restored from an older copy while the witness was away"})
		x.NoError(err)
		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
	})
	t.Run("an acceptance takes in what it was shown", func(t *testing.T) {
		x := require.New(t)
		e := &ear{}
		s, p := heardBy(t, 3, map[string]Witness{"ear": e})
		windBack(t, s, p, e, 1)
		p.Pass(ctx, s)
		windBack(t, s, p, e, 1)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]int{3, 6}, slices.Sorted(maps.Keys(at(v))), "%v", v.Findings)

		// The later rewind accepted, by an answer that left the earlier out.
		e.set(func(e *ear) { e.hide = func(k int) bool { return k == 3 } })
		_, err = accept(t, p, s, Acceptance{Why: "the restore of last night"})
		x.NoError(err)
		e.set(func(e *ear) { e.hide = nil })

		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.Len(v.Findings, 1, "%v", v.Findings)
		x.Equal("rewritten", v.Findings[0].Kind)
		x.Equal(3, v.Findings[0].Checkpoint, "the earlier rewind was hidden by the acceptance of the later")

		_, err = accept(t, p, s, Acceptance{Why: "and the one before it, looked at"})
		x.NoError(err)
		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)

		// A third statement of a number taken in.
		b := []byte(`{"k":3,"at":"2026-10-22T00:00:00Z","said":"a third time"}`)
		key, sig, err := sign(p.Signer, b)
		x.NoError(err)
		e.set(func(e *ear) {
			e.heard = append(e.heard, Heard{Signed: Signed{K: 3, Digest: flob.DigestFromBytes(b), Statement: b, Key: key, Sig: sig}, At: time.Now()})
		})
		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.Len(v.Findings, 1, "%v", v.Findings)
		x.Equal("rewritten", v.Findings[0].Kind)
		x.Equal(3, v.Findings[0].Checkpoint, "a third statement of a number taken in was taken in with it")
	})
}

// TestACheckpointNoWitnessHeardIsFound (#115): the newest checkpoint, once it
// is older than the grace, is a finding when no witness that answers heard
// it -- one that hears nothing, and one that does not answer at all, so that
// keeping a witness from hearing is no way past it. A witness that only tells
// is no witness a verification can ask.
func TestACheckpointNoWitnessHeardIsFound(t *testing.T) {
	ctx := t.Context()

	t.Run("one that heard nothing since", func(t *testing.T) {
		x := require.New(t)
		e := &ear{}
		s, p := heardBy(t, 1, map[string]Witness{"ear": e})
		e.set(func(e *ear) { e.hard = errors.New("the witness hears nothing") })
		x.NoError(oneMore(t, s, p))

		p.HearWithin = time.Hour
		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "a checkpoint was unheard inside the grace: %v", v.Findings)

		p.HearWithin = time.Nanosecond
		v, err = p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unheard"}, kinds(v))
		x.Equal(2, v.Findings[0].Checkpoint)
	})
	t.Run("one that does not answer", func(t *testing.T) {
		x := require.New(t)
		e := &ear{}
		s, p := heardBy(t, 1, map[string]Witness{"ear": e})
		e.set(func(e *ear) { e.away = errors.New("the witness is away") })

		p.HearWithin = time.Nanosecond
		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"unheard"}, kinds(v))
		x.Empty(v.Heard)
	})
	t.Run("one that only tells", func(t *testing.T) {
		x := require.New(t)
		e := &ear{deaf: true}
		s, p := heardBy(t, 1, map[string]Witness{"ear": e})
		x.Len(e.all(), 1, "a witness that only tells was not told")

		p.HearWithin = time.Nanosecond
		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.True(v.Ok(), "%v", v.Findings)
	})
}

// TestWhatAWitnessAnswersIsHeldToItsSignature (#115): a witness's word is
// what the deployment signed, and an answer whose signature is not good is a
// finding of its own -- somebody wrote to the witness -- and not a head the
// chain is held to.
func TestWhatAWitnessAnswersIsHeldToItsSignature(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	e := &ear{}
	s, p := heardBy(t, 1, map[string]Witness{"ear": e})
	e.set(func(e *ear) {
		e.heard = append(e.heard, Heard{Signed: Signed{K: 9, Digest: flob.DigestFromBytes([]byte("nothing")),
			Statement: []byte("{}"), Key: []byte("no key"), Sig: []byte("no signature")}, At: time.Now()})
	})

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"forged"}, kinds(v))
	x.Equal(9, v.Findings[0].Checkpoint)
	x.NoError(oneMore(t, s, p), "a forged answer stopped the trail")
}

// TestACheckpointIsWhatItHolds (#115): the checkpoint store is read for what
// each blob holds, and never by its labels. A checkpoint with its labels taken
// off is as much one as before -- the newest hidden that way, the count wound
// back, is a database behind it -- and is still a checkpoint the store had
// before the first pass. A blob that is neither a checkpoint nor a witness's
// receipt is a finding, and a receipt relabelled is still one.
func TestACheckpointIsWhatItHolds(t *testing.T) {
	ctx := t.Context()

	t.Run("relabelled, and the count wound back", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 3, nil)
		x.NoError(p.checkpoints().Label(ctx, numbered(t, p, 3).d, flob.Labels{}))
		s.mu.Lock()
		s.counter = 2
		s.mu.Unlock()

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Contains(kinds(v), "behind", "a checkpoint with its labels taken off was not read: %v", v.Findings)
		x.ErrorIs(oneMore(t, s, p), errBehind)
	})
	t.Run("before the first pass", func(t *testing.T) {
		x := require.New(t)
		p := Policy{Archive: flob.NewMemStores(), Signer: signer}
		_, err := p.checkpoints().Add(ctx, flob.Meta{}, bytes.NewReader(gzipped(t, []byte(`{"checkpoint":{"k":1}}`))))
		x.NoError(err)

		first, err := p.unchecked(ctx, &fakeStore{})
		x.NoError(err)
		x.False(first, "an archive whose checkpoint store keeps a blob was taken as it is")
	})
	t.Run("a blob that is neither", func(t *testing.T) {
		x := require.New(t)
		s, p := chained(t, 2, nil)
		_, err := p.checkpoints().Add(ctx, flob.Meta{}, bytes.NewReader(gzipped(t, []byte(`{"something":"else"}`))))
		x.NoError(err)

		v, err := p.Verify(ctx, s, false)
		x.NoError(err)
		x.Equal([]string{"forged"}, kinds(v))
		x.Contains(v.Findings[0].Note, "neither a checkpoint nor a witness's receipt")
	})
	t.Run("a receipt relabelled", func(t *testing.T) {
		x := require.New(t)
		_, p := heardBy(t, 1, map[string]Witness{"ear": &ear{}})
		c := numbered(t, p, 1)

		ls, err := listCheckpoints(ctx, p.checkpoints())
		x.NoError(err)
		x.Len(ls, 2, "the checkpoint and its receipt")
		for _, v := range ls {
			x.NoError(p.checkpoints().Label(ctx, v.d, flob.Labels{}))
		}

		ch, err := p.readChain(ctx)
		x.NoError(err)
		x.True(ch.heard[c.d]["ear"], "a receipt relabelled was not read")
		x.Len(ch.decided(), 1)
	})
}

// TestAWitnessIsNamed: a finding says which witness by its name, and a
// witness hears checkpoints, which a deployment with no archive takes none of.
func TestAWitnessIsNamed(t *testing.T) {
	x := require.New(t)

	a := flob.NewMemStores()
	x.Error(Policy{Archive: a, Signer: signer, Witnesses: map[string]Witness{"": &ear{}}}.Valid())
	x.Error(Policy{Archive: a, Signer: signer, Witnesses: map[string]Witness{"ear": nil}}.Valid())
	x.Error(Policy{Signer: signer, Witnesses: map[string]Witness{"ear": &ear{}}}.Valid())
	x.NoError(Policy{Archive: a, Signer: signer, Witnesses: map[string]Witness{"ear": &ear{}}}.Valid())
	x.Contains(Policy{Archive: a, Signer: signer}.String(), "heard by no witness")
}

// teller is a witness that keeps digests and holds the chain to itself.
type teller struct {
	*ear
	h      crypto.Hash
	checks func(kept []Checked, full bool) ([]Finding, error)
}

func (t *teller) Prehash(crypto.PublicKey) crypto.Hash { return t.h }

func (t *teller) Latest(context.Context, string) (Heard, error) { return Heard{}, ErrDeaf }

func (t *teller) Check(_ context.Context, _ string, kept []Checked, full bool) ([]Finding, error) {
	return t.checks(kept, full)
}

// TestAWitnessThatKeepsDigestsIsToldASignatureOverOne (#115): beside the
// statement, the digest it asks for, signed -- Ed25519 once more, as
// Ed25519ph, which RFC 8032 keeps apart from the Ed25519 the statement is
// signed with; ECDSA's own signature, which is over SHA-256 already. A key
// that signs Ed25519 over the message alone is found by TryPrehash, and so is
// one that signs the digest as if it were the message.
func TestAWitnessThatKeepsDigestsIsToldASignatureOverOne(t *testing.T) {
	ctx := t.Context()

	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	for name, c := range map[string]struct {
		key   crypto.Signer
		h     crypto.Hash
		again bool
	}{
		"ed25519":    {newKey(t), crypto.SHA512, true},
		"ecdsa-p256": {p256, crypto.SHA256, false},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			w := &teller{ear: &ear{}, h: c.h, checks: func([]Checked, bool) ([]Finding, error) { return nil, nil }}
			s := &fakeStore{}
			s.add(fakeRow{tenant: newTenant().Uuid(), domain: person, created: ago(40 * day), value: "a"})
			p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: c.key, Witnesses: map[string]Witness{"log": w}}
			p.Pass(ctx, s)

			hs := w.all()
			x.Len(hs, 1)
			h := hs[0]
			x.Equal(c.h, h.Prehash)
			d := c.h.New()
			d.Write(h.Statement)
			x.True(verifiedOver(c.key.Public(), c.h, d.Sum(nil), h.PrehashSig), "what it was told is no signature over the digest")
			x.True(verified(h.Key, h.Statement, h.Sig))
			x.Equal(c.again, !bytes.Equal(h.Sig, h.PrehashSig), "signed once more, or not")
			x.NoError(TryPrehash(c.key, w))
		})
	}

	t.Run("a key that cannot", func(t *testing.T) {
		x := require.New(t)
		w := &teller{ear: &ear{}, h: crypto.SHA512}
		x.Error(TryPrehash(messageOnly{newKey(t)}, w), "a key that refuses a digest")
		x.Error(TryPrehash(asMessage{newKey(t)}, w), "a key that signs a digest as a message")

		e := &ear{}
		heardBy(t, 1, map[string]Witness{"ear": e})
		x.Zero(e.all()[0].Prehash, "a witness that keeps statements was told a digest")
	})
}

// messageOnly is a key in a device that signs Ed25519 over the message alone.
type messageOnly struct{ crypto.Signer }

func (k messageOnly) Sign(r io.Reader, b []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts.HashFunc() != 0 {
		return nil, errors.New("an Ed25519 key here signs a message itself")
	}
	return k.Signer.Sign(r, b, opts)
}

// asMessage is one that signs a digest as if it were the message.
type asMessage struct{ crypto.Signer }

func (k asMessage) Sign(r io.Reader, b []byte, _ crypto.SignerOpts) ([]byte, error) {
	return k.Signer.Sign(r, b, crypto.Hash(0))
}

// TestAWitnessThatChecksItself (#115): what it finds is a finding of the
// verification's, bound by its digest; it is handed every checkpoint that is
// the deployment's word with the receipt the store keeps beside it and the
// statement's digests, and told a full verification; and one that cannot
// check is a witness that did not answer -- the newest it heard nothing of,
// once the grace is over.
func TestAWitnessThatChecksItself(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	var got []Checked
	var full []bool
	var finding *Finding
	var down error
	w := &teller{ear: &ear{}, h: crypto.SHA512, checks: func(kept []Checked, f bool) ([]Finding, error) {
		got, full = kept, append(full, f)
		if down != nil {
			return nil, down
		}
		if finding != nil {
			return []Finding{*finding}, nil
		}
		return nil, nil
	}}
	s, p := heardBy(t, 2, map[string]Witness{"log": w})

	v, err := p.Verify(ctx, s, true)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.True(full[len(full)-1], "a full verification was not told it was one")
	x.False(full[0], "a pass's verification was told it was a full one")
	x.Len(got, 2)
	for _, c := range got {
		x.NotNil(c.Receipt, "checkpoint %d was handed without its receipt", c.K)
		x.Len(c.Sums[crypto.SHA512], 64)
		x.NotEmpty(c.Keys)
	}
	clean := v.Digest

	finding = &Finding{Kind: "forged", Checkpoint: 1, Note: "the log's receipt of it: it does not read"}
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"forged"}, kinds(v))
	x.NotEqual(clean, v.Digest)

	finding, down = nil, errors.New("the log is away")
	p.HearWithin = time.Nanosecond
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Equal([]string{"unheard"}, kinds(v), "a witness that could not check heard the newest")

	down = nil
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "a witness whose receipt of the newest the store keeps did not hear it: %v", v.Findings)
}
