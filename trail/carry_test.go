package trail

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"uuid"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
)

// carrying is a store of robots' trail, each line with the digest its write
// left of a row, and the key those were made under.
type carrying struct {
	x      *require.Assertions
	s      *fakeStore
	d      *audit.Digest
	tenant pdid.Id
}

func newCarrying(t *testing.T) *carrying {
	d, err := audit.NewDigest(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)

	return &carrying{x: require.New(t), s: &fakeStore{live: map[uuid.UUID]bool{}}, d: d, tenant: newTenant()}
}

// state is what a robot's row was at the n-th write.
func state(n int64) proto.Message { return &timestamppb.Timestamp{Seconds: n} }

// write is a line about `object`, `ago` before now, keeping the digest of the
// row as `state(n)`; a negative n keeps none.
func (c *carrying) write(object pdid.Id, ago time.Duration, n int64) fakeRow {
	r := fakeRow{
		id: uuid.NewV7(), tenant: c.tenant.Uuid(), object: object.Uuid(), domain: robot,
		created: time.Now().Add(-ago).UTC().Truncate(time.Microsecond),
	}
	if n >= 0 {
		sum, err := c.d.Of(object, state(n), []uint32{1, 2}, r.line())
		c.x.NoError(err)
		r.digest = sum
	}

	return c.s.add(r)
}

// carried is the lines the pass carried forward, by what they are about.
func (c *carrying) carried() map[pdid.Id]fakeRow {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()

	out := map[pdid.Id]fakeRow{}
	for _, r := range c.s.rows {
		if r.action == CarriedAction {
			out[pdid.Id(r.object)] = r
		}
	}

	return out
}

// TestARowStillThereIsCarriedForwardBeforeItsLineIsDestroyed (payday#115): a
// pass about to destroy the newest line about a robot that is still a row
// writes a line of the database's own, keeping what the destroyed line said of
// the row -- so it still matches the row it vouched for and still differs
// from one changed since. Nothing is carried for a robot that is gone, one
// with a newer line in the archive or the database, or one whose line kept no
// digest.
func TestARowStillThereIsCarriedForwardBeforeItsLineIsDestroyed(t *testing.T) {
	t.Run("with no lease", func(t *testing.T) { carriesForward(t, nil) })

	// In the trail's turn, which a pass takes for the writing and gives back.
	t.Run("in the trail's turn", func(t *testing.T) { carriesForward(t, spin.Local()) })
}

func carriesForward(t *testing.T, lease spin.Lease) {
	c := newCarrying(t)
	x := c.x

	still, gone, newer, inDb, before := pdid.New(robot), pdid.New(robot), pdid.New(robot), pdid.New(robot), pdid.New(robot)
	for _, v := range []pdid.Id{still, newer, inDb, before} {
		c.s.live[v.Uuid()] = true
	}

	c.write(still, 2*year, 1)
	c.write(gone, 2*year, 1)
	c.write(newer, 2*year, 1)
	c.write(newer, 100*day, 2)
	c.write(inDb, 2*year, 1)
	c.write(inDb, day, 2)
	c.write(before, 2*year, -1)

	p := Policy{Signer: signer, Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day, Destroy: year}, Digest: c.d, Lease: lease}
	p.Pass(t.Context(), c.s)

	got := c.carried()
	x.Len(got, 1, "%v", got)
	r, ok := got[still]
	x.True(ok)
	x.Equal(c.tenant.Uuid(), r.tenant, "filed where the line it carries was")

	v, err := c.d.Check(r.digest, still, state(1), []uint32{1, 2}, r.line())
	x.NoError(err)
	x.Equal(audit.Matches, v, "what the destroyed line said of the row")

	v, err = c.d.Check(r.digest, still, state(9), []uint32{1, 2}, r.line())
	x.NoError(err)
	x.Equal(audit.Differs, v, "carried as recorded, and not made again of the row as it is")

	held := inArchive(t, p.Archive)
	x.Len(slices.Concat(slicesOf(held)...), 1, "the year-old chunk went; the line of a hundred days stays")
}

// TestNothingIsCarriedWithoutAKey: a pass with no digest key destroys what its
// window says and carries nothing, which a check of the rows then finds.
func TestNothingIsCarriedWithoutAKey(t *testing.T) {
	c := newCarrying(t)

	still := pdid.New(robot)
	c.s.live[still.Uuid()] = true
	c.write(still, 2*year, 1)

	p := Policy{Signer: signer, Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day, Destroy: year}}
	p.Pass(t.Context(), c.s)

	c.x.Empty(c.carried())
	c.x.Empty(inArchive(t, p.Archive), "the window is an obligation, carried forward or not")
}

func slicesOf(m map[string][]uuid.UUID) [][]uuid.UUID {
	out := [][]uuid.UUID{}
	for _, v := range m {
		out = append(out, v)
	}

	return out
}
