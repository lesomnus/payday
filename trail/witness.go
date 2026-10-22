package trail

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/otx/log"
)

// Witness hears every checkpoint the trail takes, somewhere outside the
// database and the archive (payday#115).
//
// # Why
//
// A checkpoint names the one before it, so the chain below the newest can be
// checked. The newest itself -- the head -- is known only from the database's
// count and from what the checkpoint store keeps, and whoever can write the
// database and take objects out of the store can cut the chain short there:
// take the newest checkpoints away and wind the count back, and the next act
// numbers past what is left. Nothing in the two says so. A witness is
// somewhere they do not reach that heard each checkpoint as it was taken, and
// a verification holds the chain to it: one it heard that the store does not
// keep, or keeps as something else, is a finding, and so is the newest the
// store keeps once it is older than [Policy.HearWithin] and no witness that
// answers heard it -- keeping a witness from hearing is no way past it either.
//
// A deployment names one or several ([Policy.Witnesses]; `audit.checkpoints.
// witnesses` in config). Each is told of every checkpoint as it is taken,
// and what it answers is kept beside the checkpoint, in the checkpoint store.
// One that cannot hear does not stop the trail: the act says so in the log,
// and the next pass tells it again.
//
// # Chains
//
// What a witness hears is a statement in a chain ([Signed.Chain]): the
// deployment's checkpoints are one. A tenant whose contract names witnesses of
// its own is to have one of its own -- a statement of its part of each
// checkpoint, naming its statement before -- told to its witnesses the same
// way. A witness answers for each chain apart.
type Witness interface {
	// Hear is told of a statement as it is signed. What it answers is kept
	// beside the checkpoint. An error is a statement it did not hear, which
	// it is told again.
	Hear(ctx context.Context, s Signed) (Witnessed, error)

	// Latest is the newest statement of a chain the witness heard: the
	// highest number, and of two of that number, the one it heard first.
	// Nothing heard is zero. A witness that can also tell it heard a number
	// as two statements -- the count wound back and the number taken again
	// -- says each in [Heard.Twice]. [ErrDeaf] is a witness that only tells,
	// and has nothing to answer with.
	Latest(ctx context.Context, chain string) (Heard, error)
}

// Signed is a statement the deployment signed, as a witness is told of it.
type Signed struct {
	// Chain is whose chain it is in: empty for the deployment's
	// checkpoints, and a tenant's for a statement of its own.
	Chain string `json:"chain,omitempty"`

	// K is its number in the chain, Digest what the checkpoint store keeps
	// it under, and Prev the one before it, which it names.
	K      int         `json:"k"`
	Digest flob.Digest `json:"digest"`
	Prev   flob.Digest `json:"prev,omitempty"`

	// Statement is what was signed, byte for byte, Key the public half of
	// the key that signed it, PKIX, and Sig the signature: a witness can hold
	// what it is told to the key, and a verification what it answers.
	Statement []byte `json:"statement"`
	Key       []byte `json:"key"`
	Sig       []byte `json:"sig"`
}

// Witnessed is what a witness answers when it hears a statement: its
// receipt, in a form of its own -- where a store kept it, a log's inclusion
// proof -- and empty for one that answers nothing but that it heard.
type Witnessed struct {
	Receipt json.RawMessage `json:"receipt,omitempty"`
}

// Heard is a statement as a witness heard it, and when, by its own clock.
type Heard struct {
	Signed

	At time.Time `json:"at"`

	// Twice is, of the chain, every number the witness heard as two
	// statements or more: each the first it heard, and one of the others,
	// lowest number first. A number heard twice is a count wound back -- the
	// newest checkpoints taken away while every witness was away, and
	// numbered again -- and is found below the head, where the newest no
	// longer says it. Every one, and not the latest or the lowest: a single
	// one would be the one an acceptance of it hides another behind.
	Twice []Twice `json:"twice,omitempty"`
}

// Twice is a number a witness heard two statements of: the first it heard,
// and another, heard after it.
type Twice struct {
	First Heard `json:"first"`
	Again Heard `json:"again"`
}

// HeardTwice is a number a witness heard twice, as an acceptance keeps it:
// the number and the two digests ([Accepted.Twice]).
type HeardTwice struct {
	K     int         `json:"k"`
	First flob.Digest `json:"first"`
	Again flob.Digest `json:"again"`
}

// heardTwice is what an acceptance keeps of a number heard twice.
func (t Twice) heardTwice() HeardTwice {
	return HeardTwice{K: t.First.K, First: t.First.Digest, Again: t.Again.Digest}
}

// ErrDeaf is a witness that only tells -- a log that the monitoring outside
// reads -- and has nothing to answer a verification with.
var ErrDeaf = errors.New("the witness only tells, and answers nothing")

// errHeardAhead is a database that has numbered fewer checkpoints than a
// witness heard: wound back, the newest checkpoints taken away from the store
// or a database restored from before them.
var errHeardAhead = errors.New("a witness heard checkpoints the database has not numbered: a database wound back or restored from before them, " +
	"and no checkpoint is taken under a number already taken until an operator accepts; see `trail verify`")

// formatWitnessed is the label a witness's receipt is written under. It is a
// hint for whoever lists the store by hand: what a blob is, is what it holds.
const formatWitnessed = "witnessed+json+gzip"

// witnessed is a witness's receipt as the checkpoint store keeps it, beside
// the checkpoint it is of: which witness, which checkpoint, when, and what it
// answered.
type witnessed struct {
	Witness string          `json:"witness"`
	Of      flob.Digest     `json:"of"`
	K       int             `json:"k"`
	At      time.Time       `json:"at"`
	Receipt json.RawMessage `json:"receipt,omitempty"`
}

// signedOf is a checkpoint as a witness is told of it.
func signedOf(v stored) Signed {
	return Signed{K: v.c.K, Digest: v.d, Prev: v.c.Prev, Statement: v.s.Checkpoint, Key: v.s.Key, Sig: v.s.Sig}
}

// tell tells each witness that has not heard it of one checkpoint, and keeps
// what each answers beside it. A witness that does not hear is said so in the
// log, and is told again by the next pass ([pass.retell]); it stops nothing.
func (p Policy) tell(ctx context.Context, v stored, heard map[string]bool) {
	for _, name := range slices.Sorted(maps.Keys(p.Witnesses)) {
		if heard[name] {
			continue
		}

		w, err := p.Witnesses[name].Hear(ctx, signedOf(v))
		if err == nil {
			err = p.keepWitnessed(ctx, witnessed{Witness: name, Of: v.d, K: v.c.K, At: time.Now().UTC(), Receipt: w.Receipt})
		}
		if err != nil {
			log.From(ctx).WarnContext(ctx, "trail: a witness did not hear a checkpoint, and the next pass tells it again",
				"witness", name, "k", v.c.K, "err", err)
		}
	}
}

// keepWitnessed writes a witness's receipt into the checkpoint store, beside
// the checkpoint it is of.
func (p Policy) keepWitnessed(ctx context.Context, v witnessed) error {
	doc, err := json.Marshal(struct {
		Witnessed witnessed `json:"witnessed"`
	}{v})
	if err != nil {
		return err
	}

	buf := &bytes.Buffer{}
	z := gzip.NewWriter(buf)
	if _, err := z.Write(doc); err != nil {
		return err
	}
	if err := z.Close(); err != nil {
		return err
	}

	l := flob.Labels{}
	l.Set(labelFormat, formatWitnessed)
	l.Set(labelK, strconv.Itoa(v.K))
	if _, err := p.checkpoints().Add(ctx, flob.Meta{Labels: l}, buf); err != nil && !errors.Is(err, flob.ErrAlreadyExists) {
		return fmt.Errorf("the receipt of witness %s: %w", v.Witness, err)
	}

	return nil
}

// retold is how many checkpoints a pass tells again at most: a witness named
// after a long history hears it over a few passes.
const retold = 100

// retell tells each witness, oldest first, the checkpoints it has no receipt
// for: one it did not hear when the act that took it told it, and every one
// taken before the witness was named. At most [retold] checkpoints a pass.
func (x *pass) retell(ctx context.Context) {
	p := x.p
	if len(p.Witnesses) == 0 {
		return
	}

	ch, err := p.readChain(ctx)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "trail: telling the witnesses again", "err", err)

		return
	}

	n := 0
	for _, v := range ch.decided() {
		if n >= retold {
			return
		}

		heard := ch.heard[v.d]
		if !slices.ContainsFunc(slices.Collect(maps.Keys(p.Witnesses)), func(name string) bool { return !heard[name] }) {
			continue
		}

		p.tell(ctx, v, heard)
		n++
	}
}

// hearWithin is [Policy.HearWithin], or twice how often the policy is
// applied: long enough for a pass to tell a witness that was away again.
func (p Policy) hearWithin() time.Duration {
	if p.HearWithin > 0 {
		return p.HearWithin
	}

	return 2 * p.every()
}

// answers is what each witness that answers heard last of the deployment's
// chain, by name. One that only tells is not in it, and one that does not
// answer is said so in the log and is not in it either; `asked` is how many
// were asked that are not deaf.
func (p Policy) answers(ctx context.Context) (map[string]Heard, int) {
	out := map[string]Heard{}
	asked := 0
	for _, name := range slices.Sorted(maps.Keys(p.Witnesses)) {
		h, err := p.Witnesses[name].Latest(ctx, "")
		switch {
		case errors.Is(err, ErrDeaf):
			continue
		case err != nil:
			asked++
			log.From(ctx).WarnContext(ctx, "trail: a witness did not answer", "witness", name, "err", err)

			continue
		}

		asked++
		out[name] = h
	}

	return out, asked
}

// heardAhead is the highest number a witness that answers heard of the
// deployment's chain: what the database has to have numbered for a
// checkpoint to be taken after it, and what an acceptance is numbered past.
func (p Policy) heardAhead(ctx context.Context) int {
	if len(p.Witnesses) == 0 {
		return 0
	}

	hs, _ := p.answers(ctx)
	n := 0
	for _, h := range hs {
		if verified(h.Key, h.Statement, h.Sig) {
			n = max(n, h.K)
		}
	}

	return n
}

// acceptedTwice answers whether an acceptance the chain vouches for took in
// this pair of statements a witness heard of one number: one that an
// operator looked at, which is a finding no more. Another statement of the
// same number heard after it is a pair of its own.
func (ch chain) acceptedTwice(witness string, t HeardTwice) bool {
	for _, v := range ch.decided() {
		if a := v.c.Accepted; a != nil && slices.Contains(a.Twice[witness], t) {
			return true
		}
	}

	return false
}

// twices is, by witness, the pairs of statements of one number the witnesses
// that answered heard that no acceptance took in yet: what an acceptance of
// them takes in. They are findings of the verification it is of, and bound by
// its digest as every finding is.
func (ch chain) twices(hs map[string]Heard) map[string][]HeardTwice {
	out := map[string][]HeardTwice{}
	for _, name := range slices.Sorted(maps.Keys(hs)) {
		for _, t := range hs[name].Twice {
			if v := t.heardTwice(); !ch.acceptedTwice(name, v) {
				out[name] = append(out[name], v)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}

	return out
}

// witnessFindings holds the chain to every witness that answers: what each
// heard last, the store keeps as it was heard, and the newest checkpoint the
// store keeps, once it is older than [Policy.HearWithin], was heard by one of
// them. See [Witness].
func (p Policy) witnessFindings(ctx context.Context, ch chain, out *Verified) []Finding {
	if len(p.Witnesses) == 0 {
		return nil
	}

	var newest *stored
	for _, v := range ch.decided() {
		if newest == nil || v.c.K > newest.c.K {
			newest = &v
		}
	}

	hs, asked := p.answers(ctx)
	out.Heard = hs

	fs := []Finding{}
	heardNewest := false
	for _, name := range slices.Sorted(maps.Keys(hs)) {
		h := hs[name]
		if h.K == 0 {
			continue
		}
		if !verified(h.Key, h.Statement, h.Sig) {
			fs = append(fs, Finding{Kind: "forged", Checkpoint: h.K, Digest: h.Digest,
				Note: fmt.Sprintf("witness %s answers that it heard it, and its signature is not good", name)})
			continue
		}

		for _, t := range h.Twice {
			if ch.acceptedTwice(name, t.heardTwice()) {
				continue
			}

			switch {
			case !verified(t.First.Key, t.First.Statement, t.First.Sig) || !verified(t.Again.Key, t.Again.Statement, t.Again.Sig):
				fs = append(fs, Finding{Kind: "forged", Checkpoint: t.First.K, Digest: t.First.Digest,
					Note: fmt.Sprintf("witness %s answers that it heard it again as %s, and a signature is not good", name, t.Again.Digest)})
			default:
				fs = append(fs, Finding{Kind: "rewritten", Checkpoint: t.First.K, Digest: t.First.Digest,
					Note: fmt.Sprintf("witness %s heard it as %s, and again as %s: the count was wound back and the number taken again", name, t.First.Digest, t.Again.Digest)})
			}
		}

		_, kept := ch.kept[h.Digest]
		if newest != nil && (h.K > newest.c.K || h.K == newest.c.K && kept) {
			heardNewest = true
		}
		if kept {
			continue
		}

		switch others := ch.numbered(h.K); {
		case len(others) > 0:
			fs = append(fs, Finding{Kind: "rewritten", Checkpoint: h.K, Digest: others[0].d,
				Note: fmt.Sprintf("witness %s heard it as %s, and the store keeps it as something else", name, h.Digest)})
		default:
			note := fmt.Sprintf("witness %s heard it, and the store does not keep it", name)
			if newest == nil || newest.c.K < h.K {
				k := 0
				if newest != nil {
					k = newest.c.K
				}
				note += fmt.Sprintf(": the newest the store keeps is %d, and the chain was cut short there", k)
			}
			fs = append(fs, Finding{Kind: "unkept", Checkpoint: h.K, Digest: h.Digest, Note: note})
		}
	}

	if asked > 0 && newest != nil && !heardNewest && time.Since(newest.c.At) > p.hearWithin() {
		fs = append(fs, Finding{Kind: "unheard", Checkpoint: newest.c.K, Digest: newest.d,
			Note: fmt.Sprintf("taken %s ago, and no witness that answers heard it", time.Since(newest.c.At).Round(time.Minute))})
	}
	slices.SortFunc(fs, findingOrder)

	return fs
}
