package cmd_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// A layer may finish a generated verb, and docs/guide/server.md § Completing a
// generated verb says how. This is that shape, compiled and run.
//
// It is here rather than only in the guide because all three of the things that
// make it safe fail at **run time**: a second transaction where there should be
// one, a write that goes past the layer, and a method that calls itself. A
// snippet in a guide demonstrates none of them, and this app is where what
// payday says is shown to be true.
//
// A test layer rather than a rule in `core`, because what is under test is the
// shape and not a decision this app has made about robots. Stacking it here
// also keeps every other test's counts alone.

// joints is the layer: adding a robot adds the joint it is useless without.
type joints struct {
	app.Overlay

	// drv is why this layer is constructed with one at all. A layer that only
	// guards needs nothing; a layer that writes more than once needs a
	// transaction, and a transaction is begun on a driver.
	drv dialect.Driver
}

func newJoints(next app.Server, drv dialect.Driver) joints {
	return joints{app.NewOverlay(next), drv}
}

func jointsBuild(drv dialect.Driver) app.Builder { return jointsBuilder{drv} }

type jointsBuilder struct{ drv dialect.Driver }

func (b jointsBuilder) Build(next app.Server) (app.Server, error) {
	return newJoints(next, b.drv), nil
}

// WithDriver carries the driver **and replaces it**, which is the half a layer
// that only guards does not have to think about.
//
// Rebuilt with the original, this layer would open a transaction of its own on
// another connection while running inside somebody else's -- an RPC of the
// app's own that writes several rows -- and the writes would be split between
// two of them. With the new one, the transaction it begins is that one:
// `dialect.BeginTx` on a driver that is already a transaction answers with a
// `Tx` that cannot end it, so the inner `Commit` is a no-op and the work joins
// the transaction that wrapped it.
func (s joints) WithDriver(drv dialect.Driver) (app.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return newJoints(next, drv), nil
}

var (
	_ app.Server               = joints{}
	_ enttx.Binder[app.Server] = joints{}
)

type jointsRobot struct {
	joints
	app.RobotServiceServer
}

func (s joints) Robot() app.RobotServiceServer {
	return jointsRobot{s, s.Next().Robot()}
}

type jointsJoint struct {
	joints
	app.JointServiceServer
}

func (s joints) Joint() app.JointServiceServer {
	return jointsJoint{s, s.Next().Joint()}
}

// Add is the rule this layer holds about joints, and it is here so that the
// composite below has something to go through. Written with `s.Next()` instead,
// the joint an `Add` writes for itself would be the one joint in the app this
// never saw -- which is the whole of what "through the layer" buys, and it is
// invisible until a rule like this one exists.
func (s jointsJoint) Add(ctx context.Context, req *app.JointAddRequest) (*app.Joint, error) {
	if strings.HasPrefix(req.GetAlias(), "boom") {
		return nil, errors.New("this joint is not written")
	}

	return s.JointServiceServer.Add(ctx, req)
}

func (s jointsRobot) Add(ctx context.Context, req *app.RobotAddRequest) (*app.Robot, error) {
	drv, tx, err := dialect.BeginTx(ctx, s.drv)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}
	at := newJoints(next, drv)

	// The row itself goes to the server **below**: `at.Robot().Add` is this
	// method again.
	v, err := next.Robot().Add(ctx, req)
	if err != nil {
		return nil, err
	}

	// And the rest through this layer, so the rule above meets the joint this
	// method wrote as well as one a caller sends.
	if _, err := at.Joint().Add(ctx, app.JointAddRequest_builder{
		Robot: app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias: v.GetAlias() + "-base",
	}.Build()); err != nil {
		return nil, err
	}

	return v, tx.Commit()
}

// composed is the stack these tests call: the layer, and the servers under it.
// No wall and no gate -- what is under test is what one call writes, and
// narrowing reads would only make the counts harder to read.
func (b *built) composed(t *testing.T) app.Server {
	t.Helper()

	sink, err := pd.NewSink(b.Ent, bare.WithMinter(pd.Minter()))
	require.NoError(t, err)

	s, err := app.Build(sink, jointsBuild(b.Drv))
	require.NoError(t, err)

	return s
}

// rows is what is in the database, which is where a rollback is a fact.
func (b *built) rows(ctx context.Context, x *require.Assertions) (int, int) {
	robots, err := b.Ent.Robot.Query().Count(ctx)
	x.NoError(err)

	joints, err := b.Ent.Joint.Query().Count(ctx)
	x.NoError(err)

	return robots, joints
}

// TestCompletingAVerbWritesWhatTheRowIsUselessWithout, which is the shape
// working at all.
func TestCompletingAVerbWritesWhatTheRowIsUselessWithout(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	robots, joints := b.rows(ctx, x)

	v, err := b.composed(t).Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)
	x.Equal("arm-01", v.GetAlias())

	gotRobots, gotJoints := b.rows(ctx, x)
	x.Equal(robots+1, gotRobots)
	x.Equal(joints+1, gotJoints, "the joint the robot is useless without")
}

// TestAVerbRefusedHalfwayWritesNothing: one transaction, or the row that is
// left is the unfinished state the whole shape exists to make impossible.
func TestAVerbRefusedHalfwayWritesNothing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	robots, joints := b.rows(ctx, x)

	_, err := b.composed(t).Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "boom-01",
	}.Build())
	x.Error(err)

	gotRobots, gotJoints := b.rows(ctx, x)
	x.Equal(robots, gotRobots, "the robot outlived the joint it is useless without")
	x.Equal(joints, gotJoints)
}

// TestCompletingAVerbInsideATransactionIsOneTransaction is the half a layer
// that carries its driver wrongly passes anyway.
//
// An RPC of the app's own that writes several rows as one puts the whole stack
// on its own transaction (`enttx.Rebind`), so this layer's `Add` runs already
// inside one. What it then begins is that transaction rather than a second:
// the driver it was rebound with is a transaction wearing a driver's clothes,
// and what such a driver hands out is a `Tx` that cannot end it.
//
// So a transaction refused at its second write takes the **first write's
// joint** with it. A layer that had kept the original driver would have
// committed that joint on another connection, and it would still be there.
func TestCompletingAVerbInsideATransactionIsOneTransaction(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	robots, joints := b.rows(ctx, x)

	tenant := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()
	adds := func(aliases ...string) error {
		drv, tx, err := dialect.BeginTx(ctx, b.Drv)
		x.NoError(err)

		s, err := enttx.Rebind(b.composed(t), drv)
		x.NoError(err)
		for _, v := range aliases {
			if _, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{Tenant: tenant, Alias: v}.Build()); err != nil {
				x.NoError(tx.Rollback())
				return err
			}
		}

		return tx.Commit()
	}

	x.Error(adds("arm-02", "boom-02"))

	gotRobots, gotJoints := b.rows(ctx, x)
	x.Equal(robots, gotRobots)
	x.Equal(joints, gotJoints,
		"the joint of the write before the refusal was committed on its own")

	// And the same writes without the refusal leave both halves of both,
	// which is what says the assertions above are a rollback.
	x.NoError(adds("arm-03", "arm-04"))

	gotRobots, gotJoints = b.rows(ctx, x)
	x.Equal(robots+2, gotRobots)
	x.Equal(joints+2, gotJoints)
}
