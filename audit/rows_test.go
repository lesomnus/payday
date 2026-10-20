package audit_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
)

// table is one kind of thing as a check reads it: its live rows, and the
// trail about them in the database and in the archive.
type table struct {
	rows     map[pdid.Id]proto.Message
	db       map[pdid.Id]audit.Recorded
	archived map[pdid.Id]audit.Recorded

	// between is what happens to the table after the first pass, before the
	// second reads it again.
	between func(*table)
	read    bool
}

var thing = pdid.Domain(7)

func newTable() *table {
	return &table{
		rows:     map[pdid.Id]proto.Message{},
		db:       map[pdid.Id]audit.Recorded{},
		archived: map[pdid.Id]audit.Recorded{},
	}
}

func (t *table) page(ids []pdid.Id) audit.Page {
	p := audit.Page{Recorded: map[pdid.Id]audit.Recorded{}}
	for _, id := range ids {
		row, ok := t.rows[id]
		if !ok {
			continue
		}

		p.Rows = append(p.Rows, audit.Live{Object: id, Row: row})
		if r, ok := t.db[id]; ok {
			p.Recorded[id] = r
		}
	}

	return p
}

func (t *table) sorted() []pdid.Id {
	ids := []pdid.Id{}
	for id := range t.rows {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b pdid.Id) int { return slices.Compare(a[:], b[:]) })

	return ids
}

func (t *table) kind() audit.Kind {
	return audit.Kind{
		Domain: thing,
		After: func(_ context.Context, after pdid.Id) (audit.Page, error) {
			ids := slices.DeleteFunc(t.sorted(), func(v pdid.Id) bool { return slices.Compare(v[:], after[:]) <= 0 })

			return t.page(ids[:min(len(ids), audit.RowsPage)]), nil
		},
		Of: func(_ context.Context, ids []pdid.Id) (audit.Page, error) {
			if !t.read && t.between != nil {
				t.between(t)
			}
			t.read = true

			return t.page(ids), nil
		},
	}
}

func (t *table) archive(_ context.Context, kind pdid.Domain, ids []pdid.Id) (map[pdid.Id]audit.Recorded, error) {
	out := map[pdid.Id]audit.Recorded{}
	for _, id := range ids {
		if r, ok := t.archived[id]; ok && kind == thing {
			out[id] = r
		}
	}

	return out, nil
}

// add is a live row written through the servers, and the digest its write
// left on the trail.
func (t *table) add(x *require.Assertions, d *audit.Digest, n byte, seconds int64) (pdid.Id, audit.Recorded) {
	id := object
	id[15] = n

	t.rows[id] = row(seconds, 0)
	sum, err := d.Of(id, t.rows[id], []uint32{1, 2})
	x.NoError(err)

	return id, audit.Recorded{Digest: sum, Fields: []uint32{1, 2}}
}

func TestEveryRowIsAccountedForInTheDatabaseOrTheArchive(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	inDb, r := tb.add(x, d, 1, 10)
	tb.db[inDb] = r
	inArchive, r := tb.add(x, d, 2, 20)
	tb.archived[inArchive] = r
	nowhere, _ := tb.add(x, d, 3, 30)

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))

	x.Equal(3, out.Read)
	x.Equal(2, out.Matches)
	x.Equal(1, out.Archived)
	x.Empty(out.Differs)
	x.Equal([]pdid.Id{nowhere}, out.NotOnTrail, "a row nothing is about is the finding")
	x.False(out.Ok())
}

func TestWithNoArchiveWhatIsNotInTheDatabaseIsNotOnTheTrail(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	archived, r := tb.add(x, d, 1, 10)
	tb.archived[archived] = r

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), nil, &out))
	x.Equal([]pdid.Id{archived}, out.NotOnTrail)
	x.Zero(out.Archived)
}

// TestARowWrittenBetweenThePassesIsCheckedAgainstItsNewWrite: the second pass
// reads the database's trail again before the archive's, since a row written in
// between has its newest row there now -- and the archive's older digest would
// be a difference nobody made.
func TestARowWrittenBetweenThePassesIsCheckedAgainstItsNewWrite(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	id, r := tb.add(x, d, 1, 10)
	tb.archived[id] = r
	tb.between = func(tb *table) {
		tb.rows[id] = row(11, 0)
		sum, err := d.Of(id, tb.rows[id], []uint32{1, 2})
		x.NoError(err)
		tb.db[id] = audit.Recorded{Digest: sum, Fields: []uint32{1, 2}}
	}

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))
	x.True(out.Ok(), "%+v", out)
	x.Equal(1, out.Matches)
	x.Zero(out.Archived)
}

// TestARowErasedBetweenThePassesIsNotCounted: it is not a live row any more,
// and a check says nothing about it.
func TestARowErasedBetweenThePassesIsNotCounted(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	id, _ := tb.add(x, d, 1, 10)
	tb.between = func(tb *table) { delete(tb.rows, id) }

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))
	x.Zero(out.Read)
	x.True(out.Ok())
}

// TestARowTheArchiveSaysOtherwiseOfDiffers: a row changed past the servers after
// its trail rows left the database is found as one changed while they were
// there is.
func TestARowTheArchiveSaysOtherwiseOfDiffers(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	id, r := tb.add(x, d, 1, 10)
	tb.archived[id] = r
	tb.rows[id] = row(12, 0)

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))
	x.Equal([]pdid.Id{id}, out.Differs)
	x.Equal(1, out.Archived)
}

// TestManyRowsAreReadAPageAtATime: past a page, in both passes.
func TestManyRowsAreReadAPageAtATime(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	n := audit.RowsPage + 7
	for i := range n {
		id := object
		id[14], id[15] = byte(i>>8), byte(i)
		tb.rows[id] = row(int64(i), 0)
		sum, err := d.Of(id, tb.rows[id], []uint32{1, 2})
		x.NoError(err)

		r := audit.Recorded{Digest: sum, Fields: []uint32{1, 2}}
		if i%2 == 0 {
			tb.db[id] = r
		} else {
			tb.archived[id] = r
		}
	}

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))
	x.Equal(n, out.Read)
	x.Equal(n, out.Matches)
	x.Equal(n/2, out.Archived)
	x.True(out.Ok())
}
