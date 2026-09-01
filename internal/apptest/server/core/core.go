// Package core is what this app writes by hand.
//
// payday generates the CRUD of every entity and refuses to serve a general
// write. What is left is an operation that means something -- one with a rule
// nothing else has -- and there is exactly one of those here.
//
// # Why it exists in the test app at all
//
// It is the half of payday's doctrine that had no demonstration. "The general
// writes are closed, so an operation that means something has to be declarable"
// is written up in the schema guide and had only ever been carried out in an
// app built on payday elsewhere. Everything else payday claims is proved here;
// this was not, so `pd gen` merging an overlay that adds an **Rpc** -- rather
// than a field -- was a path nothing in this module took.
//
// It is a layer in the same sense every other one is: a `struct{ app.Overlay }`
// that answers the Rpcs it has something to say about and hands the rest down.
// So it stacks with the wall, the gate and the trail rather than standing beside
// them, and a `Move` is on the trail for the same reason an `Add` is -- nothing
// listed it.
//
// What the trail says it was is `Move`, not the `Patch` below. That is
// `bare.Change.Method`, which carries the Rpc gRpc dispatched for the whole
// request rather than the leg being written -- so an operation written by hand
// answers "who did this" with the name the caller used. Called directly against
// the server, with nothing dispatched, the trail says `Patch`, which is the
// honest answer rather than a fallback. `TestMoveIsOnTheTrail` asserts both.
package core

import (
	"context"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/payday/internal/apptest"
)

// Core is the layer that answers what this app wrote.
type Core struct {
	app.Overlay
}

func New(next app.Server) Core { return Core{app.NewOverlay(next)} }

// Build makes a builder of this layer so that it can be stacked.
func Build() app.Builder { return builder{} }

type builder struct{}

func (builder) Build(next app.Server) (app.Server, error) { return New(next), nil }

var (
	_ app.Server               = Core{}
	_ enttx.Binder[app.Server] = Core{}
)

// WithDriver answers with this stack running on `drv`.
//
// Every layer writes this and none can inherit it: an overlay holds what is
// behind it and has no way to make itself again, so a layer that did not write
// it would be missing from the rebuilt stack and the requests inside a
// transaction would go around it. `pd doctor` is what finds a layer that forgot.
func (s Core) WithDriver(drv dialect.Driver) (app.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return New(next), nil
}

type coreRobot struct {
	Core
	app.RobotServiceServer
}

func (s Core) Robot() app.RobotServiceServer {
	return coreRobot{s, s.Next().Robot()}
}

// Move puts a robot in another cell.
//
// # What the wall does and does not do here
//
// Reading the robot is narrowed like every other read, so a caller who cannot
// see it is told NotFound -- that it exists is itself something not to say.
//
// The **destination** is not a narrowing. The row has not moved, so there is
// nothing yet to filter; whether this caller may put a robot in that cell is a
// decision about a row that does not exist in that shape yet. payday has the
// same shape in its own gate -- "a holder is added to a tenant you can see" --
// and the answer is the same: read the cell through the wall, and let NotFound
// be the answer when it is not visible.
//
// So a caller whose grant covers every cell may move a robot anywhere; one
// granted a single cell can only move a robot it can see into the cell it can
// see, which is where it already is. That is the rule falling out of the scope
// rather than being written twice.
//
// # Why the cell in particular
//
// Field 3 is the set a row is in -- payday's second narrowing, under the
// tenant. So this moves a row between the groups a caller's grant is written in
// terms of, and after it the same caller may no longer be able to read what it
// just moved. An ordinary field write does not do that, which is why this is an
// operation with a name rather than a `Patch`.
func (s coreRobot) Move(ctx context.Context, req *app.RobotMoveRequest) (*app.Robot, error) {
	if _, err := gate.Actor(ctx); err != nil {
		return nil, err
	}

	// Read through the wall, so this is also the check that the caller holds it.
	v, err := s.RobotServiceServer.Get(ctx, app.RobotGetRequest_builder{
		Ref: req.GetRef(),
	}.Build())
	if err != nil {
		return nil, err
	}

	to := req.GetTo()
	if to == nil {
		// Out of every cell, which is a move like any other: `Robot.cell` is
		// nullable, so there is somewhere for it to go.
		return s.RobotServiceServer.Patch(ctx, app.RobotPatchRequest_builder{
			Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
			CellNull:    proto.Bool(true),
			DateUpdated: v.GetDateUpdated(),
		}.Build())
	}

	// The destination, read through the wall for the reason above. `Next()`
	// rather than this layer, because there is nothing this layer adds to a
	// cell and going through itself would be a loop waiting for somebody to
	// add one.
	cell, err := s.Core.Next().Cell().Get(ctx, app.CellGetRequest_builder{
		Ref: to,
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, gate.ErrNotFound("Cell")
		}

		return nil, err
	}

	if string(v.GetCell().GetId()) == string(cell.GetId()) {
		return nil, pderr.Invalidf("to", "it is already there")
	}

	// One write, through the servers below -- so the trail, the watch and the
	// outbox are told exactly as they are for anything else.
	//
	// `DateUpdated` is carried from the row that was read, which is what makes
	// this refuse a move racing another write rather than overwriting it.
	return s.RobotServiceServer.Patch(ctx, app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
		Cell:        app.CellRef_builder{Id: cell.GetId()}.Build(),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
}
