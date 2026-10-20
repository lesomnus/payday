package audit

import (
	"context"
	"errors"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
)

// Checking the live rows against the trail (payday#102): each row digested
// again and compared with the digest the newest row of the trail about it
// keeps. The generated `Digests` reads the rows a page at a time, kind by kind,
// and hands each page here.

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
}

// Newest answers the newest row of the trail in the database about each of
// `ids`, and nothing for one it has none about.
type Newest func(ctx context.Context, ids []pdid.Id) (map[pdid.Id]Recorded, error)

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

	// NotOnTrail is the rows the trail in the database says nothing about: the
	// rows about them archived, or never written through a server.
	NotOnTrail int

	// KeyNotHeld is the rows whose digest was made under a key the check was
	// not given, so it says nothing either way.
	KeyNotHeld int
}

// Ok is a check that found nothing to look at: every row it could say anything
// about matches.
func (r Rows) Ok() bool { return len(r.Differs) == 0 }

// CheckRows checks one page of live rows and adds what it found to `out`.
func CheckRows(ctx context.Context, d *Digest, rows []Live, newest Newest, out *Rows) error {
	if d == nil {
		return errors.New("no digest key: rows are checked against digests made under one")
	}

	ids := make([]pdid.Id, len(rows))
	for i, v := range rows {
		ids[i] = v.Object
	}

	recorded, err := newest(ctx, ids)
	if err != nil {
		return err
	}

	for _, v := range rows {
		out.Read++

		r, ok := recorded[v.Object]
		switch {
		case !ok:
			out.NotOnTrail++
			continue
		case len(r.Digest) == 0:
			out.NoDigest++
			continue
		}

		verdict, err := d.Check(r.Digest, v.Object, v.Row, r.Fields)
		if err != nil && !errors.Is(err, ErrDigest) {
			return err
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

	return nil
}
