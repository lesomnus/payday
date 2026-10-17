package cmd_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// rowReads counts the statements that read rows of one table by their key, off
// the driver they go through: the read that tells a write's caller what it
// wrote, and the one the trail made of the same row after it. Quotes are taken
// out so that SQLite's spelling and PostgreSQL's are one.
type rowReads struct {
	table string

	mu sync.Mutex
	n  int
}

func (r *rowReads) on(drv dialect.Driver) dialect.Driver {
	unquote := strings.NewReplacer("`", "", `"`, "")
	from, by := "FROM "+r.table+" WHERE ", r.table+".id = "
	return dialect.DebugWithContext(drv, func(_ context.Context, vs ...any) {
		q := unquote.Replace(fmt.Sprint(vs...))
		if strings.Contains(q, "SELECT") && strings.Contains(q, from) && strings.Contains(q, by) {
			r.mu.Lock()
			r.n++
			r.mu.Unlock()
		}
	})
}

func (r *rowReads) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// theTrailHolds is that the newest line of the trail about `id` holds `row`,
// which is what a read of it finds.
func (b *built) theTrailHolds(t *testing.T, ctx context.Context, id []byte, row proto.Message) {
	t.Helper()

	k, err := entuuid.FromBytes(id)
	require.NoError(t, err)
	vs, err := b.Ent.Audit.Query().Where(entaudit.ObjectId(k)).All(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, vs, "nothing was recorded, so this proves nothing")

	newest := vs[0]
	for _, v := range vs[1:] {
		if v.DateCreated.After(newest.DateCreated) {
			newest = v
		}
	}
	got := row.ProtoReflect().New().Interface()
	require.NoError(t, proto.Unmarshal(newest.Value, got))
	require.True(t, proto.Equal(row, got), "the trail holds %v, and a read finds %v", got, row)
}

// capture is a recorder told of every write after the trail is, and keeps the
// row each was handed.
type capture struct {
	rows []proto.Message
}

func (c *capture) Record(_ context.Context, _ bare.Server, v bare.Change) error {
	c.rows = append(c.rows, v.Value)
	return nil
}

func (*capture) Snapshots(string) bare.SnapshotOptions {
	return bare.SnapshotOptions{Value: true}
}

// TestTheTrailIsTheRowTheWriteRead is #84's third: a line of the trail holds
// the row its write left, and the recorder read that row again -- a statement
// for every write -- when the write had just read it itself. It is handed over
// now.
func TestTheTrailIsTheRowTheWriteRead(t *testing.T) {
	t.Run("an Add's is the row its INSERT answered with", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		robot := b.robot(t, ctx, b.Tenant, "arm")

		r := &rowReads{table: "reading"}
		s, err := enttx.Rebind(b.Walled, r.on(b.Drv))
		x.NoError(err)

		v, err := s.Reading().Add(b.as(ctx), reading(robot, 1))
		x.NoError(err)
		x.Zero(r.count(), "the reading was read again after its INSERT")

		read, err := bare.NewServer(b.Ent)
		x.NoError(err)
		row, err := read.Reading().Get(ctx, app.ReadingGetRequest_builder{
			Ref: app.ReadingRef_builder{Id: v.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		b.theTrailHolds(t, ctx, v.GetId(), row)
	})

	t.Run("a Patch's is the row it read back to answer with", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
			Alias:  "arm",
		}.Build())
		x.NoError(err)
		robot := v.GetId()

		// Ungated, so that a read of the robot through the wall is not among
		// those counted: what is left is the write's own.
		r := &rowReads{table: "robot"}
		s, err := enttx.Rebind(b.Ungated, r.on(b.Drv))
		x.NoError(err)

		_, err = s.Robot().Patch(ctx, app.RobotPatchRequest_builder{
			Ref:         app.RobotRef_builder{Id: robot}.Build(),
			DateUpdated: v.GetDateUpdated(),
			Alias:       z.Ptr("leg"),
		}.Build())
		x.NoError(err)
		x.Equal(1, r.count(), "the robot was read again after the read the write answered with")

		read, err := bare.NewServer(b.Ent)
		x.NoError(err)
		row, err := read.Robot().Get(ctx, app.RobotGetRequest_builder{
			Ref: app.RobotRef_builder{Id: robot}.Build(),
		}.Build())
		x.NoError(err)
		b.theTrailHolds(t, ctx, robot, row)
	})

	// The row is the same one for every recorder a write has, and the trail
	// keeps a secret out of what it holds: on a copy, or the next recorder
	// would be told of a row without the column the write wrote.
	t.Run("and every recorder is handed it whole", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		// Payday's minter, as the deployment's servers have it: the trail
		// learns which entity a row is from the domain its key carries.
		c := &capture{}
		s, err := bare.NewServer(b.Ent, bare.WithMinter(pd.Minter()), bare.WithRecorder(bare.Recorders{pd.Recorder(), c}))
		x.NoError(err)

		secret := []byte("hunter2-and-something-long-enough-to-find")
		v, err := s.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
			Alias:  "arm",
			Secret: secret,
		}.Build())
		x.NoError(err)

		x.Len(c.rows, 1)
		x.Equal(secret, c.rows[0].(*app.Robot).GetSecret(), "the trail took the secret out of the row the next recorder was handed")

		k, err := entuuid.FromBytes(v.GetId())
		x.NoError(err)
		w, err := b.Ent.Audit.Query().Where(entaudit.ObjectId(k)).Only(ctx)
		x.NoError(err)
		x.NotEmpty(w.Value)
		x.NotContains(string(w.Value), string(secret), "the trail holds the verifier")
	})
}
