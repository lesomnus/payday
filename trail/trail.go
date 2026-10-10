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
// # Whose window it is
//
// The deployment's policy is per kind of thing, and a deployment whose
// customers keep their history for different lengths of time -- a plan, a
// contract, a regulated customer -- answers per tenant as well, through
// [Policy.Tenants]. payday stores no plans: what a tenant keeps follows from
// its contract, which is the app's data. What payday adds is the arithmetic --
// the deployment's own floors ([Policy.Min]), and the rule for a row that more
// than one tenant may read, which is that it lasts as long as the longest of
// them keeps it.
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
// everything with a decision in it is here: the two clocks, whose clock a row
// is on, the refusal to destroy what was never written, the archive's layout,
// the order of the write and the delete, and what may be destroyed.
//
// It also means this package names no `Audit` Go type, and could not: payday's
// copy of the schema is generated **into each app**, so `payday.Audit` has no
// Go type upstream at all. What travels between the two halves is the
// protojson document, which is the archive's format anyway.
//
// # The archive
//
// A [flob.Stores], with a namespace per tenant -- see [Chunk] for what is in
// one, and [SharedNamespace] for the rows that are not one tenant's. A
// deployment that names a directory in `audit.archive` gets one on the disk;
// anything else flob reaches, S3 included, is a store the app hands in.
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
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"uuid"

	"github.com/lesomnus/flob"

	"github.com/lesomnus/payday/pdid"
)

// Ext is what an archive file was called, after the month it held, when the
// archive was a directory of files; see [Adopt]. A chunk read back out of the
// archive and written to a disk is the same format.
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
	// which of a policy's windows applies to it. See [Policy].
	Domain pdid.Domain

	// Created is when it was written, which is what decides which month it is
	// filed under in the archive.
	Created time.Time

	// Tenants is every tenant the row names, and so every tenant that may read
	// it. See [TenantsOf].
	Tenants []pdid.Id
}

// TenantsOf is the tenants a row names, from its three columns: the one it is
// filed under, the actor's, and the other party's.
//
// Each once, the one it is filed under first, and none of them zero. Zero is
// how the trail spells *nobody* -- an actor with no frame is the deployment
// acting on itself -- and nobody is not a tenant whose window applies.
//
// Here rather than in the generated store so that the rule is written once:
// whose window a row is on is a judgement, and this is it.
func TenantsOf(tenant uuid.UUID, actor uuid.UUID, counterpart *uuid.UUID) []pdid.Id {
	out := make([]pdid.Id, 0, 3)
	add := func(v uuid.UUID) {
		id := pdid.Id(v)
		if id.IsZero() || slices.Contains(out, id) {
			return
		}

		out = append(out, id)
	}

	add(tenant)
	add(actor)
	if counterpart != nil {
		add(*counterpart)
	}

	return out
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

// CutFor is a cutoff for these kinds, asked of the archive by the kind a chunk
// carries: *destroy this pass's kinds, as far back as this*. It is what an
// operator at a shell means by `--kind` beside `--older-than`.
//
// A chunk that says nothing about its kind -- which is what the first version
// of the format wrote -- belongs to whichever pass is about everything **else**,
// since that is where a row of an unknown kind would have gone.
func (k Kinds) CutFor(before time.Time) func(kind string) (time.Time, bool) {
	return func(kind string) (time.Time, bool) {
		d, ok := domainOf(kind)
		if !ok {
			return before, len(k.Only) == 0
		}
		if !k.Has(d) {
			return time.Time{}, false
		}

		return before, true
	}
}

// Before is the cutoff that is the same for every kind, which is what an
// operator at a shell means by `--older-than` alone.
func Before(at time.Time) func(string) (time.Time, bool) {
	return func(string) (time.Time, bool) { return at, true }
}

// Whose is how the rows of a [Scope] belong to its tenant.
type Whose int

const (
	// Anyone does not narrow by tenant: every row of the scope's kinds.
	Anyone Whose = iota

	// FiledUnder is the rows filed under the tenant -- `tenant_id` -- whoever
	// else they name. Every row is filed under exactly one tenant, so a pass
	// that goes tenant by tenant this way reaches each row once.
	FiledUnder

	// Alone is the rows filed under the tenant that name no other: the
	// ordinary write, which is nearly all of them.
	Alone

	// Together is the rows that name the tenant and some other tenant as well,
	// in whichever columns.
	Together

	// Sharing is the rows filed under the tenant that name another tenant as
	// well: the ones it shares, from its own side. A tenant that leaves takes
	// these with it in part -- what they said goes, that they happened stays.
	Sharing
)

// Scope is which rows a read is about: of which kinds, and whose.
type Scope struct {
	Kinds

	// Whose narrows to the rows of [Scope.Tenant], and says how they are its.
	// The zero value, [Anyone], does not narrow.
	Whose Whose

	// Tenant is whose rows these are, when Whose says so. Zero is the rows
	// filed under nobody.
	Tenant pdid.Id

	// Objects, when set, narrows to the rows about these: what erasing a
	// subject reads.
	Objects []pdid.Id
}

// forever is the cutoff of an act that is about every row whatever its age:
// a subject's erasure, a tenant leaving.
//
// The largest instant there is in nanoseconds, in 2262, and not a round year
// after it: SQLite's driver stores a time as nanoseconds in an int64, and the
// year 9999 this was first written as overflowed into a cutoff no row is
// older than. The tests on SQLite are what said so.
var forever = time.Unix(0, math.MaxInt64).UTC()

// Cursor is where a read of [Store.Older] carries on from: past the row written
// at this instant with this key, in the order the store answers in.
//
// A pass leaves some rows where they are -- a row that names a tenant keeping
// it longer -- and asking again for *the oldest thousand* would answer with the
// same ones for ever. The zero value is the start.
type Cursor struct {
	Created time.Time
	Key     any
}

// Store is the app's half: the audit table, as much of it as this needs.
//
// Generated rather than written, for `internal/pdgen/outbox.go`'s reason -- the
// ent client and its predicates are the app's types. What is asked of it has no
// judgement in it: read a batch of rows older than an instant, count them, say
// which tenants rows are filed under, forget or blank the ones that were
// named.
//
// It is deliberately a **bulk** interface, which `auth/authsession.Store` is
// deliberately not. That one has `Put`, `Get` and `Del` and no pass over
// everything, because a store over a hundred million rows should not be walked
// by whoever happens to be signing in. This is the opposite job: nothing here
// is on a request path, and a pass over everything is the whole of it.
type Store interface {
	// Tenants answers up to `limit` of the tenants rows are filed under, in
	// identifier order, starting after `after`.
	//
	// One seek per tenant rather than a `DISTINCT` over the table: the one is
	// as many index lookups as there are tenants, and the other is a scan of
	// the table that never stops growing.
	Tenants(ctx context.Context, after pdid.Id, limit int) ([]pdid.Id, error)

	// Older answers up to `limit` rows of this scope written before `at`,
	// oldest first, starting past `after`.
	Older(ctx context.Context, of Scope, at time.Time, after Cursor, limit int) (Rows, error)

	// Heads is [Store.Older] without the documents: everything a decision
	// about a row reads, for an act that keeps no copy of it. A tenant that
	// leaves is millions of rows nobody needs marshalled.
	Heads(ctx context.Context, of Scope, at time.Time, after Cursor, limit int) (Rows, error)

	// Count is how many of this scope are past the cutoff, for a dry run.
	Count(ctx context.Context, of Scope, at time.Time) (int, error)

	// Forget removes exactly the rows these keys name, and answers how many
	// went. A key it does not find is not an error: another writer reached it
	// first, which is a thing that happens rather than a thing to fail over.
	Forget(ctx context.Context, keys []any) (int, error)

	// Blank empties `value` and `patch` of exactly the rows these keys name,
	// and answers how many it reached. What the row says happened stays.
	Blank(ctx context.Context, keys []any) (int, error)
}

// Batch is how many rows one pass reads and removes at a time.
//
// A first run on a deployment that has never pruned is the whole table, and one
// statement over it is a transaction holding locks for as long as it takes and
// a delete that either finishes or achieves nothing. Batched, an interrupted
// run has still moved everything it wrote.
const Batch = 1000

// Archive writes every row older than `before` into the archive, then removes
// exactly the rows it wrote. It answers with how many moved.
//
// # The order, and why it is not a flag
//
// Written, and the store has answered that it holds it -- and only then
// deleted, by the keys of the rows that are actually in the chunk. Not by
// asking for "everything older than `before`" a second time: a second query
// matches whatever is true when it runs rather than what was written, so a row
// backdated by a clock that stepped or written by a replica whose idea of now
// is behind is a row the second query removes and the archive does not have.
//
// The failure that is left is a crash between the write and the delete, and it
// leaves the rows in **both** places. That is the direction to fail in, and
// [Read] drops the duplicate.
//
// # Every tenant's rows go to that tenant
//
// Whatever the policy, a row is filed under the tenants it names -- see
// [Chunk] -- so an archive written by hand is laid out like one the sweep
// wrote, and a tenant's window can be applied to it later.
//
// # A nil archive is refused
//
// Deleting without keeping is a thing a deployment may genuinely want, and it
// is not a thing to arrive at by leaving a field blank. See [Policy.Collect],
// which is what that deployment calls.
func Archive(ctx context.Context, s Store, of Kinds, before time.Time, a flob.Stores) (int, error) {
	if a == nil {
		return 0, errors.New("no archive to write into")
	}

	run, err := newRun()
	if err != nil {
		return 0, err
	}

	moved, _, err := drain(ctx, s, Scope{Kinds: of}, before, a, run, func(Row) fate { return archived })

	return moved, err
}

// fate is what one pass does with one row it read.
type fate int

const (
	// stays in the database: a row that also names a tenant keeping it
	// longer, or one whose tenant has no answer this pass.
	stays fate = iota

	// archived leaves the database for the archive.
	archived

	// discarded leaves it with no copy kept.
	discarded
)

// drain is the loop every move out of the database is, and answers how many
// rows it archived and how many it discarded.
//
// Rows to archive are gathered and written a chunk at a time -- see
// [chunkRows] -- and forgotten only once the store holds the chunk, which is
// the order [Archive] is about. Rows to discard are forgotten a batch at a
// time, since there is nothing to wait for. With no archive there is nothing
// to write, and the rows are read without their documents.
func drain(ctx context.Context, s Store, of Scope, before time.Time, a flob.Stores, run string, decide func(Row) fate) (int, int, error) {
	var w *writer
	read := s.Heads
	if a != nil {
		w = newWriter(a, run)
		read = s.Older
	}

	moved, gone := 0, 0
	at := Cursor{}
	for {
		vs, err := read(ctx, of, before, at, Batch)
		if err != nil {
			return moved, gone, err
		}

		drop := []any{}
		for _, v := range vs {
			at = Cursor{Created: v.Created, Key: v.Key}

			switch decide(v) {
			case archived:
				if w == nil {
					return moved, gone, errors.New("a row is due for the archive and there is no archive")
				}

				w.add(v)

			case discarded:
				drop = append(drop, v.Key)
			}
		}

		n, err := forget(ctx, s, drop)
		gone += n
		if err != nil {
			return moved, gone, err
		}

		last := len(vs) < Batch
		if w != nil && (last || w.full()) {
			keys, err := w.flush(ctx)
			if err != nil {
				return moved, gone, err
			}

			n, err := forget(ctx, s, keys)
			moved += n
			if err != nil {
				return moved, gone, err
			}
		}
		if last {
			return moved, gone, nil
		}
	}
}

// forget is [Store.Forget] a batch at a time, because a flushed chunk can name
// more rows than one statement should.
func forget(ctx context.Context, s Store, keys []any) (int, error) {
	return batched(ctx, s.Forget, keys)
}

// blank is [Store.Blank] a batch at a time.
func blank(ctx context.Context, s Store, keys []any) (int, error) {
	return batched(ctx, s.Blank, keys)
}

func batched(ctx context.Context, fn func(context.Context, []any) (int, error), keys []any) (int, error) {
	n := 0
	for len(keys) > 0 {
		k := min(len(keys), Batch)

		m, err := fn(ctx, keys[:k])
		n += m
		if err != nil {
			return n, err
		}

		keys = keys[k:]
	}

	return n, nil
}

// Month is the part of an archive's name that says what is in it.
//
// UTC, so that a deployment does not file the same instant in two months
// depending on where the machine thinks it is.
func Month(at time.Time) string { return at.UTC().Format("2006-01") }

// nameOf is the kind, as a name a person writes in a configuration file.
//
// The schema's own, through `pdid`, so `holder` and `robot` rather than 2 and
// 17. A domain nothing registered -- which includes zero, the one number no
// entity may hold -- is written as its number, because a chunk has to say
// something and a number is at least true.
func nameOf(d pdid.Domain) string {
	if v, ok := pdid.Domains()[d]; ok && v != "" {
		return v
	}

	return fmt.Sprintf("d%d", uint8(d))
}

// domainOf reads a name back, from a configuration file or from an archive's
// own labels.
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

// ReadFiles walks archives on a disk in the order given and calls `fn` for
// each row, as the protojson document it was stored as.
//
// For a file somebody took out of an archive -- a chunk saved for a request, or
// the directory of a version before the archive was a flob store. What is in
// the archive itself is [Read].
//
// Duplicates are dropped by identifier, **across** the files rather than within
// one, for the reason [Read] gives.
func ReadFiles(paths []string, fn func(doc []byte) error) error {
	seen := map[string]bool{}

	for _, path := range paths {
		if err := readFile(path, seen, fn); err != nil {
			return err
		}
	}

	return nil
}

func readFile(path string, seen map[string]bool, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	err = lines(f, func(doc []byte) error {
		k, err := identifier(doc)
		if err != nil {
			return err
		}
		if seen[k] {
			return nil
		}
		seen[k] = true

		return fn(doc)
	})
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	return nil
}

// head is the fields this package reads out of a document it cannot otherwise
// interpret.
//
// protojson writes `bytes` as base64, which is a JSON string, so ordinary JSON
// reaches it without a descriptor. That is one of the reasons the archive is
// protojson and not the wire format: the half of payday that owns the archive
// can still tell whether it has seen a row, and whose it is, without a Go type
// it does not have. The names are payday's own columns, so there is nothing
// here an app can move.
type head struct {
	Id          string `json:"id"`
	Tenant      string `json:"tenantId"`
	Actor       string `json:"actorTenantId"`
	Counterpart string `json:"counterpartTenantId"`
	Object      string `json:"objectId"`
	Created     string `json:"dateCreated"`
	Domain      uint32 `json:"domain"`
	Value       string `json:"value"`
	Patch       string `json:"patch"`
}

func headOf(doc []byte) (head, error) {
	var v head
	if err := json.Unmarshal(doc, &v); err != nil {
		return head{}, err
	}
	if v.Id == "" {
		return head{}, errors.New("a row in the archive has no identifier")
	}

	return v, nil
}

func identifier(doc []byte) (string, error) {
	v, err := headOf(doc)
	if err != nil {
		return "", err
	}

	return v.Id, nil
}

// names reports whether a document names this tenant, in any of its three
// columns, which is the wall's own rule.
func (h head) names(tenant pdid.Id) bool {
	v := encoded(tenant)

	return h.Tenant == v || h.Actor == v || h.Counterpart == v
}

// tenants is what a document names, as [TenantsOf] reads the columns.
func (h head) tenants() []pdid.Id {
	id := func(v string) uuid.UUID {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(b) != 16 {
			return uuid.Nil()
		}

		return uuid.UUID(b)
	}

	var counterpart *uuid.UUID
	if h.Counterpart != "" {
		v := id(h.Counterpart)
		counterpart = &v
	}

	return TenantsOf(id(h.Tenant), id(h.Actor), counterpart)
}

// filed answers whether a document is filed under this tenant.
func (h head) filed(tenant pdid.Id) bool { return h.Tenant == encoded(tenant) }

// contents answers whether a document still says something about what it
// happened to.
func (h head) contents() bool { return h.Value != "" || h.Patch != "" }
