package trail

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/otx/log"
)

// CheckpointNamespace is where an archive's checkpoints are kept when the
// policy names no store of their own; see [Policy.Checkpoints].
const CheckpointNamespace = "_checkpoints"

// The states of a row of the manifest; see [Archived].
const (
	Adding  = "adding"
	Present = "present"
	Erasing = "erasing"
	Erased  = "erased"
)

const (
	labelIntent = "Intent"
	labelK      = "K"

	formatCheckpoint = "checkpoint+json+gzip"
)

// Archived is one row of the manifest: one blob of the archive, as the
// database keeps the account of it.
//
// # Why the database keeps one
//
// A chunk is named by its digest, so its bytes cannot change without its name
// changing. What its name does not say is whether the archive still holds
// **what it should**: a chunk taken away, or one put there by somebody who is
// not the deployment, looks like any other. A record of what should be there
// tells them apart, and it is worth something only where the archive's
// credentials do not reach. The database is that place, and it already does the
// rest of the work: its transactions put two writers in order, and its rows are
// what a crash leaves behind to say what was in flight.
//
// # Written before the archive changes, and settled after
//
// Every change to the archive goes through it, by the archive an act is handed
// -- see [Policy.Pass] -- so no act can change the archive without it:
//
//   - an add is a row in [Adding] before the blob is written, the blob labelled
//     with the row's intent, and the row [Present] once the store holds it;
//   - an erase is [Erasing] before the blob goes, and [Erased] after.
//
// A crash between the halves leaves a row that says what was happening, and
// the next pass settles it from what the store holds. That is why nothing
// [Policy.Verify] compares needs a clock to tell an act in flight from one
// that never happened.
type Archived struct {
	// Key is the row, as the store gave it out.
	Key any

	Namespace string

	// Digest is the blob's, as flob writes one, and empty while it is
	// being added.
	Digest string

	// Intent is what the blob was labelled with when its row was added.
	Intent string

	// Labels is the blob's, as they were when it was written or relabelled.
	Labels map[string]string

	State string

	// Since is the first checkpoint the row was in, and Gone the first one it
	// was not in once it was erased; zero is none.
	Since int
	Gone  int

	// Created is when the row was added, which is how long an add has been
	// in flight.
	Created time.Time
}

// Manifest is the database's half of the archive: its account of what the
// archive holds. The generated store keeps it on payday's `Archived` entity.
type Manifest interface {
	// Adding adds a row for a blob about to be written into a namespace,
	// labelled `intent`, and answers the row's key.
	Adding(ctx context.Context, namespace, intent string, labels map[string]string) (any, error)

	// Added says the row's blob is in the archive, by its digest and the labels
	// it carries.
	Added(ctx context.Context, key any, digest string, labels map[string]string) error

	// Drop removes a row whose blob was never written.
	Drop(ctx context.Context, key any) error

	// Mark moves every row of a blob that is in state `from` to state `to`.
	Mark(ctx context.Context, namespace, digest, from, to string) error

	// Label records the labels a blob carries now.
	Label(ctx context.Context, namespace, digest string, labels map[string]string) error

	// Rows answers every row that is not erased.
	Rows(ctx context.Context) ([]Archived, error)

	// Checkpoint numbers the next checkpoint and answers it with the rows in
	// it: every row present or erasing at that moment. Two checkpoints never
	// share a number, and the rows read back by [Manifest.At] for the latest
	// two are the same rows.
	Checkpoint(ctx context.Context) (int, []Archived, error)

	// At answers the rows a checkpoint was taken over, for the latest two.
	At(ctx context.Context, k int) ([]Archived, error)

	// Latest is the number of the latest checkpoint, and zero before one.
	Latest(ctx context.Context) (int, error)
}

// manifested is the archive with every change written into the manifest
// first.
//
// A decorator rather than a call at every place a pass writes, so that a way
// of changing the archive added later goes through the manifest without
// anybody remembering to.
type manifested struct {
	a flob.Stores
	m Manifest

	mu    sync.Mutex
	added map[blobKey]bool
}

func (p Policy) manifested(m Manifest) *manifested {
	return &manifested{a: p.Archive, m: m, added: map[blobKey]bool{}}
}

func (r *manifested) Use(ns string) flob.Store {
	return manifestedStore{Store: r.a.Use(ns), ns: ns, r: r}
}

// Unwrap is what keeps the capabilities of the store beneath -- listing,
// reclaiming -- reachable through this.
func (r *manifested) Unwrap() flob.Stores { return r.a }

// wrote answers whether a blob was added through this archive.
func (r *manifested) wrote(k blobKey) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.added[k]
}

type manifestedStore struct {
	flob.Store
	ns string
	r  *manifested
}

func (s manifestedStore) Unwrap() flob.Store { return s.Store }

// Add writes the row first, and the blob labelled with the row's intent. A
// blob the store already held -- the same rows, written by another pass -- is
// settled with the labels the store kept, which are the other pass's.
func (s manifestedStore) Add(ctx context.Context, m flob.Meta, r io.Reader) (flob.Meta, error) {
	intent, err := newRun()
	if err != nil {
		return flob.Meta{}, err
	}

	l := flob.Labels{}
	for k, vs := range m.Labels {
		l[k] = slices.Clone(vs)
	}
	l.Set(labelIntent, intent)

	key, err := s.r.m.Adding(ctx, s.ns, intent, labelsOf(l))
	if err != nil {
		return flob.Meta{}, fmt.Errorf("the manifest: %w", err)
	}

	v, err := s.Store.Add(ctx, flob.Meta{Digest: m.Digest, Labels: l}, r)
	switch {
	case err == nil:
	case errors.Is(err, flob.ErrAlreadyExists):
		if info, serr := s.Store.Stat(ctx, v.Digest); serr == nil {
			if kept, lerr := info.Labels(ctx); lerr == nil {
				l = kept
			}
		}
	default:
		if derr := s.r.m.Drop(ctx, key); derr != nil {
			log.From(ctx).WarnContext(ctx, "trail: the manifest", "err", derr)
		}

		return v, err
	}

	if merr := s.r.m.Added(ctx, key, string(v.Digest), labelsOf(l)); merr != nil {
		// The blob is there and its row says adding, which the next pass
		// settles by the intent it is labelled with.
		log.From(ctx).WarnContext(ctx, "trail: the manifest", "err", merr)
	}

	s.r.mu.Lock()
	s.r.added[blobKey{s.ns, v.Digest}] = true
	s.r.mu.Unlock()

	return v, err
}

func (s manifestedStore) Label(ctx context.Context, d flob.Digest, l flob.Labels) error {
	if err := s.Store.Label(ctx, d, l); err != nil {
		return err
	}

	return s.r.m.Label(ctx, s.ns, string(d), labelsOf(l))
}

// Erase marks the row first. An erase the store refused is marked back, for
// the act that asked to decide again.
func (s manifestedStore) Erase(ctx context.Context, d flob.Digest) error {
	if err := s.r.m.Mark(ctx, s.ns, string(d), Present, Erasing); err != nil {
		return fmt.Errorf("the manifest: %w", err)
	}

	if err := s.Store.Erase(ctx, d); err != nil {
		if merr := s.r.m.Mark(ctx, s.ns, string(d), Erasing, Present); merr != nil {
			log.From(ctx).WarnContext(ctx, "trail: the manifest", "err", merr)
		}

		return err
	}

	return s.r.m.Mark(ctx, s.ns, string(d), Erasing, Erased)
}

// adopted records a blob put into the archive beneath the manifest -- a hard
// link, which [flob.OsStore.Adopt] makes without the decorator seeing it.
func adopted(ctx context.Context, s flob.Store, d flob.Digest, l flob.Labels) error {
	r, ok := s.(manifestedStore)
	if !ok {
		return nil
	}

	key, err := r.r.m.Adding(ctx, r.ns, "", labelsOf(l))
	if err != nil {
		return err
	}
	if err := r.r.m.Added(ctx, key, string(d), labelsOf(l)); err != nil {
		return err
	}

	r.r.mu.Lock()
	r.r.added[blobKey{r.ns, d}] = true
	r.r.mu.Unlock()

	return nil
}

// unwrapStores is the archive beneath a manifest.
func unwrapStores(a flob.Stores) flob.Stores {
	if r, ok := a.(*manifested); ok {
		return r.a
	}

	return a
}

// labelsOf is the labels a row records.
func labelsOf(l flob.Labels) map[string]string {
	out := map[string]string{}
	for k := range l {
		if v := l.Get(k); v != "" {
			out[k] = v
		}
	}

	return out
}

type blobKey struct {
	ns string
	d  flob.Digest
}

func blobOrder(a, b blobKey) int { return cmp.Or(cmp.Compare(a.ns, b.ns), cmp.Compare(a.d, b.d)) }

// inventory is every blob the archive holds outside its checkpoints, with its
// labels.
func inventory(ctx context.Context, a flob.Stores) (map[blobKey]map[string]string, error) {
	nss, err := namespaces(ctx, a)
	if err != nil {
		return nil, err
	}

	out := map[blobKey]map[string]string{}
	for _, ns := range nss {
		if ns == CheckpointNamespace {
			continue
		}

		w, ok := flob.AsWalker(a.Use(ns))
		if !ok {
			return nil, errUnwalkable
		}
		for v, err := range w.Walk(ctx) {
			if err != nil {
				return nil, err
			}

			l, err := v.Labels(ctx)
			if err != nil {
				if errors.Is(err, flob.ErrNotExist) {
					continue
				}

				return nil, err
			}

			out[blobKey{ns, v.Digest()}] = labelsOf(l)
		}
	}

	return out, nil
}

// settle finishes what a crash left in flight, and fills the manifest the
// first time from what the archive holds.
//
// An add whose blob is there, labelled with its intent, is present, and one
// whose blob is not is dropped once it is a day old -- what is younger may be
// another pass still writing. An erase whose blob is gone is erased, and one
// whose blob is still there is **undone**: the act that marked it did not
// finish, and the pass decides again, so a row in the database alone can never
// make a pass destroy anything.
func (p Policy) settle(ctx context.Context, m Manifest) error {
	rows, err := m.Rows(ctx)
	if err != nil {
		return err
	}
	inv, err := inventory(ctx, p.Archive)
	if err != nil {
		return err
	}

	if len(rows) == 0 && len(inv) > 0 {
		return p.fill(ctx, m, inv)
	}

	intents := map[string]blobKey{}
	for k, l := range inv {
		if v := l[labelIntent]; v != "" {
			intents[k.ns+"\x00"+v] = k
		}
	}

	for _, v := range rows {
		switch v.State {
		case Adding:
			if k, ok := intents[v.Namespace+"\x00"+v.Intent]; ok && v.Intent != "" {
				err = errors.Join(err, m.Added(ctx, v.Key, string(k.d), inv[k]))
			} else if time.Since(v.Created) > Swept {
				err = errors.Join(err, m.Drop(ctx, v.Key))
			}
		case Erasing:
			if _, ok := inv[blobKey{v.Namespace, flob.Digest(v.Digest)}]; ok {
				err = errors.Join(err, m.Mark(ctx, v.Namespace, v.Digest, Erasing, Present))
			} else {
				err = errors.Join(err, m.Mark(ctx, v.Namespace, v.Digest, Erasing, Erased))
			}
		}
	}

	return err
}

// fill is the first time: every blob the archive holds goes into the manifest
// as it is. Refused when the archive has checkpoints, since an empty manifest
// beside them is a database restored from before them -- which an operator
// looks at, and [Policy.Accept] is for.
func (p Policy) fill(ctx context.Context, m Manifest, inv map[blobKey]map[string]string) error {
	if k, err := m.Latest(ctx); err != nil {
		return err
	} else if k > 0 {
		return nil
	}

	cs, err := checkpointsIn(ctx, p.checkpoints())
	if err != nil {
		return err
	}
	if len(cs) > 0 {
		log.From(ctx).WarnContext(ctx, "trail: the manifest is empty and the archive has checkpoints, so it is not filled from what the archive holds",
			"checkpoints", len(cs), "see", "`trail verify`")

		return nil
	}

	for _, k := range slices.SortedFunc(maps.Keys(inv), blobOrder) {
		key, err := m.Adding(ctx, k.ns, "", inv[k])
		if err != nil {
			return err
		}
		if err := m.Added(ctx, key, string(k.d), inv[k]); err != nil {
			return err
		}
	}

	log.From(ctx).InfoContext(ctx, "trail: the manifest, filled from what the archive holds", "blobs", len(inv))

	return nil
}

// Finding is something about the archive that its manifest does not account
// for.
type Finding struct {
	// Kind is what was found:
	//
	//	missing      a blob the manifest says the archive holds, and it does not
	//	unaccounted  a blob the archive holds that the manifest never added
	//	relabelled   a blob whose labels are not the ones the manifest recorded
	//	altered      a blob whose bytes are not its digest any more
	//	rewritten    a checkpoint the manifest's rows no longer add up to
	//	unkept       a checkpoint the database numbered that the store does not keep
	//	forged       a checkpoint whose signature is bad, or made by a key nobody trusts
	//	unsigned     a checkpoint with no signature, after one that had one
	//	emptied      a manifest with no rows beside an archive with checkpoints
	//	behind       a checkpoint the store keeps that the database never numbered
	Kind string `json:"kind"`

	Namespace string      `json:"ns,omitempty"`
	Digest    flob.Digest `json:"digest,omitempty"`

	// Checkpoint is the checkpoint it is about, for the findings about one.
	Checkpoint int `json:"checkpoint,omitempty"`

	Note string `json:"note,omitempty"`
}

func (f Finding) String() string {
	v := f.Kind
	if f.Namespace != "" {
		v += " " + f.Namespace
	}
	if f.Digest != "" {
		v += " " + string(f.Digest)
	}
	if f.Checkpoint > 0 {
		v += " checkpoint " + strconv.Itoa(f.Checkpoint)
	}
	if f.Note != "" {
		v += ": " + f.Note
	}

	return v
}

// Verified is what a verification read, and what it found.
type Verified struct {
	// Rows is the manifest's rows, Blobs the archive's blobs outside its
	// checkpoints, and Hashed how many of them were read whole, which only a
	// full verification does. Checkpoint is the latest checkpoint, compared.
	Rows       int `json:"rows"`
	Blobs      int `json:"blobs"`
	Hashed     int `json:"hashed"`
	Checkpoint int `json:"checkpoint,omitempty"`

	// Findings is everything that did not add up: any of them is a reason to
	// look.
	Findings []Finding `json:"findings,omitempty"`

	// known is the archive's blobs the manifest accounts for.
	known map[blobKey]bool
}

// Ok answers whether there was nothing to find.
func (v Verified) Ok() bool { return len(v.Findings) == 0 }

// Verify compares the archive with its manifest, and the latest checkpoint
// with the rows it was taken over.
//
// The manifest is read before the archive is listed and again after, so that
// an act that changed both in between -- a pass on another replica -- is
// accounted for by one reading or the other. A blob being written has a row
// in [Adding] and is labelled with its intent; one being erased has a row in
// [Erasing]. Neither is a finding, and no clock is asked. `full` also reads
// every blob whole and checks its bytes against its digest.
//
// # Which checkpoint signatures count
//
// The ones made by [Policy.Key] or by a key in [Policy.Trust]; one signed by
// any other key is forged. One with no signature is a finding only after one
// that had a signature: a deployment that gave itself a key later has
// checkpoints that begin unsigned.
func (p Policy) Verify(ctx context.Context, s Store, full bool) (Verified, error) {
	out := Verified{known: map[blobKey]bool{}}
	if p.Archive == nil {
		return out, errors.New("no archive to verify")
	}
	a := unwrapStores(p.Archive)

	before, err := s.Rows(ctx)
	if err != nil {
		return out, fmt.Errorf("the manifest: %w", err)
	}
	inv, err := inventory(ctx, a)
	if err != nil {
		return out, err
	}
	after, err := s.Rows(ctx)
	if err != nil {
		return out, fmt.Errorf("the manifest: %w", err)
	}
	out.Rows, out.Blobs = len(after), len(inv)

	type held struct{ before, after map[blobKey]Archived }
	live := held{map[blobKey]Archived{}, map[blobKey]Archived{}}
	intents := map[string]bool{}
	for i, rows := range [][]Archived{before, after} {
		m := live.before
		if i == 1 {
			m = live.after
		}
		for _, v := range rows {
			switch v.State {
			case Adding:
				intents[v.Namespace+"\x00"+v.Intent] = true
			case Present, Erasing:
				m[blobKey{v.Namespace, flob.Digest(v.Digest)}] = v
			}
		}
	}

	for _, k := range slices.SortedFunc(maps.Keys(inv), blobOrder) {
		l := inv[k]
		v, ok := live.after[k]
		if !ok {
			v, ok = live.before[k]
		}
		switch {
		case ok:
			out.known[k] = true
			if v.State == Present && !sameLabels(v.Labels, l) && p.relabelled(ctx, s, a, k, v) {
				out.Findings = append(out.Findings, Finding{Kind: "relabelled", Namespace: k.ns, Digest: k.d, Note: labelDiff(v.Labels, l)})
			}
		case l[labelIntent] != "" && intents[k.ns+"\x00"+l[labelIntent]]:
			// Being written, or written by an act a crash stopped.
			out.known[k] = true
		default:
			out.Findings = append(out.Findings, Finding{Kind: "unaccounted", Namespace: k.ns, Digest: k.d})
		}
	}
	for _, k := range slices.SortedFunc(maps.Keys(live.before), blobOrder) {
		if _, ok := inv[k]; ok || live.before[k].State != Present {
			continue
		}
		if v, ok := live.after[k]; !ok || v.State != Present {
			// Erased while the archive was listed.
			continue
		}

		out.Findings = append(out.Findings, Finding{Kind: "missing", Namespace: k.ns, Digest: k.d})
	}

	if full {
		for _, k := range slices.SortedFunc(maps.Keys(inv), blobOrder) {
			if err := ctx.Err(); err != nil {
				return out, err
			}

			ok, err := intact(ctx, a.Use(k.ns), k.d)
			if err != nil {
				return out, fmt.Errorf("%s %s: %w", k.ns, k.d, err)
			}

			out.Hashed++
			if !ok {
				out.Findings = append(out.Findings, Finding{Kind: "altered", Namespace: k.ns, Digest: k.d})
			}
		}
	}

	fs, err := p.verifyCheckpoints(ctx, s, len(after) == 0, &out)
	if err != nil {
		return out, err
	}
	out.Findings = append(out.Findings, fs...)

	return out, nil
}

// relabelled reads one blob's labels again, from both sides, and answers
// whether they still disagree: what was listed a while ago may have been
// relabelled by the act that wrote it since.
func (p Policy) relabelled(ctx context.Context, s Store, a flob.Stores, k blobKey, v Archived) bool {
	info, err := a.Use(k.ns).Stat(ctx, k.d)
	if err != nil {
		return true
	}
	l, err := info.Labels(ctx)
	if err != nil {
		return true
	}

	rows, err := s.Rows(ctx)
	if err != nil {
		return true
	}
	for _, r := range rows {
		if r.Namespace == k.ns && r.Digest == string(k.d) && r.State == Present {
			v = r
		}
	}

	return !sameLabels(v.Labels, labelsOf(l))
}

func sameLabels(want, got map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}

	return true
}

func labelDiff(want, got map[string]string) string {
	vs := []string{}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if got[k] != want[k] {
			vs = append(vs, fmt.Sprintf("%s %q, and the manifest says %q", k, got[k], want[k]))
		}
	}

	return strings.Join(vs, "; ")
}

// intact answers whether a blob's bytes are still what its name says.
func intact(ctx context.Context, s flob.Store, d flob.Digest) (bool, error) {
	r, _, err := s.Open(ctx, d)
	if err != nil {
		if errors.Is(err, flob.ErrNotExist) {
			return false, nil
		}

		return false, err
	}
	defer r.Close()

	v := d.Verifier()
	if _, err := io.Copy(v, r); err != nil {
		return false, err
	}

	return v.Verified(), nil
}

// Checkpoint is what the archive held at one moment, as a checkpoint keeps it:
// every blob the manifest had, which a store that refuses deletion keeps
// beyond the reach of anybody who can write the database and the archive.
type Checkpoint struct {
	K      int         `json:"k"`
	At     time.Time   `json:"at"`
	Count  int         `json:"count"`
	Digest flob.Digest `json:"digest"`
	Blobs  []Kept      `json:"blobs"`
}

// Kept is one blob of a checkpoint.
type Kept struct {
	Namespace string      `json:"ns"`
	Digest    flob.Digest `json:"digest"`
}

// signed is a checkpoint as it is stored: its bytes, and the signature over
// exactly those bytes with the public half of the key that made it.
type signed struct {
	Checkpoint json.RawMessage `json:"checkpoint"`
	Key        []byte          `json:"key,omitempty"`
	Sig        []byte          `json:"sig,omitempty"`
}

func checkpointOf(k int, at time.Time, rows []Archived) Checkpoint {
	c := Checkpoint{K: k, At: at.UTC(), Blobs: []Kept{}}
	seen := map[blobKey]bool{}
	for _, v := range rows {
		b := blobKey{v.Namespace, flob.Digest(v.Digest)}
		if seen[b] {
			continue
		}

		seen[b] = true
		c.Blobs = append(c.Blobs, Kept{Namespace: v.Namespace, Digest: b.d})
	}
	slices.SortFunc(c.Blobs, func(a, b Kept) int { return blobOrder(blobKey{a.Namespace, a.Digest}, blobKey{b.Namespace, b.Digest}) })
	c.Count = len(c.Blobs)
	c.Digest = digestOf(c.Blobs)

	return c
}

// digestOf is a set of blobs as one digest: each blob a line, in order.
func digestOf(vs []Kept) flob.Digest {
	b := &bytes.Buffer{}
	for _, v := range vs {
		b.WriteString(v.Namespace)
		b.WriteByte(' ')
		b.WriteString(string(v.Digest))
		b.WriteByte('\n')
	}

	return flob.DigestFromBytes(b.Bytes())
}

// checkpoints is where checkpoints go: the policy's own store, or the
// archive's [CheckpointNamespace].
func (p Policy) checkpoints() flob.Store {
	if p.Checkpoints != nil {
		return p.Checkpoints
	}

	return unwrapStores(p.Archive).Use(CheckpointNamespace)
}

// errBehind is a database whose checkpoint numbers are behind the store's.
var errBehind = errors.New("the checkpoint store has checkpoints the database never numbered: a database restored from before them; see `trail verify`")

// checkpoint ends a pass: the next number, and every blob the manifest has,
// written to the checkpoint store and signed when the policy has a key.
//
// Not while the store keeps a checkpoint the database has not numbered yet.
// That is a database restored from before it, and a checkpoint written then
// would take a number one already has.
func (p Policy) checkpoint(ctx context.Context, m Manifest) (Checkpoint, error) {
	ks, err := checkpointsIn(ctx, p.checkpoints())
	if err != nil {
		return Checkpoint{}, err
	}
	latest, err := m.Latest(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	if len(ks) > 0 && slices.Max(slices.Collect(maps.Keys(ks))) > latest {
		return Checkpoint{}, errBehind
	}

	k, rows, err := m.Checkpoint(ctx)
	if err != nil {
		return Checkpoint{}, err
	}

	c := checkpointOf(k, time.Now(), rows)
	b, err := json.Marshal(c)
	if err != nil {
		return c, err
	}

	v := signed{Checkpoint: b}
	if p.Key != nil {
		v.Key = p.Key.Public().(ed25519.PublicKey)
		v.Sig = ed25519.Sign(p.Key, b)
	}
	doc, err := json.Marshal(v)
	if err != nil {
		return c, err
	}

	buf := &bytes.Buffer{}
	z := gzip.NewWriter(buf)
	if _, err := z.Write(doc); err != nil {
		return c, err
	}
	if err := z.Close(); err != nil {
		return c, err
	}

	l := flob.Labels{}
	l.Set(labelFormat, formatCheckpoint)
	l.Set(labelK, strconv.Itoa(k))
	l.Set(labelAt, c.At.Format(time.RFC3339Nano))
	if _, err := p.checkpoints().Add(ctx, flob.Meta{Labels: l}, buf); err != nil && !errors.Is(err, flob.ErrAlreadyExists) {
		return c, fmt.Errorf("checkpoint %d: %w", k, err)
	}

	return c, nil
}

// stored is a checkpoint as it was read back.
type stored struct {
	c Checkpoint
	s signed
	d flob.Digest
}

// checkpointsIn is every checkpoint a store keeps, by number.
func checkpointsIn(ctx context.Context, s flob.Store) (map[int]flob.Digest, error) {
	w, ok := flob.AsWalker(s)
	if !ok {
		return nil, errUnwalkable
	}

	out := map[int]flob.Digest{}
	for v, err := range w.Walk(ctx) {
		if err != nil {
			return nil, err
		}

		l, err := v.Labels(ctx)
		if err != nil {
			if errors.Is(err, flob.ErrNotExist) {
				continue
			}

			return nil, err
		}
		if l.Get(labelFormat) != formatCheckpoint {
			continue
		}
		if k, err := strconv.Atoi(l.Get(labelK)); err == nil {
			out[k] = v.Digest()
		}
	}

	return out, nil
}

func readCheckpoint(ctx context.Context, s flob.Store, d flob.Digest) (stored, error) {
	out := stored{d: d}

	r, _, err := s.Open(ctx, d)
	if err != nil {
		return out, err
	}
	defer r.Close()

	z, err := gzip.NewReader(r)
	if err != nil {
		return out, err
	}
	defer z.Close()

	b, err := io.ReadAll(z)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out.s); err != nil {
		return out, err
	}
	if err := json.Unmarshal(out.s.Checkpoint, &out.c); err != nil {
		return out, err
	}

	return out, nil
}

// ReadCheckpoints reads every checkpoint the policy's checkpoint store keeps,
// oldest first: what the archive held, day by day.
func (p Policy) ReadCheckpoints(ctx context.Context, fn func(Checkpoint) error) error {
	s := p.checkpoints()
	ks, err := checkpointsIn(ctx, s)
	if err != nil {
		return err
	}

	for _, k := range slices.Sorted(maps.Keys(ks)) {
		v, err := readCheckpoint(ctx, s, ks[k])
		if err != nil {
			return fmt.Errorf("checkpoint %d: %w", k, err)
		}
		if err := fn(v.c); err != nil {
			return err
		}
	}

	return nil
}

// verifyCheckpoints compares the latest checkpoint with the rows it was taken
// over, and checks the signatures of every checkpoint the store keeps.
func (p Policy) verifyCheckpoints(ctx context.Context, s Store, empty bool, out *Verified) ([]Finding, error) {
	fs := []Finding{}

	store := p.checkpoints()
	ks, err := checkpointsIn(ctx, store)
	if err != nil {
		return nil, err
	}
	latest, err := s.Latest(ctx)
	if err != nil {
		return nil, fmt.Errorf("the manifest: %w", err)
	}

	if empty && len(ks) > 0 {
		fs = append(fs, Finding{Kind: "emptied", Note: fmt.Sprintf("the archive has %d checkpoints and the manifest no rows: a database restored from before them?", len(ks))})
	}
	if len(ks) > 0 {
		if k := slices.Max(slices.Collect(maps.Keys(ks))); k > latest {
			fs = append(fs, Finding{Kind: "behind", Checkpoint: k, Note: fmt.Sprintf("the database has numbered %d checkpoints, and the store keeps one numbered %d: a database restored from before it", latest, k)})
		}
	}
	if latest > 0 {
		if _, ok := ks[latest]; !ok {
			fs = append(fs, Finding{Kind: "unkept", Checkpoint: latest, Note: "the database numbered it, and the checkpoint store does not keep it"})
		}
	}

	trusted := slices.Clone(p.Trust)
	if p.Key != nil {
		trusted = append(trusted, p.Key.Public().(ed25519.PublicKey))
	}

	signedOnce := false
	newest := 0
	for _, k := range slices.Sorted(maps.Keys(ks)) {
		v, err := readCheckpoint(ctx, store, ks[k])
		if err != nil {
			fs = append(fs, Finding{Kind: "forged", Checkpoint: k, Note: "it does not read: " + err.Error()})
			continue
		}

		switch has, good := signature(v.s); {
		case has && !good:
			fs = append(fs, Finding{Kind: "forged", Checkpoint: k, Note: "its signature is not good"})
		case has && len(trusted) > 0 && !slices.ContainsFunc(trusted, func(t ed25519.PublicKey) bool { return t.Equal(ed25519.PublicKey(v.s.Key)) }):
			fs = append(fs, Finding{Kind: "forged", Checkpoint: k, Note: "signed by a key nobody trusts: " + PublicKeyString(v.s.Key)})
		case has:
			signedOnce = true
		case signedOnce:
			fs = append(fs, Finding{Kind: "unsigned", Checkpoint: k, Note: "unsigned, after one that was signed"})
		}

		// The latest two are what the manifest's rows still add up to.
		if k < latest-1 || k > latest {
			continue
		}
		rows, err := s.At(ctx, k)
		if err != nil {
			return nil, fmt.Errorf("the manifest: %w", err)
		}
		if got := checkpointOf(k, v.c.At, rows); got.Digest != v.c.Digest {
			fs = append(fs, Finding{Kind: "rewritten", Checkpoint: k, Note: checkpointDiff(v.c, got)})
		}
		newest = max(newest, k)
	}
	out.Checkpoint = newest

	return fs, nil
}

func signature(v signed) (bool, bool) {
	if len(v.Sig) == 0 {
		return false, false
	}
	if len(v.Key) != ed25519.PublicKeySize {
		return true, false
	}

	return true, ed25519.Verify(ed25519.PublicKey(v.Key), v.Checkpoint, v.Sig)
}

// checkpointDiff says how the rows differ from what a checkpoint kept.
func checkpointDiff(was, is Checkpoint) string {
	a, b := map[Kept]bool{}, map[Kept]bool{}
	for _, v := range was.Blobs {
		a[v] = true
	}
	for _, v := range is.Blobs {
		b[v] = true
	}

	gone, came := 0, 0
	for v := range a {
		if !b[v] {
			gone++
		}
	}
	for v := range b {
		if !a[v] {
			came++
		}
	}

	return fmt.Sprintf("it kept %d blobs; of them the manifest has lost %d, and gained %d it never had", was.Count, gone, came)
}

// Accept makes the manifest say what the archive holds, for an operator who
// has looked at what [Policy.Verify] found and decided the archive is right:
// a pass a crash stopped, a blob restored from a backup.
//
// It adds the blobs the archive holds and the manifest does not, takes out the
// ones it says the archive holds and it does not, and records the labels the
// archive's blobs carry. A checkpoint is what it is, and Accept does nothing
// about one; the next checkpoint is taken over the manifest as it is now.
func (p Policy) Accept(ctx context.Context, s Store, why string) (Verified, error) {
	if strings.TrimSpace(why) == "" {
		return Verified{}, errors.New("an acceptance says why")
	}

	v, err := p.Verify(ctx, s, false)
	if err != nil {
		return v, err
	}

	inv, err := inventory(ctx, unwrapStores(p.Archive))
	if err != nil {
		return v, err
	}

	for _, f := range v.Findings {
		k := blobKey{f.Namespace, f.Digest}
		switch f.Kind {
		case "unaccounted":
			key, aerr := s.Adding(ctx, k.ns, "", inv[k])
			if aerr == nil {
				aerr = s.Added(ctx, key, string(k.d), inv[k])
			}
			err = errors.Join(err, aerr)
		case "relabelled":
			err = errors.Join(err, s.Label(ctx, k.ns, string(k.d), inv[k]))
		case "missing":
			err = errors.Join(err, s.Mark(ctx, k.ns, string(k.d), Present, Erased))
		}
	}

	log.From(ctx).InfoContext(ctx, "trail: the manifest, made to say what the archive holds", "why", why, "findings", len(v.Findings))

	return v, err
}

// GenerateKey makes a key a deployment signs its checkpoints with, as the PEM
// document [ParseKey] reads, and its public half as [Policy.Trust] lists it.
func GenerateKey() ([]byte, string, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, "", err
	}

	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), PublicKeyString(pub), nil
}

// ParseKey reads an Ed25519 private key from a PEM document, PKCS #8 as
// `openssl genpkey -algorithm ed25519` writes one.
func ParseKey(doc []byte) (ed25519.PrivateKey, error) {
	b, _ := pem.Decode(doc)
	if b == nil {
		return nil, errors.New("not a PEM document")
	}

	v, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}

	key, ok := v.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("a %T, and checkpoints are signed with Ed25519", v)
	}

	return key, nil
}

// PublicKeyString is a public key as a configuration lists one.
func PublicKeyString(k ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(k)
}

// ParsePublicKey reads one back.
func ParsePublicKey(v string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%d bytes, and an Ed25519 public key is %d", len(b), ed25519.PublicKeySize)
	}

	return ed25519.PublicKey(b), nil
}
