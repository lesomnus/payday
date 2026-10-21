package trail

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
// A crash between the halves leaves a row that says what was happening, and a
// pass settles it from what the store holds -- once it is old enough not to be
// another pass still at it. That is why nothing [Policy.Verify] compares needs
// a clock to tell an act in flight from one that never happened.
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
	// in flight, and Updated when it last changed, which is how long an erase
	// has.
	Created time.Time
	Updated time.Time

	// Signature is the deployment's key's signature over what the row says,
	// and empty on a row written without a key; see [Signature].
	Signature Signature
}

// Manifest is the database's half of the archive: its account of what the
// archive holds. The generated store keeps it on payday's `Archived` entity.
//
// Every write is handed a [Sign], and asks it for the signature to write beside
// each row it writes, one row at a time: a row is signed over which row it is,
// so a key must print the same every time fmt prints it.
type Manifest interface {
	// Adding adds a row for a blob about to be written into a namespace,
	// labelled `intent`, and answers the row's key.
	Adding(ctx context.Context, namespace, intent string, labels map[string]string, sign Sign) (any, error)

	// Added says the row's blob is in the archive, by its digest and the labels
	// it carries.
	Added(ctx context.Context, key any, digest string, labels map[string]string, sign Sign) error

	// Drop removes a row whose blob was never written.
	Drop(ctx context.Context, key any) error

	// Mark moves every row of a blob that is in state `from` to state `to`,
	// each only while it is still in `from`. From a state to itself, it signs
	// the rows again as they are.
	Mark(ctx context.Context, namespace, digest, from, to string, sign Sign) error

	// Label records the labels a blob carries now.
	Label(ctx context.Context, namespace, digest string, labels map[string]string, sign Sign) error

	// Rows answers every row that is not erased.
	Rows(ctx context.Context) ([]Archived, error)

	// Checkpoint numbers the next checkpoint and answers it with the rows in
	// it: every row present or erasing at that moment. Two checkpoints never
	// share a number, and the rows read back by [Manifest.At] for the latest
	// two are the same rows.
	//
	// The next is after the latest, and after `after` as well when that is
	// higher: an acceptance of a database restored from before checkpoints
	// the store keeps is numbered past them (see [Policy.Accept]).
	Checkpoint(ctx context.Context, after int) (int, []Archived, error)

	// At answers the rows a checkpoint was taken over, for the latest two.
	At(ctx context.Context, k int) ([]Archived, error)

	// Latest is the number of the latest checkpoint and when it was taken,
	// and zero before one.
	Latest(ctx context.Context) (int, time.Time, error)
}

// manifested is the archive with every change written into the manifest
// first.
//
// A decorator rather than a call at every place a pass writes, so that a way
// of changing the archive added later goes through the manifest without
// anybody remembering to.
//
// It also keeps what the act trusts: the blobs the manifest accounts for, by
// the labels it recorded for each. Those labels are what an act decides by --
// a chunk's month, its kind, whose it is -- and not the ones on the blob, which
// anybody who can write the archive can change.
type manifested struct {
	a    flob.Stores
	m    Manifest
	sign Sign

	mu sync.Mutex

	// known is the blobs the manifest accounted for when the act loaded it,
	// and intents the adds in flight then, by namespace and intent; see
	// [pass.trust]. added is what the act wrote itself, as it wrote it.
	known   map[blobKey]map[string]string
	intents map[string]map[string]string
	added   map[blobKey]map[string]string
}

func (p Policy) manifested(m Manifest) *manifested {
	return &manifested{
		a:       p.Archive,
		m:       m,
		sign:    p.signing(),
		known:   map[blobKey]map[string]string{},
		intents: map[string]map[string]string{},
		added:   map[blobKey]map[string]string{},
	}
}

func (r *manifested) Use(ns string) flob.Store {
	return manifestedStore{Store: r.a.Use(ns), ns: ns, r: r}
}

// Unwrap is what keeps the capabilities of the store beneath -- listing,
// reclaiming -- reachable through this.
func (r *manifested) Unwrap() flob.Stores { return r.a }

// load is what the manifest accounts for, as an act read it.
func (r *manifested) load(known map[blobKey]map[string]string, intents map[string]map[string]string) {
	r.mu.Lock()
	r.known, r.intents = known, intents
	r.mu.Unlock()
}

// trusts answers the labels the manifest has for a blob: as the act wrote it,
// or as the act loaded it.
func (r *manifested) trusts(k blobKey) (map[string]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if l, ok := r.added[k]; ok {
		return l, true
	}

	l, ok := r.known[k]
	return l, ok
}

// inFlight answers the labels of an add in flight, by the intent its blob is
// labelled with.
func (r *manifested) inFlight(ns, intent string) (map[string]string, bool) {
	if intent == "" {
		return nil, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	l, ok := r.intents[ns+"\x00"+intent]
	return l, ok
}

func (r *manifested) remember(k blobKey, l map[string]string) {
	r.mu.Lock()
	r.added[k] = l
	r.mu.Unlock()
}

// relabel records new labels for a blob the act trusts, and nothing for one it
// does not: relabelling a blob through the manifest is no way into it.
func (r *manifested) relabel(k blobKey, l map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.added[k]; ok {
		r.added[k] = l
	} else if _, ok := r.known[k]; ok {
		r.known[k] = l
	}
}

func (r *manifested) forget(k blobKey) {
	r.mu.Lock()
	delete(r.added, k)
	delete(r.known, k)
	r.mu.Unlock()
}

type manifestedStore struct {
	flob.Store
	ns string
	r  *manifested
}

func (s manifestedStore) Unwrap() flob.Store { return s.Store }

// Add writes the row first, and the blob labelled with the row's intent.
//
// A blob the store already held -- the same rows, written by another pass at
// the same moment -- is that pass's to account for, and the row is dropped:
// the blob's own rows say what becomes of it, including the erase that pass
// may be making of it right now, which a row of this one's would outlive.
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

	key, err := s.r.m.Adding(ctx, s.ns, intent, labelsOf(l), s.r.sign)
	if err != nil {
		return flob.Meta{}, fmt.Errorf("the manifest: %w", err)
	}

	v, err := s.Store.Add(ctx, flob.Meta{Digest: m.Digest, Labels: l}, r)
	if err != nil {
		if derr := s.r.m.Drop(ctx, key); derr != nil {
			log.From(ctx).WarnContext(ctx, "trail: the manifest", "err", derr)
		}

		return v, err
	}

	if merr := s.r.m.Added(ctx, key, string(v.Digest), labelsOf(l), s.r.sign); merr != nil {
		// The blob is there and its row says adding, which the next pass
		// settles by the intent it is labelled with.
		log.From(ctx).WarnContext(ctx, "trail: the manifest", "err", merr)
	}

	s.r.remember(blobKey{s.ns, v.Digest}, labelsOf(l))

	return v, nil
}

func (s manifestedStore) Label(ctx context.Context, d flob.Digest, l flob.Labels) error {
	if err := s.Store.Label(ctx, d, l); err != nil {
		return err
	}

	s.r.relabel(blobKey{s.ns, d}, labelsOf(l))

	return s.r.m.Label(ctx, s.ns, string(d), labelsOf(l), s.r.sign)
}

// Erase marks the row first. An erase the store refused is marked back, for
// the act that asked to decide again.
func (s manifestedStore) Erase(ctx context.Context, d flob.Digest) error {
	if err := s.r.m.Mark(ctx, s.ns, string(d), Present, Erasing, s.r.sign); err != nil {
		return fmt.Errorf("the manifest: %w", err)
	}

	if err := s.Store.Erase(ctx, d); err != nil {
		if merr := s.r.m.Mark(ctx, s.ns, string(d), Erasing, Present, s.r.sign); merr != nil {
			log.From(ctx).WarnContext(ctx, "trail: the manifest", "err", merr)
		}

		return err
	}

	s.r.forget(blobKey{s.ns, d})

	return s.r.m.Mark(ctx, s.ns, string(d), Erasing, Erased, s.r.sign)
}

// adopted records a blob put into the archive beneath the manifest -- a hard
// link, which [flob.OsStore.Adopt] makes without the decorator seeing it.
func adopted(ctx context.Context, s flob.Store, d flob.Digest, l flob.Labels) error {
	r, ok := s.(manifestedStore)
	if !ok {
		return nil
	}

	key, err := r.r.m.Adding(ctx, r.ns, "", labelsOf(l), r.r.sign)
	if err != nil {
		return err
	}
	if err := r.r.m.Added(ctx, key, string(d), labelsOf(l), r.r.sign); err != nil {
		return err
	}

	r.r.remember(blobKey{r.ns, d}, labelsOf(l))

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

// labelsFrom is a row's labels as a blob carries them.
func labelsFrom(m map[string]string) flob.Labels {
	l := flob.Labels{}
	for k, v := range m {
		l.Set(k, v)
	}

	return l
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

// trust loads what the manifest accounts for, for an act that is not a pass --
// a pass has it from verifying; see [pass.verify]: every blob it has a row for,
// present or being erased, and the adds in flight, by the intent their blobs
// are labelled with -- each by a row whose signature is no finding. A manifest
// with no rows is filled first, as the first pass would; see [Policy.fill].
func (x *pass) trust(ctx context.Context) error {
	if x.m == nil {
		return nil
	}

	rows, err := x.s.Rows(ctx)
	if err != nil {
		return fmt.Errorf("the manifest: %w", err)
	}
	if len(rows) == 0 {
		inv, err := inventory(ctx, x.m.a)
		if err != nil {
			return err
		}
		if len(inv) > 0 {
			if err := x.p.fill(ctx, x.s, inv); err != nil {
				return fmt.Errorf("the manifest: %w", err)
			}
			if rows, err = x.s.Rows(ctx); err != nil {
				return fmt.Errorf("the manifest: %w", err)
			}
		}
	}

	// The adds in flight are trusted by the intent their blobs are labelled
	// with, which anybody who can write the archive can label a blob with.
	// While the rows add up to the latest checkpoint that is an add an act is
	// still at; once they do not, it may be a row put back into `adding` with
	// the signature it had, and its intent is nothing to go by.
	flying := x.p.addsUpToLatest(ctx, x.s) == nil

	trusted := x.p.trusted()
	known := map[blobKey]map[string]string{}
	intents := map[string]map[string]string{}
	for _, v := range rows {
		if kind, _ := x.p.rowFinding(v, trusted); kind != "" {
			continue
		}

		switch v.State {
		case Adding:
			if v.Intent != "" && flying {
				intents[v.Namespace+"\x00"+v.Intent] = v.Labels
			}
		case Present, Erasing:
			known[blobKey{v.Namespace, flob.Digest(v.Digest)}] = v.Labels
		}
	}
	x.m.load(known, intents)

	return nil
}

// raw is the archive beneath the manifest: for what an act does to a blob the
// manifest does not account for, which it must not come to account for by
// being written again.
func (x *pass) raw() flob.Stores {
	if x.m == nil {
		return x.p.Archive
	}

	return x.m.a
}

// chunks is the chunks of these namespaces as an act decides by them: the ones
// the manifest accounts for, by the labels it recorded -- which nobody who can
// write only the archive can change -- and apart from them, the rest, by their
// own. Each oldest first. With no manifest, every chunk is the first kind.
//
// `fresh` lists the archive before the manifest is read, for an act: every blob
// written through the manifest has its row by then, present or in flight, so
// that what another pass is writing is not taken for something nobody wrote. A
// pass reads it once, when it verifies, and keeps it up to date as it goes.
func (x *pass) chunks(ctx context.Context, nss []string, fresh bool) ([]Chunk, []Chunk, error) {
	type blob struct {
		ns string
		v  flob.Info
	}

	bs := []blob{}
	for _, ns := range nss {
		w, ok := flob.AsWalker(x.raw().Use(ns))
		if !ok {
			return nil, nil, errUnwalkable
		}
		for v, err := range w.Walk(ctx) {
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", ns, err)
			}

			bs = append(bs, blob{ns, v})
		}
	}

	if fresh {
		if err := x.trust(ctx); err != nil {
			return nil, nil, err
		}
	}

	trusted, rest := []Chunk{}, []Chunk{}
	for _, b := range bs {
		k := blobKey{b.ns, b.v.Digest()}

		// With no manifest, a blob's own labels are all there is.
		l, ok := map[string]string(nil), x.m == nil
		if x.m != nil {
			l, ok = x.m.trusts(k)
		}
		if x.m == nil || !ok {
			sl, err := b.v.Labels(ctx)
			if err != nil {
				if errors.Is(err, flob.ErrNotExist) {
					// Erased between the listing and the look, by another pass.
					continue
				}

				return nil, nil, fmt.Errorf("%s: %w", b.ns, err)
			}

			l = labelsOf(sl)
			if x.m != nil {
				if v, in := x.m.inFlight(b.ns, l[labelIntent]); in {
					l, ok = v, true
				}
			}
		}

		c, isChunk := chunkOf(b.ns, k.d, labelsFrom(l))
		if !isChunk {
			continue
		}

		c.Size, _ = b.v.Size(ctx)
		if ok {
			trusted = append(trusted, c)
		} else {
			rest = append(rest, c)
		}
	}
	slices.SortFunc(trusted, chunkOrder)
	slices.SortFunc(rest, chunkOrder)

	return trusted, rest, nil
}

// settle finishes what a crash left in flight, and fills the manifest the
// first time from what the archive holds.
//
// An add whose blob is there, labelled with its intent, is present, and one
// whose blob is not is dropped once it is a day old -- what is younger may be
// another pass still writing. An erase whose blob is gone is erased, and one
// whose blob is still there is **undone** once it is a day old: the act that
// marked it did not finish, and the pass decides again, so a row in the
// database alone can never make a pass destroy anything. What is younger may be
// another pass between marking and erasing, and undoing that one would leave
// its row present and its blob gone.
//
// A row whose signature is a finding is not settled: settling is the
// deployment saying what the row says next, and [Policy.Verify] says what is
// wrong with it instead.
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

	sign, trusted := p.signing(), p.trusted()
	for _, v := range rows {
		if kind, _ := p.rowFinding(v, trusted); kind != "" {
			continue
		}

		switch v.State {
		case Adding:
			if k, ok := intents[v.Namespace+"\x00"+v.Intent]; ok && v.Intent != "" {
				err = errors.Join(err, m.Added(ctx, v.Key, string(k.d), inv[k], sign))
			} else if time.Since(v.Created) > Swept {
				err = errors.Join(err, m.Drop(ctx, v.Key))
			}
		case Erasing:
			if _, ok := inv[blobKey{v.Namespace, flob.Digest(v.Digest)}]; !ok {
				err = errors.Join(err, m.Mark(ctx, v.Namespace, v.Digest, Erasing, Erased, sign))
			} else if time.Since(v.Updated) > Swept {
				err = errors.Join(err, m.Mark(ctx, v.Namespace, v.Digest, Erasing, Present, sign))
			}
		}
	}

	return err
}

// fill is the first time: every blob the archive holds goes into the manifest
// as it is. Refused once the archive has had a checkpoint, since an empty
// manifest after one is a database restored from before it, or emptied --
// which an operator looks at, and [Policy.Accept] is for.
func (p Policy) fill(ctx context.Context, m Manifest, inv map[blobKey]map[string]string) error {
	first, err := p.unchecked(ctx, m)
	if err != nil {
		return err
	}
	if !first {
		log.From(ctx).WarnContext(ctx, "trail: the manifest is empty and the archive has had checkpoints, so it is not filled from what the archive holds",
			"see", "`trail verify`")

		return nil
	}

	sign := p.signing()
	for _, k := range slices.SortedFunc(maps.Keys(inv), blobOrder) {
		key, err := m.Adding(ctx, k.ns, "", inv[k], sign)
		if err != nil {
			return err
		}
		if err := m.Added(ctx, key, string(k.d), inv[k], sign); err != nil {
			return err
		}
	}

	log.From(ctx).InfoContext(ctx, "trail: the manifest, filled from what the archive holds", "blobs", len(inv))

	return nil
}

// unchecked answers whether no checkpoint of the archive has ever been taken:
// none numbered by the database, and none kept by the store.
//
// Until the first, the archive is taken as it is -- the manifest filled from
// what it holds, the files a version before wrote into its directory adopted
// -- because there is nothing yet to say what it held before. From the first
// on, a blob or a file nobody wrote through the manifest is a finding, and only
// an operator's acceptance takes it in.
//
// Any blob the store labels a checkpoint counts, the deployment's word or not
// (see [Policy.readChain]): what this decides is whether the archive is taken
// as it is, and one nobody vouches for can only make that less likely.
func (p Policy) unchecked(ctx context.Context, m Manifest) (bool, error) {
	if k, _, err := m.Latest(ctx); err != nil || k > 0 {
		return false, err
	}

	cs, err := checkpointsIn(ctx, p.checkpoints())
	if err != nil {
		return false, err
	}

	return len(cs) == 0, nil
}

// Finding is something about the archive that its manifest does not account
// for.
type Finding struct {
	// Kind is what was found:
	//
	//	missing      a blob the manifest says the archive holds, and it does not
	//	unaccounted  a blob the archive holds that the manifest never added,
	//	             or a file of the directory format in the archive's
	//	             directory once the archive has had a checkpoint
	//	relabelled   a blob whose labels are not the ones the manifest recorded,
	//	             or a checkpoint whose labels do not say which it is
	//	altered      a blob whose bytes are not its digest any more
	//	forged       a row or a checkpoint whose signature is bad, or made by a
	//	             key nobody trusts
	//	unsigned     a row with no signature, when the policy names a key; a
	//	             checkpoint with none, after one that had one
	//	rewritten    a checkpoint the manifest's rows no longer add up to, or one
	//	             of two the store keeps under one number
	//	reordered    a checkpoint that names one after it as the one before
	//	unkept       a checkpoint the database numbered, or the one after it
	//	             names, that the store does not keep; or the numbers a
	//	             checkpoint skips over to the one it names
	//	emptied      a manifest with no rows beside checkpoints of an archive
	//	             that held something
	//	behind       a checkpoint the store keeps that the database never numbered
	//
	// A finding about a row is under the blob it is the row of. One about a
	// checkpoint has its number, and the digest it is kept under or named by
	// where that says more.
	Kind string `json:"kind"`

	Namespace string      `json:"ns,omitempty"`
	Digest    flob.Digest `json:"digest,omitempty"`

	// Checkpoint is the checkpoint it is about, for the findings about one.
	Checkpoint int `json:"checkpoint,omitempty"`

	Note string `json:"note,omitempty"`
}

// findingOrder puts findings in an order that does not hang on the order a
// store answered in.
func findingOrder(a, b Finding) int {
	return cmp.Or(
		cmp.Compare(a.Namespace, b.Namespace),
		cmp.Compare(a.Digest, b.Digest),
		cmp.Compare(a.Checkpoint, b.Checkpoint),
		cmp.Compare(a.Kind, b.Kind),
		cmp.Compare(a.Note, b.Note),
	)
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

	// trusted is the archive's blobs the manifest accounts for, with the
	// labels it recorded -- which are what a pass decides by, and not the
	// ones on the blob, which whoever can write the archive can change.
	// flying is which of them are trusted only for the intent they are
	// labelled with: the blob of an add in flight.
	trusted map[blobKey]map[string]string
	flying  map[blobKey]bool
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
// # Which signatures count
//
// The ones made by [Policy.Signer] or by a key in [Policy.Trust]; one by any
// other key is forged. A row with no signature is a finding whenever the policy
// names a key, and a checkpoint with none only after one that had a signature:
// a deployment that gave itself a key later has checkpoints that begin
// unsigned, and rows an operator accepts once (see [Policy.keyed]). A row whose
// signature is a finding is reported for itself, and is not what the archive is
// compared with -- what it says is nobody's word -- so its blob is no other
// finding.
func (p Policy) Verify(ctx context.Context, s Store, full bool) (Verified, error) {
	out := Verified{trusted: map[blobKey]map[string]string{}, flying: map[blobKey]bool{}}
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
	intents := map[string]Archived{}

	// The rows whose signatures are findings, as the second reading has them,
	// and the blobs and intents they are the rows of.
	trusted := p.trusted()
	unvouched := []Finding{}
	nobodys := map[blobKey]bool{}
	nobodysIntent := map[string]bool{}
	for i, rows := range [][]Archived{before, after} {
		m := live.before
		if i == 1 {
			m = live.after
		}
		for _, v := range rows {
			if kind, note := p.rowFinding(v, trusted); kind != "" {
				if i == 1 {
					unvouched = append(unvouched, rowFindingOf(v, kind, note))
					switch v.State {
					case Adding:
						nobodysIntent[v.Namespace+"\x00"+v.Intent] = true
					default:
						nobodys[blobKey{v.Namespace, flob.Digest(v.Digest)}] = true
					}
				}

				continue
			}

			switch v.State {
			case Adding:
				intents[v.Namespace+"\x00"+v.Intent] = v
			case Present, Erasing:
				m[blobKey{v.Namespace, flob.Digest(v.Digest)}] = v
			}
		}
	}
	slices.SortFunc(unvouched, findingOrder)
	out.Findings = append(out.Findings, unvouched...)

	for _, k := range slices.SortedFunc(maps.Keys(inv), blobOrder) {
		l := inv[k]
		v, ok := live.after[k]
		if !ok {
			v, ok = live.before[k]
		}
		intent, adding := intents[k.ns+"\x00"+l[labelIntent]]
		switch {
		case ok:
			out.trusted[k] = v.Labels
			if v.State == Present && !sameLabels(v.Labels, l) && p.relabelled(ctx, s, a, k, v) {
				out.Findings = append(out.Findings, Finding{Kind: "relabelled", Namespace: k.ns, Digest: k.d, Note: labelDiff(v.Labels, l)})
			}
		case l[labelIntent] != "" && adding:
			// Being written, or written by an act a crash stopped.
			out.trusted[k] = intent.Labels
			out.flying[k] = true
		case nobodys[k] || (l[labelIntent] != "" && nobodysIntent[k.ns+"\x00"+l[labelIntent]]):
			// Its row is a finding of its own, and the blob nothing an act
			// trusts.
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

	// And the files of the directory format beside the store, which the
	// archive's first pass takes in and nothing after it does.
	strays, err := p.looseFindings(ctx, s)
	if err != nil {
		return out, err
	}
	out.Findings = append(out.Findings, strays...)

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

// looseFindings is the files a version before this one would have written into
// the archive's directory, there after its first checkpoint: each is
// `unaccounted`, under no namespace, by its digest. Until that checkpoint they
// are the first pass's to take in, and none.
func (p Policy) looseFindings(ctx context.Context, m Manifest) ([]Finding, error) {
	root, ok := rootOf(p.Archive)
	if !ok {
		return nil, nil
	}

	vs, err := looseIn(root)
	if err != nil || len(vs) == 0 {
		return nil, err
	}

	first, err := p.unchecked(ctx, m)
	if err != nil || first {
		return nil, err
	}

	out := []Finding{}
	for _, v := range vs {
		d, err := v.digest()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}

			return out, err
		}

		out = append(out, Finding{Kind: "unaccounted", Digest: d,
			Note: "a file of the directory format in the archive's directory, " + v.name + ", after its first checkpoint"})
	}

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
	trusted := p.trusted()
	for _, r := range rows {
		if r.Namespace != k.ns || r.Digest != string(k.d) || r.State != Present {
			continue
		}
		if kind, _ := p.rowFinding(r, trusted); kind == "" {
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
	K  int       `json:"k"`
	At time.Time `json:"at"`

	// Prev is the one before it, by its digest in the checkpoint store, which
	// is what makes the checkpoints a chain: one taken away, or written again
	// as something else, is one the checkpoint after it names and the store
	// does not keep -- whatever its labels say, and whether or not the
	// database has been made to agree. Empty on the first, and on one written
	// before checkpoints were chained.
	//
	// It is the newest the store keeps below it when this one is written --
	// of the checkpoints that are the deployment's word, by what each says
	// (see [Policy.readChain]) -- which is the one numbered just before but
	// for two cases. That one was numbered and never kept, by a pass a crash
	// stopped, or it was taken away since: then the number between the two is
	// one no checkpoint the store keeps says it is, `unkept` until an operator
	// accepts (#80). Or an act at the same moment numbered it and has not
	// written it yet: then both name the one before, the store keeps each,
	// and that is no finding.
	Prev flob.Digest `json:"prev,omitempty"`

	Count  int         `json:"count"`
	Digest flob.Digest `json:"digest"`
	Blobs  []Kept      `json:"blobs"`

	// Accepted is why an operator accepted the manifest as it stood, when the
	// checkpoint was taken over an acceptance; see [Policy.Accept]. It is the
	// one kind taken whether or not the rows add up to the one before it, and
	// it says so.
	Accepted string `json:"accepted,omitempty"`
}

// Kept is one blob of a checkpoint.
type Kept struct {
	Namespace string      `json:"ns"`
	Digest    flob.Digest `json:"digest"`
}

// signed is a checkpoint as it is stored: its bytes, and the signature over
// exactly those bytes with the public half of the key that made it, PKIX.
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

// errRewritten is a manifest whose rows no longer add up to the latest
// checkpoint the store keeps.
var errRewritten = errors.New("the manifest no longer adds up to the latest checkpoint, and no checkpoint is taken over it until an operator accepts it; see `trail verify`")

// checkpoint ends a pass, and every act that changed the manifest (see
// [pass.cover]): the next number, and every blob the manifest has, written to
// the checkpoint store naming the one before it, and signed when the policy has
// a key.
//
// Not while the store keeps a checkpoint the database has not numbered yet.
// That is a database restored from before it, and a checkpoint written then
// would take a number one already has.
//
// Nor while the rows the latest checkpoint was taken over no longer add up to
// it. [Policy.Verify] recomputes the latest two checkpoints from their rows,
// and with a checkpoint at the end of every act the latest two are soon
// others: a checkpoint taken over rows somebody changed would put what they
// changed out of reach two acts later. Refused, it stays the latest, and
// stays found.
//
// Both but when `accepted` says why an operator has accepted the manifest as
// it is. That one is numbered after every checkpoint the store keeps, by what
// each says, and names the newest of them as the one before it: a database
// restored from before them goes on from there, and they stay what the
// archive held meanwhile.
//
// Every one of those is decided by the checkpoints that are the deployment's
// word, and by no other (see [Policy.readChain]). A checkpoint nobody signed,
// or one by a key the policy does not trust, is found, and that is all it
// does: put in the store numbered ahead of the database, it would otherwise
// stop every act, and be what the next acceptance is numbered after and
// names as the one before it.
func (p Policy) checkpoint(ctx context.Context, m Manifest, accepted string) (Checkpoint, error) {
	ch, err := p.readChain(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	latest, _, err := m.Latest(ctx)
	if err != nil {
		return Checkpoint{}, err
	}

	after := 0
	if accepted != "" {
		// Every one, the way [Policy.Verify] finds the database behind: a
		// checkpoint relabelled to look older, or not listed at all, is still
		// one the next must be numbered after.
		after = ch.newest()
	} else {
		// By what a checkpoint says, which is signed, and not its labels,
		// which are not: one relabelled ahead of the database is a finding of
		// its own, and no reason to stop keeping checkpoints.
		if ch.newest() > latest {
			return Checkpoint{}, errBehind
		}
		if err := ch.addsUp(ctx, m, latest); err != nil {
			return Checkpoint{}, err
		}
	}

	k, rows, err := m.Checkpoint(ctx, after)
	if err != nil {
		return Checkpoint{}, err
	}

	c := checkpointOf(k, time.Now(), rows)
	c.Accepted = accepted
	if c.Prev, err = p.prevOf(ctx, ch, k); err != nil {
		return c, err
	}

	b, err := json.Marshal(c)
	if err != nil {
		return c, err
	}

	v := signed{Checkpoint: b}
	if p.Signer != nil {
		if v.Key, v.Sig, err = sign(p.Signer, b); err != nil {
			return c, fmt.Errorf("checkpoint %d: signing it: %w", k, err)
		}
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

// addsUp answers whether the rows the latest checkpoint was taken over still
// add up to what the store keeps of it: [errRewritten] when they do not.
//
// By what a checkpoint says, which is signed, and of the ones that are the
// deployment's word: one not kept yet -- being written, or numbered by a pass
// a crash stopped -- is nothing to compare, and is `unkept` to
// [Policy.Verify] once it is old enough. Nor is one nobody can vouch for,
// which would otherwise be a way to stop every act by putting a checkpoint of
// the latest number in the store.
//
// The rows a checkpoint was taken over do not change with anything an act
// does after it: a row added since is in none yet, and one erased since is
// still in it until the next is numbered. So a difference is a row somebody
// changed beneath the deployment -- a blob taken away with its row, or a row
// put back into a state the checkpoint did not have it in.
func (ch chain) addsUp(ctx context.Context, m Manifest, latest int) error {
	if latest == 0 {
		return nil
	}

	kept := ch.numbered(latest)
	if len(kept) == 0 {
		return nil
	}

	rows, err := m.At(ctx, latest)
	if err != nil {
		return fmt.Errorf("the manifest: %w", err)
	}
	for _, v := range kept {
		if got := checkpointOf(latest, v.c.At, rows); got.Digest != v.c.Digest {
			return fmt.Errorf("checkpoint %d: %s: %w", latest, checkpointDiff(v.c, got), errRewritten)
		}
	}

	return nil
}

// addsUpToLatest is [chain.addsUp] for the checkpoint the database numbered
// last.
func (p Policy) addsUpToLatest(ctx context.Context, m Manifest) error {
	ch, err := p.readChain(ctx)
	if err != nil {
		return err
	}
	latest, _, err := m.Latest(ctx)
	if err != nil {
		return fmt.Errorf("the manifest: %w", err)
	}

	return ch.addsUp(ctx, m, latest)
}

// prevOf is the checkpoint the one numbered k names as the one before it: of
// the ones that are the deployment's word, the newest below it, by what each
// says it is and whatever its labels say. The store is listed again once k is
// taken, since one numbered before it -- by an act at the same moment -- may
// have been kept since the chain was read.
func (p Policy) prevOf(ctx context.Context, ch chain, k int) (flob.Digest, error) {
	var best *stored
	take := func(v stored) {
		if v.c.K < 1 || v.c.K >= k {
			return
		}
		if best == nil || v.c.K > best.c.K || (v.c.K == best.c.K && v.d < best.d) {
			best = &v
		}
	}
	for _, v := range ch.ordered() {
		if ch.vouched[v.d] {
			take(v)
		}
	}

	s := p.checkpoints()
	ls, err := listCheckpoints(ctx, s)
	if err != nil {
		return "", err
	}
	trusted := p.trusted()
	for _, v := range ls {
		if _, ok := ch.kept[v.d]; ok {
			continue
		}
		if was, err := readCheckpoint(ctx, s, v.d); err == nil && p.vouches(was, trusted, ch.signed) {
			take(was)
		}
	}

	if best == nil {
		return "", nil
	}

	return best.d, nil
}

// stored is a checkpoint as it was read back.
type stored struct {
	c Checkpoint
	s signed
	d flob.Digest
}

// listed is a checkpoint as a store lists it: its digest, and the number its
// labels give it, which is zero for none.
type listed struct {
	k int
	d flob.Digest
}

// listCheckpoints is every blob a store keeps that its labels say is a
// checkpoint, two of one number among them if the store keeps two.
func listCheckpoints(ctx context.Context, s flob.Store) ([]listed, error) {
	w, ok := flob.AsWalker(s)
	if !ok {
		return nil, errUnwalkable
	}

	out := []listed{}
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

		k, err := strconv.Atoi(l.Get(labelK))
		if err != nil || k < 0 {
			k = 0
		}
		out = append(out, listed{k, v.Digest()})
	}

	return out, nil
}

// checkpointsIn is every checkpoint a store keeps, by the number its labels
// give it, which is only ever compared, and never believed -- see
// [Policy.readChain].
func checkpointsIn(ctx context.Context, s flob.Store) (map[int]flob.Digest, error) {
	ls, err := listCheckpoints(ctx, s)
	if err != nil {
		return nil, err
	}

	out := map[int]flob.Digest{}
	for _, v := range ls {
		if v.k > 0 {
			out[v.k] = v.d
		}
	}

	return out, nil
}

// chain is every checkpoint a store keeps, read, by digest; the number the
// store's labels give each one it lists; which of them are the deployment's
// word; and what reading them found.
type chain struct {
	kept     map[flob.Digest]stored
	labelled map[flob.Digest]int
	findings []Finding

	// vouched is the checkpoints kept that are the deployment's word, which
	// alone decide anything -- see [Policy.readChain] -- and signed is
	// whether the store keeps one signed by a key the policy trusts.
	vouched map[flob.Digest]bool
	signed  bool
}

// readChain reads every checkpoint the policy's checkpoint store lists, and
// follows each one's [Checkpoint.Prev] to the ones the listing does not show:
// a checkpoint whose labels were changed or taken off is not listed as
// itself, and is still in the store under its digest. A checkpoint's number is
// what it says, which is signed, and not what its labels say, which is not.
//
// # Which checkpoints are the deployment's word
//
// What it says is signed only when somebody can tell who signed it. With a
// key, a checkpoint is the deployment's word when its signature is good and
// by a key the policy trusts, and so is every checkpoint such a one names as
// the one before it, and so on down the chain: what a checkpoint names, it
// names by digest, and a checkpoint is read only when its bytes are its
// digest, so the one named is exactly what was there when the one naming it
// was signed -- whatever signed it, if anything did. That is how the
// checkpoints a key signed before it was rotated out stay what they were
// once the key is no longer trusted, and the ones from before the deployment
// had a key, once it has one.
//
// Only those decide what the next checkpoint is numbered, names and is
// refused for, and which of the numbers between two is a finding. One
// nobody signed, or signed by a key nobody trusts, and named by none of them
// is found -- `unsigned`, `forged` -- and decides nothing: whoever can write
// the checkpoint store can put one there, numbered as they like, and it would
// otherwise stop every act, be what the next acceptance is numbered after,
// and say which gaps are accepted.
//
// Until the store keeps one checkpoint a trusted key signed -- a deployment
// that gave itself a key, until its next act takes one -- the unsigned ones
// are all there is, and decide as they did. Without a key nothing is signed
// and nothing is held to a signature, and every checkpoint that reads decides.
//
// # What it finds of the chain
//
// Of the checkpoints that decide: one named as the one before and not kept,
// which is one taken away -- or written again as something else, which the
// store then keeps under another digest; one that names a later one as the
// one before it; one whose labels do not say which it is; and the numbers one
// skips over. Of every checkpoint kept: two of one number, which is one
// written again beside the first on a store that keeps both.
func (p Policy) readChain(ctx context.Context) (chain, error) {
	s := p.checkpoints()
	ls, err := listCheckpoints(ctx, s)
	if err != nil {
		return chain{}, err
	}

	ch := chain{kept: map[flob.Digest]stored{}, labelled: map[flob.Digest]int{}, vouched: map[flob.Digest]bool{}}
	todo := []flob.Digest{}
	for _, v := range ls {
		ch.labelled[v.d] = v.k
		todo = append(todo, v.d)
	}

	// Which checkpoints name each one the listing did not show, for when the
	// store does not keep it either.
	named := map[flob.Digest][]flob.Digest{}
	unread := map[flob.Digest]error{}
	seen := map[flob.Digest]bool{}
	for len(todo) > 0 {
		d := todo[0]
		todo = todo[1:]
		if seen[d] {
			continue
		}
		seen[d] = true

		v, err := readCheckpoint(ctx, s, d)
		if err != nil {
			unread[d] = err
			continue
		}

		ch.kept[d] = v
		if prev := v.c.Prev; prev != "" {
			named[prev] = append(named[prev], d)
			if !seen[prev] {
				todo = append(todo, prev)
			}
		}
	}

	trusted := p.trusted()
	for _, v := range ch.kept {
		if p.keyed() && signedBy(v, trusted) {
			ch.signed = true
		}
	}
	todo = todo[:0]
	for d, v := range ch.kept {
		if p.vouches(v, trusted, ch.signed) {
			ch.vouched[d] = true
			todo = append(todo, d)
		}
	}
	for len(todo) > 0 {
		d := todo[0]
		todo = todo[1:]
		if prev := ch.kept[d].c.Prev; prev != "" && !ch.vouched[prev] {
			if _, ok := ch.kept[prev]; ok {
				ch.vouched[prev] = true
				todo = append(todo, prev)
			}
		}
	}

	// What does not read is found when the store lists it as a checkpoint, or
	// a checkpoint that decides names it; what only a checkpoint nobody
	// vouches for names is that one's word, which is nothing.
	for _, d := range slices.Sorted(maps.Keys(unread)) {
		_, listedAs := ch.labelled[d]
		by := slices.DeleteFunc(slices.Clone(named[d]), func(v flob.Digest) bool { return !ch.vouched[v] })
		err := unread[d]
		switch {
		case errors.Is(err, flob.ErrNotExist) && !listedAs:
			if len(by) > 0 {
				ch.findings = append(ch.findings, Finding{Kind: "unkept", Digest: d,
					Note: fmt.Sprintf("checkpoint %d names it as the one before it, and the store does not keep it", ch.kept[by[0]].c.K)})
			}

		case errors.Is(err, flob.ErrNotExist):
			// Erased between the listing and the read. If anything names it,
			// it is found when that is.

		case listedAs || len(by) > 0:
			ch.findings = append(ch.findings, Finding{Kind: "forged", Checkpoint: ch.labelled[d], Digest: d, Note: "it does not read: " + err.Error()})
		}
	}

	byK := map[int]int{}
	for _, v := range ch.kept {
		byK[v.c.K]++
	}
	for _, k := range slices.Sorted(maps.Keys(byK)) {
		if byK[k] > 1 {
			ch.findings = append(ch.findings, Finding{Kind: "rewritten", Checkpoint: k,
				Note: fmt.Sprintf("the store keeps %d checkpoints numbered %d", byK[k], k)})
		}
	}

	for _, v := range ch.decided() {
		switch l, listedAs := ch.labelled[v.d]; {
		case !listedAs:
			ch.findings = append(ch.findings, Finding{Kind: "relabelled", Checkpoint: v.c.K, Digest: v.d,
				Note: "its labels do not say it is a checkpoint, and the one after it names it"})
		case l != v.c.K:
			ch.findings = append(ch.findings, Finding{Kind: "relabelled", Checkpoint: v.c.K, Digest: v.d,
				Note: fmt.Sprintf("its labels say it is checkpoint %d", l)})
		}

		if prev, ok := ch.kept[v.c.Prev]; ok && prev.c.K >= v.c.K {
			ch.findings = append(ch.findings, Finding{Kind: "reordered", Checkpoint: v.c.K, Digest: v.d,
				Note: fmt.Sprintf("it names checkpoint %d as the one before it", prev.c.K)})
		}
	}

	ch.findings = append(ch.findings, ch.skipped()...)

	return ch, nil
}

// signedBy answers whether a checkpoint's signature is good and by one of
// these keys.
func signedBy(v stored, trusted []crypto.PublicKey) bool {
	has, good := signature(v.s)
	return has && good && trusts(trusted, v.s.Key)
}

// vouches answers whether a checkpoint is the deployment's word by itself,
// rather than by a later one naming it; `signed` is whether the store keeps
// a checkpoint a trusted key signed. See [Policy.readChain].
func (p Policy) vouches(v stored, trusted []crypto.PublicKey, signed bool) bool {
	has, good := signature(v.s)
	switch {
	case has && !good:
		return false
	case !p.keyed():
		return true
	case has:
		return trusts(trusted, v.s.Key)
	default:
		return !signed
	}
}

// skipped is, for each checkpoint that decides, the numbers between it and
// the one it names as the one before it that no checkpoint that decides says
// it is (#80).
//
// A checkpoint names the newest one the store keeps when it is taken, so a
// number between them was numbered by the database and is not in the store:
// taken away, or written over by a copy made before it -- a store restored
// from a backup -- and the next pass went past it; or numbered by a pass a
// crash stopped before the store kept it. Nothing else says so once a later
// one is numbered: the database's own `unkept` is only of the latest it
// numbered, and the one before a later checkpoint is one the store keeps.
// What it cannot tell apart, an operator can, and accepts; the numbers
// skipped before an acceptance are what the operator looked at.
//
// A number the store keeps is no finding, whatever names it. Two acts that
// take a checkpoint at the same moment are numbered one after the other, and
// the second can be written before the first: then both name the one before
// the first, and the first is kept, named by none -- no finding while it is
// there, and the number skipped over once it is taken away.
//
// Only between two checkpoints the store keeps: one that names nothing is
// the first, or one from before the checkpoints were chained.
func (ch chain) skipped() []Finding {
	accepted := 0
	says := []int{}
	for _, v := range ch.decided() {
		says = append(says, v.c.K)
		if v.c.Accepted != "" {
			accepted = max(accepted, v.c.K)
		}
	}
	says = slices.Compact(says)

	out := []Finding{}
	for _, v := range ch.decided() {
		prev, ok := ch.kept[v.c.Prev]
		if !ok || v.c.K <= accepted || prev.c.K >= v.c.K-1 {
			continue
		}

		gaps := unsaid(says, prev.c.K+1, v.c.K-1)
		if len(gaps) == 0 {
			continue
		}

		missing := make([]string, len(gaps))
		for i, g := range gaps {
			missing[i] = strconv.Itoa(g[0])
			if g[0] < g[1] {
				missing[i] = fmt.Sprintf("%d to %d", g[0], g[1])
			}
		}
		out = append(out, Finding{Kind: "unkept", Checkpoint: gaps[0][0], Digest: v.d,
			Note: fmt.Sprintf("checkpoint %d names %d as the one before it, and the store does not keep %s", v.c.K, prev.c.K, strings.Join(missing, ", "))})
	}

	return out
}

// unsaid is the runs of the numbers from `from` to `to` that are not in
// `says`, which is in order: each as its first and its last.
func unsaid(says []int, from, to int) [][2]int {
	out := [][2]int{}
	i, _ := slices.BinarySearch(says, from)
	for n := from; n <= to; {
		if i < len(says) && says[i] <= to {
			if says[i] > n {
				out = append(out, [2]int{n, says[i] - 1})
			}
			n = says[i] + 1
			i++
			continue
		}

		out = append(out, [2]int{n, to})
		break
	}

	return out
}

// ordered is the checkpoints kept, by the number each says it is.
func (ch chain) ordered() []stored {
	out := slices.Collect(maps.Values(ch.kept))
	slices.SortFunc(out, func(a, b stored) int { return cmp.Or(cmp.Compare(a.c.K, b.c.K), cmp.Compare(a.d, b.d)) })

	return out
}

// decided is the checkpoints kept that are the deployment's word, by the
// number each says it is.
func (ch chain) decided() []stored {
	return slices.DeleteFunc(ch.ordered(), func(v stored) bool { return !ch.vouched[v.d] })
}

// numbered is the checkpoints that decide and say they are number k.
func (ch chain) numbered(k int) []stored {
	return slices.DeleteFunc(ch.decided(), func(v stored) bool { return v.c.K != k })
}

// holding is how many of the checkpoints that decide say the archive held a
// blob.
func (ch chain) holding() int {
	n := 0
	for _, v := range ch.decided() {
		if v.c.Count > 0 {
			n++
		}
	}

	return n
}

// newest is the highest number a checkpoint that decides says it is, and
// zero for none.
func (ch chain) newest() int {
	n := 0
	for _, v := range ch.decided() {
		n = max(n, v.c.K)
	}

	return n
}

// readCheckpoint reads one checkpoint, and only while its bytes are its
// digest: what a checkpoint names as the one before it is that digest, and
// the one read under it has to be what was named.
func readCheckpoint(ctx context.Context, s flob.Store, d flob.Digest) (stored, error) {
	out := stored{d: d}
	if _, err := d.Sanitize(); err != nil {
		return out, err
	}

	r, _, err := s.Open(ctx, d)
	if err != nil {
		return out, err
	}
	defer r.Close()

	raw, err := io.ReadAll(r)
	if err != nil {
		return out, err
	}
	v := d.Verifier()
	_, _ = v.Write(raw)
	if !v.Verified() {
		return out, ErrAltered
	}

	z, err := gzip.NewReader(bytes.NewReader(raw))
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

// ReadCheckpoints reads every checkpoint the policy's checkpoint store keeps
// that is the deployment's word (see [Policy.readChain]), oldest first: what
// the archive held, day by day.
func (p Policy) ReadCheckpoints(ctx context.Context, fn func(Checkpoint) error) error {
	ch, err := p.readChain(ctx)
	if err != nil {
		return err
	}

	for _, v := range ch.decided() {
		if err := fn(v.c); err != nil {
			return err
		}
	}

	return nil
}

// checkpointing is how long a checkpoint may take between being numbered and
// being kept: a signature from a device across a network, and the store's
// write. One the database numbered and the store does not keep is a finding
// once it is older than this, and a pass taking it before then.
const checkpointing = 10 * time.Minute

// verifyCheckpoints compares the latest checkpoint with the rows it was taken
// over, and checks the signatures of every checkpoint the store keeps.
//
// Again when a checkpoint was taken while it read: the latest two are
// recomputed from rows a checkpoint changes, and what was read of them before
// one and after it are two checkpoints' rows.
func (p Policy) verifyCheckpoints(ctx context.Context, s Store, empty bool, out *Verified) ([]Finding, error) {
	for i := 0; ; i++ {
		fs, latest, err := p.checkpointFindings(ctx, s, empty, out)
		if err != nil {
			return nil, err
		}

		now, _, err := s.Latest(ctx)
		if err != nil {
			return nil, fmt.Errorf("the manifest: %w", err)
		}
		if now == latest || i == 2 {
			return fs, nil
		}
	}
}

// checkpointFindings is one reading of [Policy.verifyCheckpoints], and the
// latest checkpoint it read.
func (p Policy) checkpointFindings(ctx context.Context, s Store, empty bool, out *Verified) ([]Finding, int, error) {
	fs := []Finding{}

	ch, err := p.readChain(ctx)
	if err != nil {
		return nil, 0, err
	}
	latest, at, err := s.Latest(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("the manifest: %w", err)
	}

	// A checkpoint over nothing is what an empty manifest adds up to: the
	// first pass of a deployment with nothing to archive yet takes one, and
	// the second would otherwise find its own deployment emptied (#81).
	if n := ch.holding(); empty && n > 0 {
		fs = append(fs, Finding{Kind: "emptied", Note: fmt.Sprintf("the archive has %d checkpoints of blobs it held, and the manifest no rows: a database restored from before them?", n)})
	}
	if k := ch.newest(); k > latest {
		fs = append(fs, Finding{Kind: "behind", Checkpoint: k, Note: fmt.Sprintf("the database has numbered %d checkpoints, and the store keeps one numbered %d: a database restored from before it", latest, k)})
	}
	if latest > 0 && time.Since(at) > checkpointing && len(ch.numbered(latest)) == 0 {
		fs = append(fs, Finding{Kind: "unkept", Checkpoint: latest, Note: "the database numbered it, and the checkpoint store does not keep it"})
	}
	fs = append(fs, ch.findings...)

	// A signature by a key nobody trusts is no finding on a checkpoint the
	// chain vouches for: a later one the deployment's key signed names it, so
	// it is what the archive held whoever signed it -- a key rotated out,
	// which need not be trusted for this. One the chain does not reach is
	// forged, and decides nothing.
	trusted := p.trusted()
	signedOnce := false
	for _, v := range ch.ordered() {
		k := v.c.K
		switch has, good := signature(v.s); {
		case has && !good:
			fs = append(fs, Finding{Kind: "forged", Checkpoint: k, Note: "its signature is not good"})
		case has && len(trusted) > 0 && !trusts(trusted, v.s.Key) && !ch.vouched[v.d]:
			fs = append(fs, Finding{Kind: "forged", Checkpoint: k, Note: "signed by a key nobody trusts: " + base64.StdEncoding.EncodeToString(v.s.Key)})
		case has:
			signedOnce = true
		case signedOnce:
			fs = append(fs, Finding{Kind: "unsigned", Checkpoint: k, Note: "unsigned, after one that was signed"})
		}
	}

	// The latest two are what the manifest's rows still add up to -- but for
	// the one before an acceptance, which is what an operator looked at and
	// decided the rows no longer had to: the acceptance is the rows as they
	// are, and the ones after it are compared with it. And the rows erased
	// since the one before the latest are read here and nowhere else, so this
	// is where an erase nobody signed is found: a chunk taken away beneath a
	// row marked erased to match.
	from := max(1, latest-1)
	for _, v := range ch.numbered(latest) {
		if v.c.Accepted != "" {
			from = latest
		}
	}

	newest := 0
	erased := []Finding{}
	seen := map[string]bool{}
	for k := max(1, latest-1); latest > 0 && k <= latest; k++ {
		rows, err := s.At(ctx, k)
		if err != nil {
			return nil, 0, fmt.Errorf("the manifest: %w", err)
		}

		for _, r := range rows {
			if r.State != Erased || seen[fmt.Sprint(r.Key)] {
				continue
			}
			seen[fmt.Sprint(r.Key)] = true

			if kind, note := p.rowFinding(r, trusted); kind != "" {
				erased = append(erased, rowFindingOf(r, kind, note))
			}
		}

		if k < from {
			continue
		}
		for _, v := range ch.numbered(k) {
			if got := checkpointOf(k, v.c.At, rows); got.Digest != v.c.Digest {
				fs = append(fs, Finding{Kind: "rewritten", Checkpoint: k, Note: checkpointDiff(v.c, got)})
			}
			newest = max(newest, k)
		}
	}
	slices.SortFunc(erased, findingOrder)
	fs = append(fs, erased...)
	out.Checkpoint = newest

	return fs, latest, nil
}

// signature answers whether a checkpoint is signed, and whether the signature
// is good -- by the key it names, which is a question [Policy.Trust] answers
// apart.
func signature(v signed) (bool, bool) {
	if len(v.Sig) == 0 {
		return false, false
	}

	return true, verified(v.Key, v.Checkpoint, v.Sig)
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
// a pass a crash stopped, a blob restored from a backup, rows written before
// rows were signed.
//
// It signs, as they stand, the rows whose signatures are findings -- the
// deployment saying after all what they say -- and then adds the blobs the
// archive holds and the manifest does not, takes out the ones it says the
// archive holds and it does not, and records the labels the archive's blobs
// carry. Each of those is a row written and signed as a pass writes one, so a
// policy that names a key has to have [Policy.Signer] to accept anything:
// accepting is signing, and an acceptance without the key is refused.
//
// A checkpoint is what it is, and Accept changes none. It ends in one of its
// own, taken over the manifest as it accepted it and saying why
// ([Checkpoint.Accepted]): the one kind taken whether or not the rows add up to
// the one before -- no other is taken over rows that do not (see
// [Policy.checkpoint]) -- and the checkpoints after it are compared with it,
// and not the one before. It is also the one taken when the database is
// behind the store, restored from a backup older than the latest checkpoint:
// numbered after the newest the store keeps, and naming it as the one before.
func (p Policy) Accept(ctx context.Context, s Store, why string) (Verified, error) {
	if strings.TrimSpace(why) == "" {
		return Verified{}, errors.New("an acceptance says why")
	}
	if p.keyed() && p.Signer == nil {
		return Verified{}, errors.New("accepting is signing what is accepted, and the policy names a key and has none to sign with")
	}

	v, err := p.Verify(ctx, s, false)
	if err != nil {
		return v, err
	}

	// The rows first: what the archive holds is compared with rows whose word
	// counts, and these did not until now.
	act := v
	if slices.ContainsFunc(v.Findings, isRowFinding) {
		if err := p.vouch(ctx, s); err != nil {
			return v, err
		}
		if act, err = p.Verify(ctx, s, false); err != nil {
			return v, err
		}
		for _, f := range act.Findings {
			if !slices.Contains(v.Findings, f) {
				v.Findings = append(v.Findings, f)
			}
		}
	}

	inv, err := inventory(ctx, unwrapStores(p.Archive))
	if err != nil {
		return v, err
	}

	sign := p.signing()
	for _, f := range act.Findings {
		k := blobKey{f.Namespace, f.Digest}
		switch {
		case f.Kind == "unaccounted" && f.Namespace == "":
			// A file of the directory format, taken in as the first pass
			// would have.
			err = errors.Join(err, p.adoptLoose(ctx, s, f.Digest))
		case f.Kind == "unaccounted":
			key, aerr := s.Adding(ctx, k.ns, "", inv[k], sign)
			if aerr == nil {
				aerr = s.Added(ctx, key, string(k.d), inv[k], sign)
			}
			err = errors.Join(err, aerr)
		case f.Kind == "relabelled":
			err = errors.Join(err, s.Label(ctx, k.ns, string(k.d), inv[k], sign))
		case f.Kind == "missing":
			err = errors.Join(err, s.Mark(ctx, k.ns, string(k.d), Present, Erased, sign))
		}
	}

	log.From(ctx).InfoContext(ctx, "trail: the manifest, made to say what the archive holds", "why", why, "findings", len(v.Findings))

	c, cerr := p.checkpoint(ctx, s, why)
	if cerr != nil {
		return v, errors.Join(err, fmt.Errorf("the checkpoint over what was accepted: %w", cerr))
	}
	log.From(ctx).InfoContext(ctx, "trail: a checkpoint", "after", "accept", "k", c.K, "blobs", c.Count)

	return v, err
}

// adoptLoose takes in the file of the directory format in the archive's
// directory whose digest this is, through the manifest: what an acceptance does
// with one, as the archive's first pass did with the files it found.
func (p Policy) adoptLoose(ctx context.Context, s Store, d flob.Digest) error {
	root, ok := rootOf(p.Archive)
	if !ok {
		return nil
	}

	vs, err := looseIn(root)
	if err != nil {
		return err
	}
	for _, v := range vs {
		if got, err := v.digest(); err != nil || got != d {
			continue
		}

		if err := v.adopt(ctx, p.manifested(s)); err != nil {
			return fmt.Errorf("%s: %w", v.name, err)
		}
	}

	return nil
}

// isRowFinding answers whether a finding is about a row's signature.
func isRowFinding(f Finding) bool {
	return f.Namespace != "" && (f.Kind == "unsigned" || f.Kind == "forged")
}

// vouch signs, as they stand, the rows whose signatures are findings: the
// manifest's rows, and the erased ones the latest two checkpoints still read
// back.
func (p Policy) vouch(ctx context.Context, s Store) error {
	rows, err := s.Rows(ctx)
	if err != nil {
		return fmt.Errorf("the manifest: %w", err)
	}
	latest, _, err := s.Latest(ctx)
	if err != nil {
		return fmt.Errorf("the manifest: %w", err)
	}
	for k := max(1, latest-1); latest > 0 && k <= latest; k++ {
		vs, err := s.At(ctx, k)
		if err != nil {
			return fmt.Errorf("the manifest: %w", err)
		}

		rows = append(rows, slices.DeleteFunc(vs, func(v Archived) bool { return v.State != Erased })...)
	}

	type of struct{ ns, d, state string }
	todo := map[of]bool{}
	trusted := p.trusted()
	for _, v := range rows {
		if kind, _ := p.rowFinding(v, trusted); kind != "" {
			todo[of{v.Namespace, v.Digest, v.State}] = true
		}
	}

	sign := p.vouching()
	order := func(a, b of) int {
		return cmp.Or(cmp.Compare(a.ns, b.ns), cmp.Compare(a.d, b.d), cmp.Compare(a.state, b.state))
	}
	for _, v := range slices.SortedFunc(maps.Keys(todo), order) {
		err = errors.Join(err, s.Mark(ctx, v.ns, v.d, v.state, v.state, sign))
	}

	return err
}
