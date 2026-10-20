package audit

import (
	"context"
	"errors"
	"slices"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
)

// Checking the live rows against the trail (payday#102, #108): each row
// digested again and compared with the digest the newest row of the trail
// about it keeps -- in the database, or in the archive once it has left. The
// generated `Digests` hands [CheckKind] each kind's reads, and this is the rest.
//
// # What it is for
//
// Finding the rows the trail cannot account for. A row somebody **changed** in
// the database past the servers is one whose digest differs; a row somebody
// **added** past them is one nothing on the trail is about. So a check that
// answers "not on the trail" for anything else -- a row whose trail rows went
// to the archive, a row a restore put back -- hides the second finding among
// rows that are fine, and the count is the one an operator learns to ignore.
//
// Hence the archive is read for what left the database, and an app that writes
// rows beneath the servers on purpose records them as an act (the generated
// `Record`). What is left of [Rows.NotOnTrail] is a finding, as
// [Rows.Differs] is.

// RowsPage is how many rows a check reads at a time.
const RowsPage = 500

// Live is a row read from its own table, as the servers answer with it.
type Live struct {
	Object pdid.Id
	Row    proto.Message
}

// Recorded is what a check needs of the newest row of the trail about a
// thing: the digest it keeps, and the fields that digest covers.
type Recorded struct {
	Digest []byte
	Fields []uint32

	// Archived is a row of the archive's: the newest row of the trail about
	// the thing had left the database.
	Archived bool
}

// Page is some live rows of one kind, and the newest row of the trail in the
// database about each, read together -- as one snapshot, so that a write
// between the two reads is not a row that differs from its own trail.
type Page struct {
	Rows     []Live
	Recorded map[pdid.Id]Recorded
}

// Kind is what a check reads of one kind of thing. The generated `Digests`
// makes one for each kind the servers write.
type Kind struct {
	Domain pdid.Domain

	// After is the live rows after `after` in identifier order, at most
	// [RowsPage] of them; none at the end. The zero identifier is before
	// every row.
	After func(ctx context.Context, after pdid.Id) (Page, error)

	// Of is the live rows of `ids`, at most [RowsPage] of them: the ones still
	// live, in identifier order.
	Of func(ctx context.Context, ids []pdid.Id) (Page, error)
}

// Archived answers what the newest row of the archive about each of `ids`
// keeps, all of them things of `kind`, and nothing for one it holds nothing
// about. The generated `Digests` makes it from the deployment's
// `trail.Policy`.
type Archived func(ctx context.Context, kind pdid.Domain, ids []pdid.Id) (map[pdid.Id]Recorded, error)

// Rows is what a check of the live rows found.
type Rows struct {
	// Read is the rows checked.
	Read int

	// Matches is the rows that are what the newest write the trail recorded
	// left.
	Matches int

	// Differs is the rows that are not: changed by something the trail did not
	// record, in the order they were read.
	Differs []pdid.Id

	// NoDigest is the rows whose newest row of the trail keeps none: written
	// before digests were, or by a deployment that was given no key.
	NoDigest int

	// NotOnTrail is the rows nothing on the trail is about, in the order they
	// were read: no row of the trail about them in the database, nor in the
	// archive when the check was handed one. A row somebody added past the
	// servers is one of these, and so is one an app wrote beneath them without
	// recording it -- which is the app's to fix (the generated `Record`).
	NotOnTrail []pdid.Id

	// KeyNotHeld is the rows whose digest was made under a key the check was
	// not given, so it says nothing either way.
	KeyNotHeld int

	// Archived is how many of the rows were checked against the archive: the
	// newest row of the trail about them had left the database.
	Archived int
}

// Ok is a check that found nothing to look at: every row it could say anything
// about matches, and every row is on the trail.
func (r Rows) Ok() bool { return len(r.Differs) == 0 && len(r.NotOnTrail) == 0 }

// CheckKind checks every live row of one kind and adds what it found to `out`.
//
// Two passes. The first reads the kind a page at a time with the database's
// trail beside it, and checks every row the trail there is about. The rest --
// a row whose trail rows went to the archive, or one nothing is about -- is
// kept by identifier alone, and the second pass asks `archived` about them,
// once for the kind, and reads them again: with the database's trail first,
// since a row written in between has its newest row there now, and then with
// what the archive said. A row neither is about is [Rows.NotOnTrail].
//
// `archived` nil is a deployment with no archive, and what is not in the
// database is not on the trail.
func CheckKind(ctx context.Context, d *Digest, k Kind, archived Archived, out *Rows) error {
	if d == nil {
		return errors.New("no digest key: rows are checked against digests made under one")
	}

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

		rest, err := check(d, p.Rows, p.Recorded, out)
		if err != nil {
			return err
		}
		for _, v := range rest {
			pending = append(pending, v.Object)
		}

		after = p.Rows[len(p.Rows)-1].Object
	}
	if len(pending) == 0 {
		return nil
	}

	said := map[pdid.Id]Recorded{}
	if archived != nil {
		var err error
		if said, err = archived(ctx, k.Domain, pending); err != nil {
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

	return nil
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
			out.NoDigest++
			continue
		}

		verdict, err := d.Check(r.Digest, v.Object, v.Row, r.Fields)
		if err != nil && !errors.Is(err, ErrDigest) {
			return nil, err
		}

		switch verdict {
		case Matches:
			out.Matches++
		case KeyNotHeld:
			out.KeyNotHeld++
		default:
			out.Differs = append(out.Differs, v.Object)
		}
	}

	return rest, nil
}
