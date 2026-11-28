package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/core"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// A time the store writes is what the database keeps, on SQLite as on
// PostgreSQL: cut to the microsecond (payday#324). An Add answers the row as
// it built it, and its version used to carry the clock's nanoseconds, which
// PostgreSQL does not keep -- a version taken from an Add and compared in Go
// against the row read back was refused there, and passed here.
//
// What is compared is not cut, so a row stored before, which on SQLite can
// hold nanoseconds, is still patched with the version a read gives.

// fineAt is a time with nanoseconds in it, which is what a clock on Linux
// answers; keptAt is what a `timestamptz` keeps of it.
var (
	fineAt = time.Date(2026, 11, 28, 9, 30, 0, 123456789, time.UTC)
	keptAt = time.Date(2026, 11, 28, 9, 30, 0, 123456000, time.UTC)
)

// clocked is the app's stack in the order `cmd.Build` assembles it, with the
// trail recording, on a store whose clock answers `fineAt`.
func clocked(t *testing.T) (app.Server, *built, context.Context) {
	t.Helper()

	b, ctx := build(t)
	walled, err := pd.NewSink(b.Ent,
		bare.WithMinter(pd.Minter()),
		bare.WithRecorder(pd.Recorder(b.Contents)),
		bare.WithScope(pd.Wall()),
		bare.WithClock(func() time.Time { return fineAt }),
	)
	require.NoError(t, err)

	s, err := app.Build(walled.WithWatch(b.Watch),
		core.Build(b.Drv, b.Contents),
		pd.AuditBuild(),
		pd.SecretBuild(),
		pd.InterceptBuild(nil, nil),
		pd.GateBuild())
	require.NoError(t, err)

	return s, b, b.as(ctx)
}

func TestATimeWrittenIsWhatTheDatabaseKeeps(t *testing.T) {
	s, b, ctx := clocked(t)

	add := func(x *require.Assertions, alias string, serviced time.Time) *app.Machine {
		v, err := s.Machine().Add(ctx, app.MachineAddRequest_builder{
			Tenant:       app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:        alias,
			DateServiced: timestamppb.New(serviced),
		}.Build())
		x.NoError(err)
		return v
	}
	get := func(x *require.Assertions, v *app.Machine) *app.Machine {
		got, err := s.Machine().Get(ctx, app.MachineGetRequest_builder{Ref: v.Ref()}.Build())
		x.NoError(err)
		return got
	}

	t.Run("an Add answers what a Get answers", func(t *testing.T) {
		x := require.New(t)
		v := add(x, "m-stamped", fineAt.Add(-time.Hour))
		got := get(x, v)

		// Stamped by the store's clock.
		x.True(keptAt.Equal(v.GetDateUpdated().AsTime()), "date_updated is %s", v.GetDateUpdated().AsTime())
		x.True(proto.Equal(got.GetDateUpdated(), v.GetDateUpdated()), "an Add answered %s and a Get %s",
			v.GetDateUpdated().AsTime(), got.GetDateUpdated().AsTime())
		x.True(proto.Equal(got.GetDateCreated(), v.GetDateCreated()))

		// Handed in by the caller.
		x.True(keptAt.Add(-time.Hour).Equal(v.GetDateServiced().AsTime()), "date_serviced is %s", v.GetDateServiced().AsTime())
		x.True(proto.Equal(got.GetDateServiced(), v.GetDateServiced()))
	})

	t.Run("a Patch whose test is an Add's answer holds", func(t *testing.T) {
		x := require.New(t)
		v := add(x, "m-patched", fineAt)

		u, err := s.Machine().Patch(ctx, app.MachinePatchRequest_builder{
			Ref:          v.Ref(),
			DateUpdated:  v.GetDateUpdated(),
			Model:        z.Ptr("X-2"),
			DateServiced: timestamppb.New(fineAt.Add(time.Hour)),
		}.Build())
		x.NoError(err)
		x.Equal("X-2", u.GetModel())
		x.True(keptAt.Add(time.Hour).Equal(u.GetDateServiced().AsTime()), "date_serviced is %s", u.GetDateServiced().AsTime())
		x.True(proto.Equal(get(x, v).GetDateUpdated(), u.GetDateUpdated()))
	})

	// Written through ent, as the store wrote it before: on SQLite the row
	// holds nanoseconds, and a read answers them. PostgreSQL never held them,
	// so there the case is the one above.
	t.Run("a row stored with nanoseconds is patched with the version a read gives", func(t *testing.T) {
		x := require.New(t)
		v := add(x, "m-old", fineAt)
		k, err := pdid.From(v.GetId())
		x.NoError(err)

		old := fineAt.Add(-24 * time.Hour)
		x.NoError(b.Ent.Machine.UpdateOneId(k.Uuid()).SetDateUpdated(old).Exec(ctx))

		got := get(x, v)
		if b.Drv.Dialect() == "sqlite3" {
			x.True(old.Equal(got.GetDateUpdated().AsTime()), "date_updated is %s", got.GetDateUpdated().AsTime())
		}

		u, err := s.Machine().Patch(ctx, app.MachinePatchRequest_builder{
			Ref:         v.Ref(),
			DateUpdated: got.GetDateUpdated(),
			Model:       z.Ptr("X-3"),
		}.Build())
		x.NoError(err)
		x.True(keptAt.Equal(u.GetDateUpdated().AsTime()), "date_updated is %s", u.GetDateUpdated().AsTime())
	})
}

// What payday writes beside a row, by the wall clock -- the outbox's row and
// the trail's line -- goes the same way. Whole microseconds, which on SQLite is
// what tells a stamp that was cut from one that was not: the wall clock answers
// nanoseconds.
func TestWhatPaydayWritesOfItsOwnIsAtTheMicrosecond(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)

	_, err := b.Walled.Machine().Add(b.as(ctx), app.MachineAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "m-own",
	}.Build())
	x.NoError(err)

	lines, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	x.NotEmpty(lines)
	for _, v := range lines {
		x.Zero(v.DateCreated.Nanosecond()%1000, "the trail's line is at %s", v.DateCreated)
	}
	boxed, err := b.Ent.Outbox.Query().All(ctx)
	x.NoError(err)
	x.NotEmpty(boxed)
	for _, v := range boxed {
		x.Zero(v.DateCreated.Nanosecond()%1000, "the outbox's row is at %s", v.DateCreated)
	}
}
