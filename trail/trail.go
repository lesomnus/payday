// Package trail is what happens to the record of what happened, after long
// enough.
//
// # Why payday owns this
//
// `schema/payday/audit.proto` asks for it in a sentence that read as a caveat
// and was a task: *the trail outlives what it names, so a softly erased row's
// contents live on here. An app with an obligation to destroy data has to
// reckon with the trail, and the answer is a retention policy rather than an
// empty column.* And `proto/payday/entity.proto` gives the same gap as the
// reason an entity may declare `hard:` at all -- *payday has no retention story
// to offer instead.*
//
// The `Audit` entity is payday's, the recorder that fills it is payday's, and
// the service that refuses to let anybody write to it is payday's. Every app on
// payday gets that table and every one of them has it grow forever. An answer
// written in one app is a format the next app cannot read, a sweep with its own
// bugs, and -- for the app that never gets round to it -- a table that is the
// deployment's largest and a compliance obligation nobody has met.
//
// What stays the app's is the values: how long, where, and whether any of it is
// served. Those come from what the app is regulated as, which payday cannot
// know. See [Policy] and `config.AuditConfig`.
//
// # Where the line between this and generated code is
//
// `internal/pdgen/outbox.go` drew it already, about the drain: *it is generated
// rather than written in the runtime for the reason every other layer is,
// `ent.Client` and the predicates are the app's types and payday cannot name
// them. What is not generated is any judgement.*
//
// So the app's generated code supplies a [Store] -- query rows past a cutoff,
// hand them over as documents, delete the ones that were handed over -- and
// everything with a decision in it is here: the two clocks, the refusal to
// destroy what was never written, the archive's layout, the order of the write
// and the delete, and what may be purged.
//
// It also means this package names no `Audit` Go type, and could not: payday's
// copy of the schema is generated **into each app**, so `payday.Audit` has no
// Go type upstream at all. What travels between the two halves is the
// protojson document, which is the archive's format anyway.
//
// # And why none of it is an RPC
//
// The generated layer in front of `AuditService` refuses every write -- *"the
// trail is written by what happened, not by anybody asking"*. What a trail is
// worth is that the credential which lets somebody act is not the credential
// that lets them erase the record of having acted, and a key that prunes is a
// stolen key that prunes.
//
// So both doors need the database: a command at a shell, and [Sweep] inside the
// process.
package trail

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
)

// Ext is what an archive file is called, after the month it holds.
const Ext = ".jsonl.gz"

// Row is one line of the trail on its way out of the database.
//
// The document is protojson of the app's `Audit` message -- the archive's
// format, and the only shape both halves of this package can name. The key is
// whatever the app deletes by, handed straight back to [Store.Forget] without
// being looked at.
type Row struct {
	// Doc is the row, marshalled.
	Doc []byte

	// Key is what identifies it to the app.
	Key any

	// Domain is what kind of thing the row is about, which is what decides
	// whose policy applies to it. See [Policy].
	Domain pdid.Domain

	// Created is when it was written, which is what decides which month's file
	// it belongs in.
	Created time.Time
}

// Rows is one batch of them, oldest first.
type Rows []Row

// Kinds is which of them a pass is about.
//
// Two shapes because a policy has two: the kinds it names, and everything else.
// A pass over *everything else* cannot be a loop over domains -- the deployment
// has kinds this policy has never heard of, and new ones arrive with the next
// entity -- so it is one query saying which to leave alone.
type Kinds struct {
	// Only, when set, is the kinds this pass is about and nothing else.
	Only []pdid.Domain

	// Except, when Only is empty, is the kinds this pass leaves to their own.
	Except []pdid.Domain
}

// Only is the pass for the kinds a policy names.
func Only(ds ...pdid.Domain) Kinds { return Kinds{Only: ds} }

// Except is the pass for everything a policy did not name.
func Except(ds ...pdid.Domain) Kinds { return Kinds{Except: ds} }

// Has answers whether a kind belongs to this pass.
func (k Kinds) Has(d pdid.Domain) bool {
	if len(k.Only) > 0 {
		return slices.Contains(k.Only, d)
	}

	return !slices.Contains(k.Except, d)
}

// All answers whether this pass is about everything there is.
func (k Kinds) All() bool { return len(k.Only) == 0 && len(k.Except) == 0 }

// cut is this pass's cutoff, asked of an archive by the name it carries.
//
// A file whose name says nothing about its kind -- which is what the first
// version of the format wrote -- belongs to whichever pass is about everything
// **else**, since that is where a row of an unknown kind would have gone.
func (k Kinds) cut(before time.Time) func(domain string) (time.Time, bool) {
	return k.CutFor(before)
}

// CutFor is [Kinds.cut] for a caller outside this package -- a command, or a
// test -- which is the same question asked by hand: *destroy this pass's kinds,
// as far back as this*.
func (k Kinds) CutFor(before time.Time) func(domain string) (time.Time, bool) {
	return func(domain string) (time.Time, bool) {
		d, ok := domainOf(domain)
		if !ok {
			return before, k.All() || len(k.Only) == 0
		}
		if !k.Has(d) {
			return time.Time{}, false
		}

		return before, true
	}
}

// Store is the app's half: the audit table, as much of it as this needs.
//
// Generated rather than written, for `internal/pdgen/outbox.go`'s reason -- the
// ent client and its predicates are the app's types. What is asked of it has no
// judgement in it: read a batch of one kind older than an instant, count them,
// forget the ones that were named.
//
// It is deliberately a **bulk** interface, which `auth/authsession.Store` is
// deliberately not. That one has `Put`, `Get` and `Del` and no pass over
// everything, because a store over a hundred million rows should not be walked
// by whoever happens to be signing in. This is the opposite job: nothing here
// is on a request path, and a pass over everything is the whole of it.
type Store interface {
	// Older answers up to `limit` rows of these kinds written before `at`,
	// oldest first.
	Older(ctx context.Context, of Kinds, at time.Time, limit int) (Rows, error)

	// Count is how many of these kinds are past the cutoff, for a dry run.
	Count(ctx context.Context, of Kinds, at time.Time) (int, error)

	// Forget removes exactly the rows these keys name, and answers how many
	// went. A key it does not find is not an error: another writer reached it
	// first, which is a thing that happens rather than a thing to fail over.
	Forget(ctx context.Context, keys []any) (int, error)
}

// Batch is how many rows one pass reads and removes at a time.
//
// A first run on a deployment that has never pruned is the whole table, and one
// statement over it is a transaction holding locks for as long as it takes and
// a delete that either finishes or achieves nothing. Batched, an interrupted
// run has still moved everything it wrote.
const Batch = 1000

// Archive writes every row older than `before` into `dir`, then removes exactly
// the rows it wrote. It answers with how many moved.
//
// # The order, and why it is not a flag
//
// Written, flushed, `fsync`ed, closed -- and only then deleted, by the keys of
// the rows that are actually in the file. Not by asking for "everything older
// than `before`" a second time: a second query matches whatever is true when it
// runs rather than what was written, so a row backdated by a clock that stepped
// or written by a replica whose idea of now is behind is a row the second query
// removes and the file does not have.
//
// The failure that is left is a crash between the sync and the delete, and it
// leaves the rows in **both** places. That is the direction to fail in, and
// [Read] drops the duplicate.
//
// # An empty dir is refused
//
// Deleting without keeping is a thing a deployment may genuinely want, and it
// is not a thing to arrive at by leaving a field blank. See [Collect], which is
// what that deployment calls.
func Archive(ctx context.Context, s Store, of Kinds, before time.Time, dir string) (int, error) {
	if dir == "" {
		return 0, errors.New("no directory to archive into")
	}

	w, err := NewWriter(dir)
	if err != nil {
		return 0, err
	}

	return move(ctx, s, of, before, w)
}

// Collect removes rows older than `before` and keeps no copy.
//
// Separate from [Archive] rather than the same call with an empty directory,
// because the two are different acts and one of them is irreversible. A
// deployment that means it says so.
func Collect(ctx context.Context, s Store, of Kinds, before time.Time) (int, error) {
	return move(ctx, s, of, before, nil)
}

// Leave is whichever of the two a policy asked for, which is the one thing that
// varies between them.
func Leave(ctx context.Context, s Store, of Kinds, before time.Time, dir string) (int, error) {
	if dir == "" {
		return Collect(ctx, s, of, before)
	}

	return Archive(ctx, s, of, before, dir)
}

func move(ctx context.Context, s Store, of Kinds, before time.Time, w *Writer) (int, error) {
	moved := 0
	for {
		vs, err := s.Older(ctx, of, before, Batch)
		if err != nil {
			return moved, err
		}
		if len(vs) == 0 {
			return moved, nil
		}

		if w != nil {
			if err := w.Write(vs); err != nil {
				return moved, err
			}
		}

		keys := make([]any, len(vs))
		for i, v := range vs {
			keys[i] = v.Key
		}

		n, err := s.Forget(ctx, keys)
		if err != nil {
			return moved, err
		}

		moved += n

		if len(vs) < Batch {
			return moved, nil
		}
	}
}

// Month is the part of an archive's name that says what is in it.
//
// UTC, so that a deployment does not file the same instant in two months
// depending on where the machine thinks it is.
func Month(at time.Time) string { return at.UTC().Format("2006-01") }

// Named is the file a row of this date belongs in, for one run.
//
// **A month and a run**, and the run half is not decoration. The first version
// was the month alone, appended to, on the reasoning that concatenated gzip
// members are a valid stream and so a file need never be rewritten. That is
// true of one writer.
//
// There is not one writer. [Sweep] takes no lock -- neither does the generated
// outbox drain, and its comment says so: *nothing here takes a lock, so two of
// these drain the same rows.* For the drain that is wasted work. Here two
// writers interleave gzip members **inside one member**, because a
// `gzip.Writer` flushes to the file in chunks of its own choosing, and what
// lands is not a gzip stream at all. Two replicas, or an operator pruning while
// the process sweeps, and the month is unreadable.
//
// A run writes its own files, so writers never share one. What it costs is more
// files, and the duplicate rows two writers produce -- which [Read] already
// drops, because a crash between the sync and the delete could always leave
// them.
//
// The month stays **first** so that [Doomed] can answer *is all of this old
// enough* from the name, without opening anything.
func Named(at time.Time, d pdid.Domain, run string) string {
	return "audit-" + Month(at) + "." + nameOf(d) + "." + run + Ext
}

// nameOf is the kind, as a name a person writes in a configuration file.
//
// The schema's own, through `pdid`, so `holder` and `robot` rather than 2 and
// 17. A domain nothing registered -- which includes zero, the one number no
// entity may hold -- is written as its number, because a file has to be named
// something and a number is at least true.
func nameOf(d pdid.Domain) string {
	if v, ok := pdid.Domains()[d]; ok && v != "" {
		return v
	}

	return fmt.Sprintf("d%d", uint8(d))
}

// domainOf reads a name back, from a configuration file or from an archive's
// own name.
func domainOf(v string) (pdid.Domain, bool) {
	if d, ok := pdid.DomainOf(v); ok {
		return d, true
	}

	n, err := strconv.ParseUint(strings.TrimPrefix(v, "d"), 10, 8)
	if err != nil || !strings.HasPrefix(v, "d") {
		return pdid.Unknown, false
	}

	return pdid.Domain(n), true
}

// DomainOf is [domainOf] for a configuration block, which has to refuse a name
// nothing answers to rather than sweep on a kind that does not exist.
func DomainOf(v string) (pdid.Domain, error) {
	d, ok := domainOf(strings.ToLower(strings.TrimSpace(v)))
	if ok {
		return d, nil
	}

	names := []string{}
	for _, n := range pdid.Domains() {
		if n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	return pdid.Unknown, fmt.Errorf("%q is not a kind this app has; it has %s",
		v, strings.Join(names, ", "))
}

// Writer is one pass, collecting documents into that pass's own files.
type Writer struct {
	dir string
	run string
}

// NewWriter makes the directory if it is not there and picks this run's name.
func NewWriter(dir string) (*Writer, error) {
	if dir == "" {
		return nil, errors.New("no directory to archive into")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}

	// Random rather than a timestamp or a process id: two replicas starting
	// from the same cron minute would collide on the first, and a container
	// that always comes up as pid 1 on the second.
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}

	return &Writer{dir: dir, run: hex.EncodeToString(b)}, nil
}

// Run is what tells this pass's files from another's.
func (w *Writer) Run() string { return w.run }

// Write appends a batch to this run's file for the month and kind each row
// belongs to, and returns once every byte is on the disk.
//
// Split by kind as well as by month, because that is what makes the second
// clock answerable: [Purge] decides from the name, so a file holding two kinds
// with two `destroy` windows would be a file that is half destroyable.
func (w *Writer) Write(vs Rows) error {
	byName := map[string][][]byte{}
	for _, v := range vs {
		name := Named(v.Created, v.Domain, w.run)
		byName[name] = append(byName[name], v.Doc)
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := appendTo(filepath.Join(w.dir, name), byName[name]); err != nil {
			return err
		}
	}

	return nil
}

func appendTo(path string, docs [][]byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()

	// Built whole and written once. A `gzip.Writer` over the file would flush
	// in chunks of its own choosing, and a chunk is where a second writer gets
	// in -- see [Named]. This is belt beside those braces: the run id already
	// means nobody else has this file open, and a member that reaches the disk
	// in one write cannot be half of one either.
	buf := &bytes.Buffer{}

	z := gzip.NewWriter(buf)
	for _, doc := range docs {
		if _, err := z.Write(append(doc, '\n')); err != nil {
			return err
		}
	}
	if err := z.Close(); err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return err
	}

	// The whole point of the ordering. Until this returns, what is in the file
	// is what the operating system intends to write, and the delete after it is
	// about to make this copy the only one.
	return f.Sync()
}

// Read walks archives in the order given and calls `fn` for each row, as the
// protojson document it was stored as.
//
// Duplicates are dropped by identifier, **across** the files rather than within
// one: two writers that reached the same month wrote two files, and a row in
// both is one row. It is the same drop that makes [Archive]'s crash window
// cheap.
//
// The document is handed over rather than a message, because this package has
// no `Audit` type to unmarshal into -- see the note on the package. An app that
// wants one has a generated reader that does it.
func Read(paths []string, fn func(doc []byte) error) error {
	seen := map[string]bool{}

	for _, path := range paths {
		if err := read(path, seen, fn); err != nil {
			return err
		}
	}

	return nil
}

func read(path string, seen map[string]bool, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	z, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer z.Close()

	s := bufio.NewScanner(z)
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for s.Scan() {
		line := bytes.TrimSpace(s.Bytes())
		if len(line) == 0 {
			continue
		}

		k, err := identifier(line)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if seen[k] {
			continue
		}
		seen[k] = true

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

// head is the two fields this package reads out of a document it cannot
// otherwise interpret.
//
// protojson writes `bytes` as base64, which is a JSON string, so ordinary JSON
// reaches it without a descriptor. That is one of the reasons the archive is
// protojson and not the wire format: the half of payday that owns the files can
// still tell whether it has seen a row, without a Go type it does not have.
type head struct {
	Id string `json:"id"`
}

func identifier(doc []byte) (string, error) {
	var v head
	if err := json.Unmarshal(doc, &v); err != nil {
		return "", err
	}
	if v.Id == "" {
		return "", errors.New("a row in the archive has no identifier")
	}

	return v.Id, nil
}

// Files is every archive in `dir`, oldest month first.
func Files(dir string) ([]string, error) {
	vs, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	out := []string{}
	for _, v := range vs {
		if v.IsDir() || !strings.HasSuffix(v.Name(), Ext) {
			continue
		}

		out = append(out, filepath.Join(dir, v.Name()))
	}
	sort.Strings(out)

	return out, nil
}

// Purge destroys the archives that are entirely older than `before`, and
// answers with what it removed.
//
// This is the end of the line and there is nothing after it. It is separate
// from [Archive] because it answers to a different clock and a different
// person: how long the hot table carries a row is an operational choice, and
// how long the record exists at all is the obligation.
func Purge(ctx context.Context, dir string, cut func(kind string) (time.Time, bool)) ([]string, error) {
	vs, err := Doomed(dir, cut)
	if err != nil {
		return nil, err
	}

	out := []string{}
	for _, path := range vs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := os.Remove(path); err != nil {
			return out, err
		}

		out = append(out, path)
	}

	return out, nil
}

// Before is the cutoff that is the same for every kind, which is what an
// operator at a shell means by `--older-than`.
func Before(at time.Time) func(string) (time.Time, bool) {
	return func(string) (time.Time, bool) { return at, true }
}

// Doomed is what [Purge] would remove, and removes nothing.
//
// Its own function rather than a flag on [Purge], so that the list a dry run
// prints is the list the real one acts on -- two passes that agree today are
// two passes.
//
// **Entirely** older, which is what the month in a name is for: a file named
// for a month holds nothing after it, so one is removable when the month
// **after** it has also passed. January goes when the cutoff has reached
// February, and not on the 31st.
//
// `cut` is asked per kind, because the second clock is per kind: an operating
// record and a person's are in the same directory and are not destroyed on the
// same day. A kind it declines is left alone.
func Doomed(dir string, cut func(kind string) (time.Time, bool)) ([]string, error) {
	vs, err := Files(dir)
	if err != nil {
		return nil, err
	}

	out := []string{}
	for _, path := range vs {
		at, kind, ok := partsOf(filepath.Base(path))
		if !ok {
			// A file in the directory that this did not write. Left alone: a
			// destructive pass over a directory is not the place to guess.
			continue
		}

		before, ok := cut(kind)
		if !ok {
			continue
		}
		if end := at.AddDate(0, 1, 0); end.After(before.UTC()) {
			continue
		}

		out = append(out, path)
	}

	return out, nil
}

// partsOf reads back the two halves of [Named] that a destructive pass needs:
// the month, and the kind.
//
// The run is not parsed and is not looked at. A name it cannot read a month out
// of is a file this did not write, and one with no kind in it is a file the
// first version of the format wrote -- answered as the empty kind, which
// [Kinds.cut] gives to whichever pass is about everything else.
func partsOf(name string) (time.Time, string, bool) {
	v := strings.TrimSuffix(name, Ext)
	if v == name {
		return time.Time{}, "", false
	}

	v, ok := strings.CutPrefix(v, "audit-")
	if !ok {
		return time.Time{}, "", false
	}

	vs := strings.Split(v, ".")

	at, err := time.ParseInLocation("2006-01", vs[0], time.UTC)
	if err != nil {
		return time.Time{}, "", false
	}
	if len(vs) < 3 {
		return at, "", true
	}

	return at, vs[1], true
}
