package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
)

// Checking the live rows against the trail (payday#102, #108, #115): each row
// digested again and compared with the digest the newest line of the trail
// about it keeps -- in the database, or in the archive once it has left. The
// generated `Digests` hands [CheckKind] each kind's reads, and this is the rest.
//
// # What it is for
//
// Finding what the trail cannot account for. A row somebody **changed** in the
// database past the servers is one whose digest differs; a row somebody
// **added** past them is one nothing on the trail is about; a row somebody
// **removed** past them is a thing the trail says is there and is not. So a
// check that answers "not on the trail" for anything else -- a row whose trail
// rows went to the archive, a row a restore put back -- hides the second
// finding among rows that are fine, and the count is the one an operator
// learns to ignore.
//
// Hence the archive is read for what left the database, and an app that writes
// rows beneath the servers on purpose records them as an act (the generated
// `Record`, and `RecordErased` for the ones it removed). What is left of
// [Rows.NotOnTrail] is a finding, as [Rows.Differs] is.
//
// # And what no answer may be
//
// A line with no digest, and a digest under a key the check was not given, say
// nothing about the row -- and both are what whoever can write the database
// can make of any line in a statement, to make the row beside it say nothing.
// So neither is a count beside the findings any more: each is a finding,
// except where the deployment said why it is not, in configuration it keeps
// beside the key ([Digest.Since], [Digest.Retire]).

// RowsPage is how many rows a check reads at a time.
const RowsPage = 500

// Live is a row read from its own table, as the servers answer with it: a row
// there is, live or erased softly -- an erased row is still a row the trail
// accounts for, and one somebody brings back past the servers is a row that
// differs from the erase.
type Live struct {
	Object pdid.Id
	Row    proto.Message
}

// Recorded is what a check needs of the newest line of the trail about a
// thing: the digest it keeps, the fields that digest covers, and the line
// itself, which the digest is bound to.
type Recorded struct {
	Digest []byte
	Fields []uint32
	Line   Line

	// Archived is a line of the archive's: the newest line of the trail about
	// the thing had left the database.
	Archived bool
}

// Page is some rows of one kind, and the newest line of the trail in the
// database about each, read together -- as one snapshot, so that a write
// between the two reads is not a row that differs from its own trail.
type Page struct {
	Rows     []Live
	Recorded map[pdid.Id]Recorded
}

// Kind is what a check reads of one kind of thing. The generated `Digests`
// makes one for each kind on the trail.
type Kind struct {
	Domain pdid.Domain

	// After is the rows after `after` in identifier order, at most
	// [RowsPage] of them; none at the end. The zero identifier is before
	// every row.
	After func(ctx context.Context, after pdid.Id) (Page, error)

	// Of is the rows of `ids`, at most [RowsPage] of them: the ones still
	// there, in identifier order.
	Of func(ctx context.Context, ids []pdid.Id) (Page, error)

	// Trail hands `yield` the things of this kind the database's trail is
	// about, a page at a time. A thing may be handed more than once.
	Trail func(ctx context.Context, yield func([]pdid.Id) error) error

	// Gone is, of `ids` -- at most [RowsPage] of them -- the ones that are no
	// row: each with the newest line of the trail in the database about it,
	// and nil for one the database's trail says nothing about. Read at one
	// moment, so that a row added between the two reads is not a row gone.
	Gone func(ctx context.Context, ids []pdid.Id) (map[pdid.Id]*Recorded, error)
}

// Archived answers the newest line of the archive about every thing of `kind`
// it holds a line about. The generated `Digests` makes it from the
// deployment's `trail.Policy`.
type Archived func(ctx context.Context, kind pdid.Domain) (map[pdid.Id]Recorded, error)

// Skipped is a kind a check does not read, and why: the schema keeps its
// writes off the trail (`trail: {off: {why: ...}}`).
type Skipped struct {
	Kind string
	Why  string
}

// Rows is what a check of the rows found.
//
// The lists are the findings, in the order they were read, and the counts are
// what was looked at and is fine -- or, for [Rows.BeforeDigests] and
// [Rows.Retired], what the deployment said nothing can be said of.
type Rows struct {
	// Read is the rows checked.
	Read int

	// Matches is the rows that are what the newest write the trail recorded
	// left.
	Matches int

	// Differs is the rows that are not: changed by something the trail did not
	// record -- brought back from an erase, among them.
	Differs []pdid.Id

	// Forged is the rows whose newest line of the trail keeps a digest that is
	// not that line's: copied onto it from another line, or not one payday
	// makes. The line was changed, rather than the row or as well.
	Forged []pdid.Id

	// NoDigest is the rows whose newest line keeps no digest, and was written
	// after digests began ([Digest.Since]): a deployment that wrote it with no
	// key, or a digest somebody took off.
	NoDigest []pdid.Id

	// BeforeDigests is how many rows' newest line keeps no digest and was
	// written before digests began. Nothing can be said of them, and the
	// deployment said so; they are counted rather than found.
	BeforeDigests int

	// NotOnTrail is the rows nothing on the trail is about, in the order they
	// were read: no line about them in the database, nor in the archive when
	// the check was handed one. A row somebody added past the servers is one
	// of these, and so is one an app wrote beneath them without recording it
	// -- which is the app's to fix (the generated `Record`).
	NotOnTrail []pdid.Id

	// Missing is the things the trail says are rows, and that are no row: the
	// newest line about each says the row its write left, or says nothing,
	// and the row is not there. A row somebody removed past the servers is one
	// of these, and so is one an app removed without recording it (the
	// generated `RecordErased`).
	Missing []pdid.Id

	// KeyNotHeld is the rows whose digest was made under a key the check was
	// not given, and was not told was let go: a check handed the wrong key, or
	// a digest nobody here made.
	KeyNotHeld []pdid.Id

	// Unheld is the keys those were made under, each once, for an operator to
	// give the check or to let go.
	Unheld []Fingerprint

	// Retired is how many rows' digests were made under a key the deployment
	// let go ([Digest.Retire]).
	Retired int

	// Archived is how many of the rows were checked against the archive: the
	// newest line of the trail about them had left the database.
	Archived int

	// Skipped is the kinds the check did not read, each with why, so that a
	// check that found nothing says what it did not look at.
	Skipped []Skipped
}

// Ok is a check that found nothing to look at: every row it read matches or
// is one the deployment said nothing can be said of, every row is on the
// trail, and everything the trail says is a row is one.
func (r Rows) Ok() bool {
	return len(r.Differs) == 0 && len(r.Forged) == 0 && len(r.NoDigest) == 0 &&
		len(r.NotOnTrail) == 0 && len(r.Missing) == 0 && len(r.KeyNotHeld) == 0
}

// Digest is the findings as one digest: each a line of which finding it is and
// the row, in an order of their own. An acceptance of a check -- an app's
// `trail rows --accept`, which puts the rows on the trail with `Record` -- names
// it, and is refused when a check now finds anything else (payday#115): what
// is accepted is what somebody looked at, and no row found after they did. The
// counts are not in it, nor the order the rows were read in.
func (r Rows) Digest() string {
	b := &bytes.Buffer{}
	for _, l := range []struct {
		name string
		ids  []pdid.Id
	}{
		{"differs", r.Differs},
		{"forged", r.Forged},
		{"no-digest", r.NoDigest},
		{"not-on-trail", r.NotOnTrail},
		{"missing", r.Missing},
		{"key-not-held", r.KeyNotHeld},
	} {
		vs := slices.Clone(l.ids)
		slices.SortFunc(vs, func(a, b pdid.Id) int { return bytes.Compare(a[:], b[:]) })
		for _, v := range slices.Compact(vs) {
			fmt.Fprintf(b, "%s %s\n", l.name, v)
		}
	}

	sum := sha256.Sum256(b.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CheckKind checks every row of one kind and adds what it found to `out`.
//
// Three passes. The first reads the kind a page at a time with the database's
// trail beside it, and checks every row the trail there is about. The rest --
// a row whose trail went to the archive, or one nothing is about -- is kept by
// identifier alone, and the second reads them again, with the database's trail
// first, since a row written in between has its newest line there now, and
// then with what the archive said. A row neither is about is
// [Rows.NotOnTrail]. The third goes the other way: every thing the trail, in
// the database or the archive, is about that the first pass did not read, and
// the ones that are no row are checked against what their newest line says.
//
// `archived` nil is a deployment with no archive, and what is not in the
// database is not on the trail.
func CheckKind(ctx context.Context, d *Digest, k Kind, archived Archived, out *Rows) error {
	if d == nil {
		return errors.New("no digest key: rows are checked against digests made under one")
	}

	seen := map[pdid.Id]bool{}
	pending := []pdid.Id{}
	var after pdid.Id
	for {
		p, err := k.After(ctx, after)
		if err != nil {
			return err
		}
		if len(p.Rows) == 0 {
			break
		}

		for _, v := range p.Rows {
			seen[v.Object] = true
		}

		rest, err := check(d, p.Rows, p.Recorded, out)
		if err != nil {
			return err
		}
		for _, v := range rest {
			pending = append(pending, v.Object)
		}

		after = p.Rows[len(p.Rows)-1].Object
	}

	said := map[pdid.Id]Recorded{}
	if archived != nil {
		var err error
		if said, err = archived(ctx, k.Domain); err != nil {
			return err
		}
	}

	for ids := range slices.Chunk(pending, RowsPage) {
		p, err := k.Of(ctx, ids)
		if err != nil {
			return err
		}

		recorded := p.Recorded
		if recorded == nil {
			recorded = map[pdid.Id]Recorded{}
		}
		for _, v := range p.Rows {
			if _, ok := recorded[v.Object]; ok {
				continue
			}
			if r, ok := said[v.Object]; ok {
				r.Archived = true
				recorded[v.Object] = r
			}
		}

		rest, err := check(d, p.Rows, recorded, out)
		if err != nil {
			return err
		}
		for _, v := range rest {
			out.Read++
			out.NotOnTrail = append(out.NotOnTrail, v.Object)
		}
	}

	return gone(ctx, d, k, seen, said, out)
}

// check checks the rows `recorded` says something about and adds what it found
// to `out`, and answers with the rest, unread.
func check(d *Digest, rows []Live, recorded map[pdid.Id]Recorded, out *Rows) ([]Live, error) {
	rest := []Live{}
	for _, v := range rows {
		r, ok := recorded[v.Object]
		if !ok {
			rest = append(rest, v)
			continue
		}

		out.Read++
		if r.Archived {
			out.Archived++
		}
		if len(r.Digest) == 0 {
			if d.before(r) {
				out.BeforeDigests++
			} else {
				out.NoDigest = append(out.NoDigest, v.Object)
			}
			continue
		}

		verdict, err := d.Check(r.Digest, v.Object, v.Row, r.Fields, r.Line)
		if err != nil && !errors.Is(err, ErrDigest) {
			return nil, err
		}
		if err := d.began(v.Object, r, verdict); err != nil {
			return nil, err
		}

		switch verdict {
		case Matches:
			out.Matches++
		case Retired:
			out.Retired++
		case KeyNotHeld:
			out.unheld(d, v.Object, r.Digest)
		case Forged:
			out.Forged = append(out.Forged, v.Object)
		default:
			out.Differs = append(out.Differs, v.Object)
		}
	}

	return rest, nil
}

// gone is the third pass of [CheckKind]: what the trail is about and the first
// pass did not read, checked against what its newest line says when it is no
// row.
func gone(ctx context.Context, d *Digest, k Kind, seen map[pdid.Id]bool, said map[pdid.Id]Recorded, out *Rows) error {
	if k.Gone == nil {
		return nil
	}

	queued := map[pdid.Id]bool{}
	ids := []pdid.Id{}
	add := func(vs []pdid.Id) {
		for _, v := range vs {
			if seen[v] || queued[v] {
				continue
			}

			queued[v] = true
			ids = append(ids, v)
		}
	}

	if k.Trail != nil {
		if err := k.Trail(ctx, func(vs []pdid.Id) error { add(vs); return nil }); err != nil {
			return err
		}
	}
	add(slices.SortedFunc(maps.Keys(said), func(a, b pdid.Id) int { return bytes.Compare(a[:], b[:]) }))

	for page := range slices.Chunk(ids, RowsPage) {
		vs, err := k.Gone(ctx, page)
		if err != nil {
			return err
		}

		for _, id := range page {
			r, ok := vs[id]
			if !ok {
				// A row after all: written since the first pass read past it.
				continue
			}
			if r == nil {
				a, ok := said[id]
				if !ok {
					continue
				}
				a.Archived = true
				r = &a
			}

			if err := judgeGone(d, id, *r, out); err != nil {
				return err
			}
		}
	}

	return nil
}

// judgeGone is what a check finds of a thing that is no row, from the newest
// line about it.
func judgeGone(d *Digest, id pdid.Id, r Recorded, out *Rows) error {
	if len(r.Digest) == 0 {
		// Nothing says the write took it, and nothing can be said of a line
		// from before digests: an erase made by a version of payday that kept
		// no digest of one is among these, and is looked at once.
		if !d.before(r) {
			out.Missing = append(out.Missing, id)
		}
		return nil
	}

	verdict, err := d.CheckGone(r.Digest, id, r.Line)
	if err != nil && !errors.Is(err, ErrDigest) {
		return err
	}
	if err := d.began(id, r, verdict); err != nil {
		return err
	}

	switch verdict {
	case Matches:
	case Retired:
		out.Retired++
	case KeyNotHeld:
		out.unheld(d, id, r.Digest)
	case Forged:
		out.Forged = append(out.Forged, id)
	default:
		out.Missing = append(out.Missing, id)
	}

	return nil
}

// before is whether `r` was written before this deployment began keeping
// digests, which is never for one that says nothing of when.
func (d *Digest) before(r Recorded) bool {
	return !d.since.IsZero() && r.Line.Created.Before(d.since)
}

// began refuses a check told digests began after a line that keeps one this
// deployment made: [Digest.Since] is later than it is, and every line without
// a digest between the two would be counted as from before by a check that was
// misconfigured into it.
//
// Only of a digest whose key is held: one under any other key may not be this
// deployment's at all, and it is a finding of its own.
func (d *Digest) began(id pdid.Id, r Recorded, v Verdict) error {
	if v != Matches && v != Differs {
		return nil
	}
	if !d.before(r) {
		return nil
	}

	return fmt.Errorf("audit.digest.since is %s, and the trail about %s %s has a line written at %s that keeps a digest: digests began no later than that",
		d.since.UTC().Format(time.RFC3339), r.Line.Action, id, r.Line.Created.UTC().Format(time.RFC3339Nano))
}

// unheld files a digest under a key the check was not given.
func (o *Rows) unheld(d *Digest, id pdid.Id, sum []byte) {
	o.KeyNotHeld = append(o.KeyNotHeld, id)
	for _, p := range prints(sum) {
		if _, v := d.keyOf(p); v == KeyNotHeld && !slices.Contains(o.Unheld, p) {
			o.Unheld = append(o.Unheld, p)
		}
	}
}

// prints is the fingerprints a digest names: the line's and the row's, or the
// row's alone for one of version 1.
func prints(sum []byte) []Fingerprint {
	switch {
	case len(sum) == onlyRow && sum[0] == 1:
		return []Fingerprint{Fingerprint(sum[1:9])}
	case len(sum) == onlyLine && sum[0] == DigestVersion:
		return []Fingerprint{Fingerprint(sum[1:9])}
	case len(sum) == lineAndRow && sum[0] == DigestVersion:
		return []Fingerprint{Fingerprint(sum[1:9]), Fingerprint(sum[onlyLine : onlyLine+8])}
	}

	return nil
}
