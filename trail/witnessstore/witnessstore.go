// Package witnessstore is a witness that keeps every statement it hears in a
// flob store of its own (payday#115; see trail.Witness).
//
// Its worth is where the store is: somewhere the database's credentials and
// the archive's do not reach, and best a store that refuses deletion -- a
// WORM directory, a bucket under Object Lock -- so that what it heard stays
// heard whoever holds the deployment's. It only ever adds.
//
// It is read by the statements it holds, never by labels: a blob is what it
// holds, and one put there or relabelled by somebody is read for what it says,
// which a verification holds to the deployment's key.
package witnessstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lesomnus/flob"

	"github.com/lesomnus/payday/trail"
)

// Scheme is what `audit.checkpoints.witnesses` names one by: `store:` and a
// directory, `store:/mnt/worm/witness`.
const Scheme = "store"

// Namespace is where in the directory's flob store the statements are.
const Namespace = "heard"

// Witness keeps what it hears in a store.
type Witness struct {
	s flob.Store
}

var _ trail.Witness = (*Witness)(nil)

// New is a witness that keeps what it hears in `s`, which it only ever adds
// to. A store that cannot be listed is refused when it is first asked.
func New(s flob.Store) *Witness {
	return &Witness{s: s}
}

// Open is the witness `store:<dir>` names: a flob store on the disk, in that
// directory, for config's `RegisterWitness`.
func Open(v string) (*Witness, error) {
	dir, ok := strings.CutPrefix(v, Scheme+":")
	if !ok {
		return nil, fmt.Errorf("%s: names no %s witness", v, Scheme)
	}
	if dir == "" {
		return nil, errors.New("store: names no directory; `store:/mnt/worm/witness`")
	}

	return New(flob.NewOsStores(dir).Use(Namespace)), nil
}

// doc is a statement as the store keeps it.
type doc struct {
	Heard trail.Heard `json:"heard"`
}

// receipt is what Hear answers: where the statement is kept.
type receipt struct {
	Digest flob.Digest `json:"digest"`
}

// Hear keeps the statement, and when it was heard.
func (w *Witness) Hear(ctx context.Context, s trail.Signed) (trail.Witnessed, error) {
	b, err := json.Marshal(doc{Heard: trail.Heard{Signed: s, At: time.Now().UTC()}})
	if err != nil {
		return trail.Witnessed{}, err
	}

	// A hint for whoever lists the store by hand, which nothing here reads.
	l := flob.Labels{}
	l.Set("K", strconv.Itoa(s.K))
	if s.Chain != "" {
		l.Set("Chain", s.Chain)
	}

	d := flob.DigestFromBytes(b)
	if _, err := w.s.Add(ctx, flob.Meta{Digest: d, Labels: l}, bytes.NewReader(b)); err != nil && !errors.Is(err, flob.ErrAlreadyExists) {
		return trail.Witnessed{}, err
	}

	r, err := json.Marshal(receipt{Digest: d})
	if err != nil {
		return trail.Witnessed{}, err
	}

	return trail.Witnessed{Receipt: r}, nil
}

// Latest reads every statement the store keeps of `chain`, and answers the
// highest number, and of two of that number the one heard first -- and the
// number it heard two statements of most lately, its first statement and the
// last heard that was something else (trail.Heard.Twice). A blob that holds no
// statement, or whose bytes are not its digest, is passed over.
func (w *Witness) Latest(ctx context.Context, chain string) (trail.Heard, error) {
	walker, ok := flob.AsWalker(w.s)
	if !ok {
		return trail.Heard{}, errors.New("the witness's store cannot be listed")
	}

	// What each number was heard as, without the statements, which are read
	// again for the few that are answered.
	type seen struct {
		at     time.Time
		digest flob.Digest
		blob   flob.Digest
	}
	byK := map[int][]seen{}
	for v, err := range walker.Walk(ctx) {
		if err != nil {
			return trail.Heard{}, err
		}

		h, err := w.read(ctx, v.Digest())
		if err != nil {
			if errors.Is(err, flob.ErrNotExist) || errors.Is(err, errNoStatement) {
				continue
			}

			return trail.Heard{}, err
		}
		if h.Chain != chain || h.K < 1 {
			continue
		}

		byK[h.K] = append(byK[h.K], seen{h.At, h.Digest, v.Digest()})
	}
	if len(byK) == 0 {
		return trail.Heard{}, nil
	}

	ks := slices.Sorted(maps.Keys(byK))
	for _, k := range ks {
		slices.SortFunc(byK[k], func(a, b seen) int { return a.at.Compare(b.at) })
	}

	out, err := w.read(ctx, byK[ks[len(ks)-1]][0].blob)
	if err != nil {
		return trail.Heard{}, err
	}

	// Of the numbers heard as two statements, the one heard so most lately:
	// its first statement, and the last heard that was something else.
	var first, again *seen
	for _, k := range ks {
		vs := byK[k]
		for i := range vs[1:] {
			v := &vs[i+1]
			if v.digest != vs[0].digest && (again == nil || v.at.After(again.at)) {
				first, again = &vs[0], v
			}
		}
	}
	if again != nil {
		f, err := w.read(ctx, first.blob)
		if err != nil {
			return trail.Heard{}, err
		}
		a, err := w.read(ctx, again.blob)
		if err != nil {
			return trail.Heard{}, err
		}
		out.Twice = &trail.Twice{First: f, Again: a}
	}

	return out, nil
}

// errNoStatement is a blob of the store that holds no statement.
var errNoStatement = errors.New("no statement")

// read reads one statement, and only while its bytes are its digest.
func (w *Witness) read(ctx context.Context, d flob.Digest) (trail.Heard, error) {
	r, _, err := w.s.Open(ctx, d)
	if err != nil {
		return trail.Heard{}, err
	}
	defer r.Close()

	b, err := io.ReadAll(r)
	if err != nil {
		return trail.Heard{}, err
	}
	v := d.Verifier()
	_, _ = v.Write(b)
	if !v.Verified() {
		return trail.Heard{}, errNoStatement
	}

	var h doc
	if err := json.Unmarshal(b, &h); err != nil || h.Heard.Digest == "" {
		return trail.Heard{}, errNoStatement
	}

	return h.Heard, nil
}
