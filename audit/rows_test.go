package audit_test

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
)

// table is one kind of thing as a check reads it: its rows, and the trail
// about things of it in the database and in the archive.
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

func sorted(ids []pdid.Id) []pdid.Id {
	slices.SortFunc(ids, func(a, b pdid.Id) int { return bytes.Compare(a[:], b[:]) })
	return ids
}

func (t *table) kind() audit.Kind {
	return audit.Kind{
		Domain: thing,
		After: func(_ context.Context, after pdid.Id) (audit.Page, error) {
			ids := slices.DeleteFunc(sorted(slices.Collect(maps.Keys(t.rows))), func(v pdid.Id) bool {
				return bytes.Compare(v[:], after[:]) <= 0
			})

			return t.page(ids[:min(len(ids), audit.RowsPage)]), nil
		},
		Of: func(_ context.Context, ids []pdid.Id) (audit.Page, error) {
			if !t.read && t.between != nil {
				t.between(t)
			}
			t.read = true

			return t.page(ids), nil
		},
		Trail: func(_ context.Context, yield func([]pdid.Id) error) error {
			return yield(sorted(slices.Collect(maps.Keys(t.db))))
		},
		Gone: func(_ context.Context, ids []pdid.Id) (map[pdid.Id]*audit.Recorded, error) {
			out := map[pdid.Id]*audit.Recorded{}
			for _, id := range ids {
				if _, ok := t.rows[id]; ok {
					continue
				}
				if r, ok := t.db[id]; ok {
					out[id] = &r
				} else {
					out[id] = nil
				}
			}

			return out, nil
		},
	}
}

func (t *table) archive(_ context.Context, kind pdid.Domain) (map[pdid.Id]audit.Recorded, error) {
	if kind != thing {
		return nil, nil
	}

	return maps.Clone(t.archived), nil
}

// lines is how many lines a table has had written, so each has its own key.
var lines byte

// written is the line a write `ago` before [at] is recorded as.
func written(ago time.Duration) audit.Line {
	lines++
	return line(lines, ago)
}

// add is a row written through the servers, and the digest its write left on
// the trail.
func (t *table) add(x *require.Assertions, d *audit.Digest, n byte, seconds int64) (pdid.Id, audit.Recorded) {
	id := object
	id[15] = n

	t.rows[id] = row(seconds, 0)

	return id, recorded(x, d, id, t.rows[id], 0)
}

// recorded is the line a write of `v` left, `ago` before [at].
func recorded(x *require.Assertions, d *audit.Digest, id pdid.Id, v proto.Message, ago time.Duration) audit.Recorded {
	l := written(ago)
	sum, err := d.Of(id, v, []uint32{1, 2}, l)
	x.NoError(err)

	return audit.Recorded{Digest: sum, Fields: []uint32{1, 2}, Line: l}
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
		tb.db[id] = recorded(x, d, id, tb.rows[id], 0)
	}

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))
	x.True(out.Ok(), "%+v", out)
	x.Equal(1, out.Matches)
	x.Zero(out.Archived)
}

// TestARowErasedBetweenThePassesIsNotCounted: the servers erased it, which
// left a line saying so, and a check says nothing about it.
func TestARowErasedBetweenThePassesIsNotCounted(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	id, _ := tb.add(x, d, 1, 10)
	tb.between = func(tb *table) {
		delete(tb.rows, id)
		l := written(0)
		tb.db[id] = audit.Recorded{Digest: d.Gone(id, l), Line: l}
	}

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))
	x.Zero(out.Read)
	x.True(out.Ok(), "%+v", out)
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

		r := recorded(x, d, id, tb.rows[id], 0)
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

// TestALineWithNoDigestIsAFindingOnceDigestsBegan: a row whose newest line
// keeps none says nothing, which is what anybody who can write the database
// can make of any line. Before the moment the deployment says digests began it
// is counted; after it, and when nothing was said, it is found (payday#115).
func TestALineWithNoDigestIsAFindingOnceDigestsBegan(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	old, _ := tb.add(x, d, 1, 10)
	tb.db[old] = audit.Recorded{Line: written(30 * 24 * time.Hour)}
	recent, _ := tb.add(x, d, 2, 20)
	tb.db[recent] = audit.Recorded{Line: written(time.Hour)}

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))
	x.Equal([]pdid.Id{old, recent}, out.NoDigest, "nothing said: digests are from the beginning")
	x.False(out.Ok())

	out = audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d.Since(at.Add(-7*24*time.Hour)), tb.kind(), tb.archive, &out))
	x.Equal([]pdid.Id{recent}, out.NoDigest)
	x.Equal(1, out.BeforeDigests)
}

// TestASinceLaterThanADigestIsRefused: a line written before the moment the
// deployment says digests began, and keeping one this deployment made, says
// they began earlier; every line without one in between would be counted as
// from before. The check refuses rather than counting them.
func TestASinceLaterThanADigestIsRefused(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	id := object
	tb.rows[id] = row(1, 0)
	tb.db[id] = recorded(x, d, id, tb.rows[id], 30*24*time.Hour)

	out := audit.Rows{}
	err = audit.CheckKind(t.Context(), d.Since(at), tb.kind(), tb.archive, &out)
	x.ErrorContains(err, "audit.digest.since")
}

// TestADigestUnderAKeyNotHeldIsAFinding: a check given the wrong key finds
// every row and names the key; one under a key the deployment let go is
// counted.
func TestADigestUnderAKeyNotHeldIsAFinding(t *testing.T) {
	x := require.New(t)
	server, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	id, r := tb.add(x, server, 1, 10)
	tb.db[id] = r

	wrong, err := audit.NewDigest(keyB)
	x.NoError(err)
	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), wrong, tb.kind(), tb.archive, &out))
	x.Equal([]pdid.Id{id}, out.KeyNotHeld)
	x.Equal([]audit.Fingerprint{audit.FingerprintOf(keyA)}, out.Unheld)
	x.False(out.Ok())

	letGo, err := wrong.Retire(audit.FingerprintOf(keyA))
	x.NoError(err)
	out = audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), letGo, tb.kind(), tb.archive, &out))
	x.Equal(1, out.Retired)
	x.True(out.Ok(), "%+v", out)
}

// TestADigestCopiedOntoTheNewestLineIsForged: a row put back to what an older
// write left, and that write's digest copied onto the newest line, is a line
// the digest is not of.
func TestADigestCopiedOntoTheNewestLineIsForged(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	id, older := tb.add(x, d, 1, 10)
	newest := written(0)
	tb.db[id] = audit.Recorded{Digest: older.Digest, Fields: older.Fields, Line: newest}

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))
	x.Equal([]pdid.Id{id}, out.Forged)
	x.False(out.Ok())
}

// TestARowBroughtBackFromAnEraseDiffers: the newest line says the write took
// the row, and the row is there.
func TestARowBroughtBackFromAnEraseDiffers(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	id, _ := tb.add(x, d, 1, 10)
	l := written(0)
	tb.db[id] = audit.Recorded{Digest: d.Gone(id, l), Line: l}

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d, tb.kind(), tb.archive, &out))
	x.Equal([]pdid.Id{id}, out.Differs)
}

// TestAThingTheTrailSaysIsARowAndIsNotIsMissing: the newest line about it
// says the row its write left, in the database or in the archive, and there is
// no row. One whose erase the trail recorded is not, and one whose newest line
// says nothing at all is missing too, unless it is from before digests.
func TestAThingTheTrailSaysIsARowAndIsNotIsMissing(t *testing.T) {
	x := require.New(t)
	d, err := audit.NewDigest(keyA)
	x.NoError(err)

	tb := newTable()
	removed, r := tb.add(x, d, 1, 10)
	tb.db[removed] = r
	delete(tb.rows, removed)

	archived, r := tb.add(x, d, 2, 10)
	tb.archived[archived] = r
	delete(tb.rows, archived)

	erased, _ := tb.add(x, d, 3, 10)
	delete(tb.rows, erased)
	l := written(0)
	tb.db[erased] = audit.Recorded{Digest: d.Gone(erased, l), Line: l}

	silent := object
	silent[15] = 4
	tb.db[silent] = audit.Recorded{Line: written(time.Hour)}

	old := object
	old[15] = 5
	tb.db[old] = audit.Recorded{Line: written(30 * 24 * time.Hour)}

	out := audit.Rows{}
	x.NoError(audit.CheckKind(t.Context(), d.Since(at.Add(-7*24*time.Hour)), tb.kind(), tb.archive, &out))
	x.Equal([]pdid.Id{removed, silent, archived}, out.Missing)
	x.Zero(out.Read, "none of them is a row")
	x.False(out.Ok())
}

// TestWhatACheckFoundIsOneDigest (payday#115): what an acceptance of the rows
// names, so that it is of what somebody looked at. The rows found and which
// finding each is are in it; the order they were read in, and what was fine,
// are not.
func TestWhatACheckFoundIsOneDigest(t *testing.T) {
	x := require.New(t)

	a, b := pdid.New(thing), pdid.New(thing)
	looked := audit.Rows{Read: 3, Matches: 1, Differs: []pdid.Id{a, b}}

	x.Equal(looked.Digest(), audit.Rows{Read: 9, Matches: 7, Differs: []pdid.Id{b, a}}.Digest())
	x.NotEqual(looked.Digest(), audit.Rows{Differs: []pdid.Id{a}}.Digest(), "a row less")
	x.NotEqual(looked.Digest(), audit.Rows{Differs: []pdid.Id{a, b, pdid.New(thing)}}.Digest(), "a row more")
	x.NotEqual(looked.Digest(), audit.Rows{Differs: []pdid.Id{a}, NotOnTrail: []pdid.Id{b}}.Digest(), "a row found as another finding")
	x.NotEqual(looked.Digest(), audit.Rows{}.Digest())
	x.True(strings.HasPrefix(looked.Digest(), "sha256:"))
}
