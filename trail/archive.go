package trail

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"uuid"

	"github.com/lesomnus/flob"

	"github.com/lesomnus/payday/pdid"
)

// The namespaces of an archive that are not a tenant's. A tenant's is its
// identifier, as [pdid.Id.String] writes it; nothing else here starts with an
// underscore, and no identifier does.
const (
	// SharedNamespace holds the rows that name more than one tenant.
	//
	// A row a transfer wrote, or one an operator of one tenant wrote into
	// another, is readable by every tenant it names -- that is the wall's rule
	// on the trail -- so it is nobody's alone, and filing it under one of them
	// would let that one's window decide for the others. Each chunk here holds
	// the rows of one **set** of tenants and says which in its labels, so the
	// longest of their windows is answerable without opening it.
	SharedNamespace = "_shared"

	// DeploymentNamespace holds the rows that name no tenant at all: the
	// deployment writing to itself before anybody could ask it to. The
	// deployment's own policy decides them.
	DeploymentNamespace = "_deployment"

	// ReceiptNamespace holds the record of what was destroyed; see [Receipt].
	ReceiptNamespace = "_receipts"

	// LegacyNamespace holds the files a version wrote before the archive was
	// a flob store; see [Adopt]. They hold every tenant's rows together, so
	// the deployment's policy decides them, as it did.
	LegacyNamespace = "_legacy"
)

// The labels a chunk carries. Everything a pass decides is decided from these,
// without opening the chunk.
const (
	labelFormat  = "Format"
	labelKind    = "Kind"
	labelMonth   = "Month"
	labelRun     = "Run"
	labelRows    = "Rows"
	labelFirst   = "First"
	labelLast    = "Last"
	labelTenants = "Tenants"
	labelName    = "Name"
	labelForgot  = "Forgotten"
	labelAt      = "At"
	labelActs    = "Acts"
)

const (
	// formatRows is what a chunk is: protojson rows, one per line, gzipped.
	//
	// Kept, when the archive moved into flob, rather than traded for something
	// a single subject is faster to find in. A database file is fifty to a
	// hundred times quicker at that and five to nine times the size, and an
	// archive is written every day and read when somebody asks. A label
	// rather than a guess, so that a format after it is a value and not a
	// migration.
	formatRows = "jsonl+gzip"

	// formatReceipts is a chunk of [Receipt]s, one per line.
	formatReceipts = "receipts+jsonl+gzip"
)

// Chunk is one blob of the archive: rows of one kind and one month, written
// by one run, filed under one namespace.
//
// # Why the archive is chunks in namespaces
//
// It was a directory of files named for a month and a kind, and a name could
// carry everything a pass needed to decide. A tenant's window cannot be
// carried that way -- the same month of the same kind is destroyed on
// different days for different tenants -- so a chunk is filed under the
// tenant it belongs to ([SharedNamespace] for the rows that belong to several),
// and what the name used to say is in its labels.
//
// Which is also what makes a tenant leaving cheap: what is theirs is one
// namespace, and what is not only theirs says so.
type Chunk struct {
	// Namespace is whose it is: a tenant's identifier, or one of the
	// namespaces above.
	Namespace string

	// Digest is the blob, in that namespace.
	Digest flob.Digest

	// Kind is the kind of thing its rows are about, as [nameOf] writes one.
	// Empty for a file the first version of the format wrote, which said
	// nothing about its kind.
	Kind string

	// Month is the month its rows were written in.
	Month time.Time

	// Run is the pass that wrote it.
	Run string

	// Rows is how many rows it holds; zero for a file adopted from a
	// directory, which never said.
	Rows int

	// First and Last are when its oldest and newest rows were written; zero
	// for an adopted file, whose month is all there is.
	First time.Time
	Last  time.Time

	// Tenants is the set of tenants every row of a [SharedNamespace] chunk
	// names, in identifier order.
	Tenants []pdid.Id

	// Name is what an adopted file was called.
	Name string

	// Size is how many bytes the blob is.
	Size int64
}

// Before answers whether every row in it was written before `at`, which is
// what being destroyable at a cutoff means.
//
// From [Chunk.Last] when it is known. A file adopted from a directory knows
// only its month, and is destroyable when the month **after** it has also
// passed -- January goes when the cutoff has reached February, and not on the
// 31st -- which is the rule the directory had.
func (c Chunk) Before(at time.Time) bool {
	if !c.Last.IsZero() {
		return c.Last.Before(at)
	}

	return !c.Month.AddDate(0, 1, 0).After(at.UTC())
}

// String is the chunk as a line worth printing.
func (c Chunk) String() string {
	kind := c.Kind
	if kind == "" {
		kind = "-"
	}

	v := fmt.Sprintf("%s %s %s %s", c.Namespace, kind, c.Month.Format("2006-01"), c.Digest)
	if c.Name != "" {
		v += " (" + c.Name + ")"
	}

	return v
}

func (c Chunk) labels() flob.Labels {
	l := flob.Labels{}
	l.Set(labelFormat, formatRows)
	l.Set(labelMonth, c.Month.UTC().Format("2006-01"))
	if c.Kind != "" {
		l.Set(labelKind, c.Kind)
	}
	if c.Run != "" {
		l.Set(labelRun, c.Run)
	}
	if c.Rows > 0 {
		l.Set(labelRows, strconv.Itoa(c.Rows))
	}
	if !c.First.IsZero() {
		l.Set(labelFirst, c.First.UTC().Format(time.RFC3339Nano))
	}
	if !c.Last.IsZero() {
		l.Set(labelLast, c.Last.UTC().Format(time.RFC3339Nano))
	}
	if len(c.Tenants) > 0 {
		l.Set(labelTenants, joined(c.Tenants))
	}
	if c.Name != "" {
		l.Set(labelName, c.Name)
	}

	return l
}

// chunkOf reads a chunk back out of its labels, and answers false for a blob
// this package did not write -- which a destructive pass leaves alone rather
// than guesses about.
func chunkOf(ns string, d flob.Digest, l flob.Labels) (Chunk, bool) {
	if l.Get(labelFormat) != formatRows {
		return Chunk{}, false
	}

	month, err := time.ParseInLocation("2006-01", l.Get(labelMonth), time.UTC)
	if err != nil {
		return Chunk{}, false
	}

	c := Chunk{
		Namespace: ns,
		Digest:    d,
		Kind:      l.Get(labelKind),
		Month:     month,
		Run:       l.Get(labelRun),
		Name:      l.Get(labelName),
	}
	if v := l.Get(labelRows); v != "" {
		c.Rows, _ = strconv.Atoi(v)
	}
	if v := l.Get(labelFirst); v != "" {
		c.First, _ = time.Parse(time.RFC3339Nano, v)
	}
	if v := l.Get(labelLast); v != "" {
		c.Last, _ = time.Parse(time.RFC3339Nano, v)
	}
	if v := l.Get(labelTenants); v != "" {
		ids, ok := idsOf(v)
		if !ok {
			return Chunk{}, false
		}

		c.Tenants = ids
	}

	return c, true
}

// placeOf is where a row is filed, from the tenants it names.
func placeOf(tenants []pdid.Id) (string, []pdid.Id) {
	switch len(tenants) {
	case 0:
		return DeploymentNamespace, nil
	case 1:
		return tenants[0].String(), nil
	default:
		vs := slices.Clone(tenants)
		slices.SortFunc(vs, byId)

		return SharedNamespace, vs
	}
}

// tenantOf reads a tenant back out of its namespace.
func tenantOf(ns string) (pdid.Id, bool) {
	v, err := uuid.Parse(ns)
	if err != nil {
		return pdid.Nil, false
	}

	id := pdid.Id(v)
	if id.IsZero() || id.String() != ns {
		return pdid.Nil, false
	}

	return id, true
}

func byId(a, b pdid.Id) int { return uuid.UUID(a).Compare(uuid.UUID(b)) }

func joined(ids []pdid.Id) string {
	vs := make([]string, len(ids))
	for i, v := range ids {
		vs[i] = v.String()
	}

	return strings.Join(vs, ",")
}

func idsOf(v string) ([]pdid.Id, bool) {
	out := []pdid.Id{}
	for _, s := range strings.Split(v, ",") {
		u, err := uuid.Parse(strings.TrimSpace(s))
		if err != nil {
			return nil, false
		}

		out = append(out, pdid.Id(u))
	}

	return out, true
}

// newRun names one pass.
//
// Random rather than a timestamp or a process id: two replicas starting from
// the same cron minute would collide on the first, and a container that always
// comes up as pid 1 on the second. It is a label now and not a file name, so a
// collision would no longer corrupt anything -- but it is still what tells one
// pass's chunks from another's.
func newRun() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}

// How much of the trail one chunk holds before it is written, in rows and in
// bytes of the rows as they are. A chunk is written whole, so this is also how
// much a pass holds in memory, and how much a crash before the write leaves in
// the database to be moved again.
const (
	chunkRows = 16 * Batch
	chunkSize = 32 << 20
)

// writer gathers rows on their way to the archive, into one chunk per
// namespace, set of tenants, kind and month.
type writer struct {
	a      flob.Stores
	run    string
	groups map[groupKey]*group
	rows   int
	size   int
}

type groupKey struct {
	ns      string
	tenants string
	kind    pdid.Domain
	month   string
}

type group struct {
	tenants []pdid.Id
	docs    [][]byte
	keys    []any
	first   time.Time
	last    time.Time
}

func newWriter(a flob.Stores, run string) *writer {
	return &writer{a: a, run: run, groups: map[groupKey]*group{}}
}

func (w *writer) add(v Row) {
	ns, tenants := placeOf(v.Tenants)
	k := groupKey{ns: ns, tenants: joined(tenants), kind: v.Domain, month: Month(v.Created)}

	g, ok := w.groups[k]
	if !ok {
		g = &group{tenants: tenants, first: v.Created, last: v.Created}
		w.groups[k] = g
	}

	g.docs = append(g.docs, v.Doc)
	g.keys = append(g.keys, v.Key)
	if v.Created.Before(g.first) {
		g.first = v.Created
	}
	if v.Created.After(g.last) {
		g.last = v.Created
	}

	w.rows++
	w.size += len(v.Doc)
}

func (w *writer) full() bool { return w.rows >= chunkRows || w.size >= chunkSize }

// flush writes every group as a chunk and answers the keys of the rows the
// archive now holds, which are the ones that may be forgotten. On an error it
// still answers the keys of the chunks it did write.
func (w *writer) flush(ctx context.Context) ([]any, error) {
	ks := make([]groupKey, 0, len(w.groups))
	for k := range w.groups {
		ks = append(ks, k)
	}
	slices.SortFunc(ks, func(a, b groupKey) int {
		return cmp.Or(
			cmp.Compare(a.ns, b.ns),
			cmp.Compare(a.tenants, b.tenants),
			cmp.Compare(a.kind, b.kind),
			cmp.Compare(a.month, b.month),
		)
	})

	keys := []any{}
	for _, k := range ks {
		g := w.groups[k]
		month, _ := time.ParseInLocation("2006-01", k.month, time.UTC)

		c := Chunk{
			Namespace: k.ns,
			Kind:      nameOf(k.kind),
			Month:     month,
			Run:       w.run,
			Rows:      len(g.docs),
			First:     g.first,
			Last:      g.last,
			Tenants:   g.tenants,
		}
		if _, err := put(ctx, w.a, c, g.docs); err != nil {
			return keys, err
		}

		keys = append(keys, g.keys...)
		w.rows -= len(g.docs)
		for _, doc := range g.docs {
			w.size -= len(doc)
		}
		delete(w.groups, k)
	}

	return keys, nil
}

// put writes one chunk whole.
//
// Whole, and answered only once the store holds it: that is the point at
// which the rows in it may leave the database. A blob the store already has is
// not a failure -- it is the same rows, written by a replica that read them at
// the same moment, and they are in the archive either way.
func put(ctx context.Context, a flob.Stores, c Chunk, docs [][]byte) (flob.Digest, error) {
	buf := &bytes.Buffer{}
	z := gzip.NewWriter(buf)
	for _, doc := range docs {
		if _, err := z.Write(append(doc, '\n')); err != nil {
			return "", err
		}
	}
	if err := z.Close(); err != nil {
		return "", err
	}

	m, err := a.Use(c.Namespace).Add(ctx, flob.Meta{Labels: c.labels()}, buf)
	if err != nil && !errors.Is(err, flob.ErrAlreadyExists) {
		return "", fmt.Errorf("%s: %w", c.Namespace, err)
	}

	return m.Digest, nil
}

// stream adds a blob whose bytes `write` produces, without holding them all.
func stream(ctx context.Context, s flob.Store, l flob.Labels, write func(w io.Writer) error) (flob.Meta, error) {
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := write(w)
		w.CloseWithError(err)
		done <- err
	}()

	m, err := s.Add(ctx, flob.Meta{Labels: l}, r)

	// Whatever Add did, the writer must not be left blocked on a pipe nobody
	// reads.
	r.CloseWithError(errors.New("the store stopped reading"))
	if werr := <-done; werr != nil && err == nil {
		return m, werr
	}

	return m, err
}

var errUnwalkable = errors.New("the archive cannot list what it holds, so nothing could ever be destroyed from it")

// namespaces is every namespace the archive has, in order.
func namespaces(ctx context.Context, a flob.Stores) ([]string, error) {
	n, ok := flob.AsNamespacer(a)
	if !ok {
		return nil, errUnwalkable
	}

	out := []string{}
	for v, err := range n.Namespaces(ctx) {
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}
	slices.Sort(out)

	return out, nil
}

// chunksIn is every chunk in one namespace, oldest first.
//
// It reads every chunk's labels, which on a filesystem is a file each and on
// S3 a request each. That is the cost of deciding by labels, and it is paid
// once a pass per namespace.
func chunksIn(ctx context.Context, a flob.Stores, ns string) ([]Chunk, error) {
	w, ok := flob.AsWalker(a.Use(ns))
	if !ok {
		return nil, errUnwalkable
	}

	out := []Chunk{}
	for v, err := range w.Walk(ctx) {
		if err != nil {
			return out, err
		}

		l, err := v.Labels(ctx)
		if err != nil {
			if errors.Is(err, flob.ErrNotExist) {
				// Erased between the listing and the look, by another pass.
				continue
			}

			return out, err
		}

		c, ok := chunkOf(ns, v.Digest(), l)
		if !ok {
			continue
		}

		c.Size, _ = v.Size(ctx)
		out = append(out, c)
	}
	slices.SortFunc(out, chunkOrder)

	return out, nil
}

func chunkOrder(a, b Chunk) int {
	return cmp.Or(
		a.Month.Compare(b.Month),
		a.First.Compare(b.First),
		cmp.Compare(a.Namespace, b.Namespace),
		cmp.Compare(a.Kind, b.Kind),
		cmp.Compare(a.Digest, b.Digest),
	)
}

// Chunks is every chunk in the archive, oldest month first.
func Chunks(ctx context.Context, a flob.Stores) ([]Chunk, error) {
	nss, err := namespaces(ctx, a)
	if err != nil {
		return nil, err
	}

	out := []Chunk{}
	for _, ns := range nss {
		if ns == ReceiptNamespace {
			continue
		}

		vs, err := chunksIn(ctx, a, ns)
		if err != nil {
			return out, fmt.Errorf("%s: %w", ns, err)
		}

		out = append(out, vs...)
	}
	slices.SortFunc(out, chunkOrder)

	return out, nil
}

// Doomed is what [Purge] would destroy, and destroys nothing.
//
// Its own function rather than a flag on [Purge], so that the list a dry run
// prints is the list the real one acts on -- two passes that agree today are
// two passes.
//
// `cut` is asked per kind, because the second clock is per kind: an operating
// record and a person's are in one archive and are not destroyed on the same
// day. A kind it declines is left alone. A chunk goes when **every** row in it
// is older than the cutoff; see [Chunk.Before].
func Doomed(ctx context.Context, a flob.Stores, cut func(kind string) (time.Time, bool)) ([]Chunk, error) {
	vs, err := Chunks(ctx, a)
	if err != nil {
		return nil, err
	}

	out := []Chunk{}
	for _, c := range vs {
		before, ok := cut(c.Kind)
		if ok && c.Before(before) {
			out = append(out, c)
		}
	}

	return out, nil
}

// Purge destroys the chunks that are entirely older than the cutoff, in every
// namespace, and answers with what it destroyed.
//
// This is the end of the line and there is nothing after it. It is the act an
// operator takes by hand, with a cutoff of their own, and it answers to nobody's
// window: [Policy.Pass] is what applies the deployment's and the tenants'.
//
// It writes a [Receipt] of what it destroyed, including when it stopped part
// of the way.
func Purge(ctx context.Context, a flob.Stores, cut func(kind string) (time.Time, bool)) ([]Chunk, error) {
	vs, err := Doomed(ctx, a, cut)
	if err != nil {
		return nil, err
	}

	out := []Chunk{}
	for _, c := range vs {
		if err = ctx.Err(); err != nil {
			break
		}
		if err = a.Use(c.Namespace).Erase(ctx, c.Digest); err != nil {
			break
		}

		out = append(out, c)
	}

	r := receipts{}
	for _, c := range out {
		before, _ := cut(c.Kind)
		r.chunk("purge", c, before)
	}
	if werr := r.write(ctx, a); werr != nil && err == nil {
		err = werr
	}

	return out, err
}

// Read walks the archive and calls `fn` for each row, as the protojson document
// it was stored as. Every namespace, oldest month first.
//
// Duplicates are dropped by identifier, **across** the chunks rather than
// within one: a crash between writing a chunk and forgetting its rows leaves
// them in the database to be moved again, and a row in both is one row.
//
// The document is handed over rather than a message, because this package has
// no `Audit` type to unmarshal into -- see the note on the package. An app that
// wants one has a generated adapter that does it, `pd.TrailOf`.
func Read(ctx context.Context, a flob.Stores, fn func(doc []byte) error) error {
	vs, err := Chunks(ctx, a)
	if err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, c := range vs {
		if err := readChunk(ctx, a, c, seen, nil, fn); err != nil {
			return err
		}
	}

	return nil
}

// ReadTenant is the rows of the archive one tenant may read: the ones that name
// it, in any of the three columns, which is the wall's rule on the trail.
//
// Its own namespace, the chunks of [SharedNamespace] whose tenants include it,
// and the rows of [LegacyNamespace] that name it -- which is the one place it
// has to open a row to know, since a file from before held everybody's.
func ReadTenant(ctx context.Context, a flob.Stores, tenant pdid.Id, fn func(doc []byte) error) error {
	if tenant.IsZero() {
		return errors.New("no tenant")
	}

	vs := []Chunk{}
	for _, ns := range []string{tenant.String(), SharedNamespace, LegacyNamespace} {
		cs, err := chunksIn(ctx, a, ns)
		if err != nil {
			return fmt.Errorf("%s: %w", ns, err)
		}

		for _, c := range cs {
			if ns == SharedNamespace && !slices.Contains(c.Tenants, tenant) {
				continue
			}

			vs = append(vs, c)
		}
	}
	slices.SortFunc(vs, chunkOrder)

	seen := map[string]bool{}
	for _, c := range vs {
		var keep func(head) bool
		if c.Namespace == LegacyNamespace {
			keep = func(h head) bool { return h.names(tenant) }
		}
		if err := readChunk(ctx, a, c, seen, keep, fn); err != nil {
			return err
		}
	}

	return nil
}

// readChunk is one chunk's rows, less the ones `seen` has, and less the ones
// `keep` declines when there is one.
func readChunk(ctx context.Context, a flob.Stores, c Chunk, seen map[string]bool, keep func(head) bool, fn func([]byte) error) error {
	r, _, err := a.Use(c.Namespace).Open(ctx, c.Digest)
	if err != nil {
		if errors.Is(err, flob.ErrNotExist) {
			// Destroyed, merged or rewritten since it was listed. Whatever
			// replaced it is a chunk of its own.
			return nil
		}

		return fmt.Errorf("%s: %w", c, err)
	}
	defer r.Close()

	err = lines(r, func(doc []byte) error {
		h, err := headOf(doc)
		if err != nil {
			return err
		}
		if seen[h.Id] {
			return nil
		}
		if keep != nil && !keep(h) {
			return nil
		}
		seen[h.Id] = true

		return fn(doc)
	})
	if err != nil {
		return fmt.Errorf("%s: %w", c, err)
	}

	return nil
}

// lines is one gzip JSONL stream, row by row.
func lines(r io.Reader, fn func([]byte) error) error {
	z, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer z.Close()

	s := bufio.NewScanner(z)
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for s.Scan() {
		line := bytes.TrimSpace(s.Bytes())
		if len(line) == 0 {
			continue
		}

		// A copy, because the scanner's buffer is about to be reused and the
		// caller may keep what it is handed.
		doc := make([]byte, len(line))
		copy(doc, line)

		if err := fn(doc); err != nil {
			return err
		}
	}

	return s.Err()
}

// Forget blanks the contents of every archived row about one of these objects,
// and answers how many rows about them it reached.
//
// # Why the archive has to be reachable at all
//
// The retention policy is about **age** and reaches everybody's rows at once.
// A person asking to be forgotten is about a **subject**, and a mechanism that
// stopped at the database would be one that destroyed the copy an operator can
// see and left the copy in the archive beside it. That is not a retention
// policy with a gap; it is an answer that is wrong in the direction that
// matters.
//
// # What it blanks, and what it deliberately does not
//
// `value` and `patch`, which are the two columns that hold contents. Everything
// else -- who acted, what they did, which object, when -- stays, and stays on
// purpose: that is the record the trail exists to be, and it is what a
// legal-obligation exemption is an exemption *for*. What is destroyed is what
// the row said about somebody; what survives is that it happened.
//
// The actor is not touched. It is an identifier, and it is personal data only
// because it **resolves** -- which is a property of the row it points at rather
// than of this one. A caller that has destroyed the person's own record has
// already made it a pseudonym that reaches nothing, and blanking it here would
// destroy *who did this*, which is the whole of what a trail is for.
//
// # It rewrites chunks, which nothing else here does
//
// [Purge] destroys whole chunks and a pass only adds them, precisely so that
// an archive is never edited. This is the one act that edits one, and it is
// worth being explicit that it is an exception rather than an oversight: the
// alternative is a person's contents surviving in a place the deployment
// controls, which is the thing they asked to end.
//
// A chunk is written again beside itself, and the old one erased only once the
// store holds the new. It goes round again until a round finds nothing left to
// blank, because a pass merging chunks at the same moment can have copied a
// row out of the old one before it was rewritten.
//
// The objects are named as protojson writes them, base64; see the test that
// uses it.
func Forget(ctx context.Context, a flob.Stores, objects []string) (int, error) {
	if len(objects) == 0 {
		return 0, nil
	}

	of := make(map[string]bool, len(objects))
	marks := make([][]byte, 0, len(objects))
	for _, v := range objects {
		of[v] = true
		marks = append(marks, []byte(`"`+v+`"`))
	}

	n, rewritten := 0, 0
	var err error
	for round := 0; round < 3; round++ {
		var vs []Chunk
		vs, err = Chunks(ctx, a)
		if err != nil {
			break
		}

		k := 0
		for _, c := range vs {
			var hits int
			var dirty bool
			hits, dirty, err = scan(ctx, a, c, of, marks)
			if err != nil {
				break
			}
			if round == 0 {
				n += hits
			}
			if !dirty {
				continue
			}
			if err = rewrite(ctx, a, c, of, marks); err != nil {
				break
			}

			k++
		}

		rewritten += k
		if err != nil || k == 0 {
			break
		}
	}

	r := receipts{}
	if n > 0 {
		r.add(Receipt{Act: "forget", Where: "archive", Rows: n, Chunks: rewritten, Objects: len(objects)})
	}
	if werr := r.write(ctx, a); werr != nil && err == nil {
		err = werr
	}

	return n, err
}

// scan answers how many rows of one chunk are about these objects, and whether
// any of them still has contents to lose.
func scan(ctx context.Context, a flob.Stores, c Chunk, of map[string]bool, marks [][]byte) (int, bool, error) {
	r, _, err := a.Use(c.Namespace).Open(ctx, c.Digest)
	if err != nil {
		if errors.Is(err, flob.ErrNotExist) {
			return 0, false, nil
		}

		return 0, false, err
	}
	defer r.Close()

	n, dirty := 0, false
	err = lines(r, func(line []byte) error {
		if !mentions(line, marks) {
			return nil
		}

		_, hit, had, err := blanked(line, of)
		if err != nil {
			return err
		}
		if hit {
			n++
		}
		if had {
			dirty = true
		}

		return nil
	})
	if err != nil {
		return n, dirty, fmt.Errorf("%s: %w", c, err)
	}

	return n, dirty, nil
}

// rewrite writes a chunk again with these objects' contents taken out, and
// erases the one it replaces.
func rewrite(ctx context.Context, a flob.Stores, c Chunk, of map[string]bool, marks [][]byte) error {
	s := a.Use(c.Namespace)

	r, _, err := s.Open(ctx, c.Digest)
	if err != nil {
		if errors.Is(err, flob.ErrNotExist) {
			return nil
		}

		return err
	}
	defer r.Close()

	l := c.labels()
	l.Set(labelForgot, time.Now().UTC().Format(time.RFC3339Nano))

	m, err := stream(ctx, s, l, func(w io.Writer) error {
		z := gzip.NewWriter(w)
		err := lines(r, func(line []byte) error {
			if mentions(line, marks) {
				out, _, _, err := blanked(line, of)
				if err != nil {
					return err
				}

				line = out
			}

			_, err := z.Write(append(line, '\n'))
			return err
		})
		if err != nil {
			return err
		}

		return z.Close()
	})
	if err != nil && !errors.Is(err, flob.ErrAlreadyExists) {
		return fmt.Errorf("%s: %w", c, err)
	}
	if m.Digest == c.Digest {
		return nil
	}

	return s.Erase(ctx, c.Digest)
}

// mentions is the cheap half of the question: whether the line has one of the
// objects in it anywhere, before it is parsed to ask whether as its object.
func mentions(line []byte, marks [][]byte) bool {
	for _, v := range marks {
		if bytes.Contains(line, v) {
			return true
		}
	}

	return false
}

// blanked is one line with its contents taken out, if it is about one of them.
// It answers whether it was, and whether there was anything to take out.
//
// Through generic JSON rather than the message, because this package has no
// `Audit` type -- see the note on the package. `value`, `patch` and `objectId`
// are the names protojson gives those fields, and they are payday's own
// columns, so there is nothing here an app can move.
//
// Counted as a hit whether or not it had contents to lose. What is being
// answered is *how many rows about this person were reached*, which is what
// somebody wants to hear back; a row that was already empty is still one this
// is responsible for.
func blanked(line []byte, of map[string]bool) ([]byte, bool, bool, error) {
	var v map[string]json.RawMessage
	if err := json.Unmarshal(line, &v); err != nil {
		return nil, false, false, err
	}

	var object string
	if raw, ok := v["objectId"]; ok {
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, false, false, err
		}
	}
	if object == "" || !of[object] {
		return line, false, false, nil
	}

	_, value := v["value"]
	_, patch := v["patch"]
	if !value && !patch {
		return line, true, false, nil
	}

	delete(v, "value")
	delete(v, "patch")

	out, err := json.Marshal(v)
	if err != nil {
		return nil, false, false, err
	}

	return out, true, true, nil
}

// merge folds chunks of one namespace, kind, month and set of tenants into one,
// and erases the ones it folded.
//
// A daily pass writes a chunk per tenant per kind per day, which is thirty a
// month and, without this, a store that grows a blob a day for every tenant
// for as long as the archive is kept. Once a month can receive no more rows --
// see the pass -- its chunks are one.
//
// The fold is deterministic, so two replicas folding the same chunks write the
// same blob, and the store's answer that it already has it is the same as its
// answer that it took it. What neither may do is erase the result: a source is
// erased only when it is not what the fold produced.
func merge(ctx context.Context, a flob.Stores, run string, cs []Chunk) (Chunk, error) {
	cs = slices.Clone(cs)
	slices.SortFunc(cs, func(a, b Chunk) int {
		return cmp.Or(a.First.Compare(b.First), cmp.Compare(a.Digest, b.Digest))
	})

	out := Chunk{
		Namespace: cs[0].Namespace,
		Kind:      cs[0].Kind,
		Month:     cs[0].Month,
		Run:       run,
		Tenants:   cs[0].Tenants,
	}
	for _, c := range cs {
		if out.First.IsZero() || (!c.First.IsZero() && c.First.Before(out.First)) {
			out.First = c.First
		}
		if c.Last.After(out.Last) {
			out.Last = c.Last
		}
	}

	s := a.Use(out.Namespace)
	rows := 0
	m, err := stream(ctx, s, out.labels(), func(w io.Writer) error {
		z := gzip.NewWriter(w)
		seen := map[string]bool{}
		for _, c := range cs {
			r, _, err := s.Open(ctx, c.Digest)
			if err != nil {
				if errors.Is(err, flob.ErrNotExist) {
					continue
				}

				return err
			}

			err = lines(r, func(line []byte) error {
				id, err := identifier(line)
				if err != nil {
					return err
				}
				if seen[id] {
					return nil
				}
				seen[id] = true
				rows++

				_, err = z.Write(append(line, '\n'))
				return err
			})
			r.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", c, err)
			}
		}

		return z.Close()
	})
	if err != nil && !errors.Is(err, flob.ErrAlreadyExists) {
		return Chunk{}, err
	}

	out.Digest = m.Digest
	out.Rows = rows
	if err := s.Label(ctx, out.Digest, out.labels()); err != nil {
		return out, err
	}

	for _, c := range cs {
		if c.Digest == out.Digest {
			continue
		}
		if err := s.Erase(ctx, c.Digest); err != nil {
			return out, err
		}
	}

	return out, nil
}

// Adopt takes the files a version before this one wrote into a directory, and
// files them under [LegacyNamespace].
//
// By hard link when the archive is the same directory on the same disk --
// `flob.OsStore.Adopt`, which copies nothing -- and by copying otherwise. Each
// file is removed from the directory once the archive holds it, so adopting
// again finds nothing and the directory is the store's alone.
//
// Their rows hold every tenant's together, so they are decided by the
// deployment's policy, as they were: by kind, and by the month in their name.
//
// [Policy.Pass] does this first, so a deployment upgrading has nothing to run.
func Adopt(ctx context.Context, a flob.Stores, dir string) (int, error) {
	vs, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}

		return 0, err
	}

	n := 0
	for _, v := range vs {
		if !v.Type().IsRegular() {
			continue
		}

		month, kind, run, ok := partsOf(v.Name())
		if !ok {
			// A file in the directory that this did not write. Left alone: a
			// pass over a directory is not the place to guess.
			continue
		}

		info, err := v.Info()
		if err != nil {
			return n, err
		}

		path := filepath.Join(dir, v.Name())
		c := Chunk{Namespace: LegacyNamespace, Kind: kind, Month: month, Run: run, Name: v.Name()}
		if err := adopt(ctx, a, c, path, info.ModTime()); err != nil {
			return n, fmt.Errorf("%s: %w", v.Name(), err)
		}
		if err := os.Remove(path); err != nil {
			return n, err
		}

		n++
	}

	return n, nil
}

func adopt(ctx context.Context, a flob.Stores, c Chunk, path string, at time.Time) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	d, err := flob.DigestFromReader(f)
	f.Close()
	if err != nil {
		return err
	}

	s := a.Use(LegacyNamespace)
	m := flob.Meta{Digest: d, Labels: c.labels()}

	if o, ok := s.(flob.OsStore); ok {
		_, err := o.Adopt(ctx, m, path, flob.AdoptOptions{Added: at, Verify: true})
		if err == nil || errors.Is(err, flob.ErrAlreadyExists) {
			return nil
		}
		if !errors.Is(err, syscall.EXDEV) && !errors.Is(err, fs.ErrPermission) {
			return err
		}

		// Another filesystem, or a file it may not link. Copied instead.
	}

	f, err = os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := s.Add(ctx, m, f); err != nil && !errors.Is(err, flob.ErrAlreadyExists) {
		return err
	}

	return nil
}

// partsOf reads back what a file of the directory format was named for: the
// month, the kind, and the run.
//
// A name it cannot read a month out of is a file this did not write, and one
// with no kind in it is a file the first version of the format wrote --
// answered as the empty kind, which [Kinds.CutFor] gives to whichever pass is
// about everything else.
func partsOf(name string) (time.Time, string, string, bool) {
	v := strings.TrimSuffix(name, Ext)
	if v == name {
		return time.Time{}, "", "", false
	}

	v, ok := strings.CutPrefix(v, "audit-")
	if !ok {
		return time.Time{}, "", "", false
	}

	vs := strings.Split(v, ".")

	at, err := time.ParseInLocation("2006-01", vs[0], time.UTC)
	if err != nil {
		return time.Time{}, "", "", false
	}
	if len(vs) < 3 {
		return at, "", "", true
	}

	return at, vs[1], vs[2], true
}

// Receipt is the record that something stopped existing: when, what, whose,
// and how much -- and never what it said.
//
// Every destruction writes one: the rows a pass discards on their way out of
// the database, the chunks it destroys at the end of their window, a [Purge]
// by hand, and the rows [Forget] blanks. What it is for is the question a
// customer or a regulator asks afterwards, *show me that it was deleted*, which
// a log answers only for as long as the log is kept and only to somebody who
// can read it.
//
// They are kept in [ReceiptNamespace] and nothing destroys them: they hold no
// contents, and their whole worth is outliving what they describe. A deployment
// with no archive has nowhere to keep them, and has the log.
type Receipt struct {
	// At is when it happened.
	At time.Time `json:"at"`

	// Act is `discard`, `destroy`, `purge` or `forget`.
	Act string `json:"act"`

	// Where is `database` or `archive`.
	Where string `json:"where"`

	// Tenant is whose: a tenant's identifier, a namespace, or empty for every
	// tenant at once.
	Tenant string `json:"tenant,omitempty"`

	// Tenants is the set, for a chunk of [SharedNamespace].
	Tenants []string `json:"tenants,omitempty"`

	// Kind is what kind of thing, or `*` for everything a policy did not name.
	Kind string `json:"kind,omitempty"`

	// Month is the month the rows were written in, for an archive's.
	Month string `json:"month,omitempty"`

	// Before is the cutoff: every row it describes was written before it.
	Before time.Time `json:"before,omitzero"`

	// Rows and Chunks are how much.
	Rows   int `json:"rows"`
	Chunks int `json:"chunks,omitempty"`

	// Objects is how many subjects [Forget] was asked about. Their identifiers
	// are not here: a receipt is the one record of an erasure that is kept,
	// and keeping *who* would keep what was erased.
	Objects int `json:"objects,omitempty"`
}

// receipts gathers one act's receipts, to be written as one blob.
type receipts struct {
	vs []Receipt
}

func (r *receipts) add(v Receipt) {
	if v.At.IsZero() {
		v.At = time.Now().UTC()
	}

	r.vs = append(r.vs, v)
}

// chunk counts one chunk destroyed, into the receipt for its namespace and kind.
func (r *receipts) chunk(act string, c Chunk, before time.Time) {
	for i, v := range r.vs {
		if v.Act == act && v.Tenant == c.Namespace && v.Kind == c.Kind && v.Month == Month(c.Month) &&
			slices.Equal(v.Tenants, ids(c.Tenants)) {
			r.vs[i].Rows += c.Rows
			r.vs[i].Chunks++

			return
		}
	}

	r.add(Receipt{
		Act:     act,
		Where:   "archive",
		Tenant:  c.Namespace,
		Tenants: ids(c.Tenants),
		Kind:    c.Kind,
		Month:   Month(c.Month),
		Before:  before.UTC(),
		Rows:    c.Rows,
		Chunks:  1,
	})
}

func ids(vs []pdid.Id) []string {
	if len(vs) == 0 {
		return nil
	}

	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.String()
	}

	return out
}

// write keeps them, as one blob in [ReceiptNamespace].
func (r *receipts) write(ctx context.Context, a flob.Stores) error {
	if a == nil || len(r.vs) == 0 {
		return nil
	}

	buf := &bytes.Buffer{}
	z := gzip.NewWriter(buf)
	acts := []string{}
	rows := 0
	for _, v := range r.vs {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := z.Write(append(b, '\n')); err != nil {
			return err
		}
		if !slices.Contains(acts, v.Act) {
			acts = append(acts, v.Act)
		}

		rows += v.Rows
	}
	if err := z.Close(); err != nil {
		return err
	}
	slices.Sort(acts)

	l := flob.Labels{}
	l.Set(labelFormat, formatReceipts)
	l.Set(labelAt, time.Now().UTC().Format(time.RFC3339Nano))
	l.Set(labelActs, strings.Join(acts, ","))
	l.Set(labelRows, strconv.Itoa(rows))

	if _, err := a.Use(ReceiptNamespace).Add(ctx, flob.Meta{Labels: l}, buf); err != nil &&
		!errors.Is(err, flob.ErrAlreadyExists) {
		return fmt.Errorf("receipts: %w", err)
	}

	r.vs = nil

	return nil
}

// Receipts reads every receipt the archive keeps, oldest first.
func Receipts(ctx context.Context, a flob.Stores, fn func(Receipt) error) error {
	w, ok := flob.AsWalker(a.Use(ReceiptNamespace))
	if !ok {
		return errUnwalkable
	}

	type blob struct {
		d  flob.Digest
		at string
	}

	bs := []blob{}
	for v, err := range w.Walk(ctx) {
		if err != nil {
			return err
		}

		l, err := v.Labels(ctx)
		if err != nil {
			return err
		}
		if l.Get(labelFormat) != formatReceipts {
			continue
		}

		bs = append(bs, blob{d: v.Digest(), at: l.Get(labelAt)})
	}
	slices.SortFunc(bs, func(a, b blob) int { return cmp.Or(cmp.Compare(a.at, b.at), cmp.Compare(a.d, b.d)) })

	vs := []Receipt{}
	for _, b := range bs {
		r, _, err := a.Use(ReceiptNamespace).Open(ctx, b.d)
		if err != nil {
			return err
		}

		err = lines(r, func(line []byte) error {
			var v Receipt
			if err := json.Unmarshal(line, &v); err != nil {
				return err
			}

			vs = append(vs, v)
			return nil
		})
		r.Close()
		if err != nil {
			return err
		}
	}
	slices.SortStableFunc(vs, func(a, b Receipt) int { return a.At.Compare(b.At) })

	for _, v := range vs {
		if err := fn(v); err != nil {
			return err
		}
	}

	return nil
}

// encoded is an identifier as protojson writes one.
func encoded(v pdid.Id) string { return base64.StdEncoding.EncodeToString(v.Bytes()) }
