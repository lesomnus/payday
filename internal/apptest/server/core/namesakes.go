package core

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/payday/internal/apptest"
)

// Seal, Reading, Job and Joint are RPCs called what other entities of this app
// are called, one of each shape a method has (`robot_svc.ext.proto`). What
// they do is read the robot named, through the wall below this layer, and
// answer it; what is being shown is that they are served at all, through
// every layer, and refused where the gate's rule for them says.

// Seal answers the robot named.
func (s coreRobot) Seal(ctx context.Context, req *app.RobotRef) (*app.Robot, error) {
	return s.read(ctx, req)
}

// Reading sends the robot named, once.
func (s coreRobot) Reading(req *app.RobotRef, out grpc.ServerStreamingServer[app.Robot]) error {
	v, err := s.read(out.Context(), req)
	if err != nil {
		return err
	}

	return out.Send(v)
}

// Job answers every robot it was sent, once the caller has sent them all.
func (s coreRobot) Job(out grpc.ClientStreamingServer[app.RobotRef, app.RobotJobResponse]) error {
	var vs []*app.Robot
	for {
		ref, err := out.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		v, err := s.read(out.Context(), ref)
		if err != nil {
			return err
		}
		vs = append(vs, v)
	}

	return out.SendAndClose(app.RobotJobResponse_builder{Items: vs}.Build())
}

// Joint answers each robot it is sent as it is sent it.
func (s coreRobot) Joint(out grpc.BidiStreamingServer[app.RobotRef, app.Robot]) error {
	for {
		ref, err := out.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		v, err := s.read(out.Context(), ref)
		if err != nil {
			return err
		}
		if err := out.Send(v); err != nil {
			return err
		}
	}
}

// read is the robot `ref` names, read through the wall.
func (s coreRobot) read(ctx context.Context, ref *app.RobotRef) (*app.Robot, error) {
	return s.RobotServiceServer.Get(ctx, app.RobotGetRequest_builder{Ref: ref}.Build())
}

// Survey answers the robot named in every place its answer can hold one,
// each a copy of the robot as it was read: two messages deep, in a list, a
// map's values, a oneof's case, and a cell inside a cell.
func (s coreRobot) Survey(ctx context.Context, req *app.RobotRef) (*app.RobotSurvey, error) {
	read := func() (*app.Robot, error) { return s.read(ctx, req) }

	var vs []*app.Robot
	for range 5 {
		v, err := read()
		if err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}

	cell := func(v *app.Robot) *app.RobotSurveyCell {
		return app.RobotSurveyCell_builder{Robot: v}.Build()
	}

	return app.RobotSurvey_builder{
		Cell: app.RobotSurveyCell_builder{
			Robots: []*app.Robot{vs[0]},
			Within: []*app.RobotSurveyCell{cell(vs[1])},
		}.Build(),
		Cells:   []*app.RobotSurveyCell{cell(vs[2])},
		ByAlias: map[string]*app.RobotSurveyCell{vs[3].GetAlias(): cell(vs[3])},
		First:   cell(vs[4]),
	}.Build(), nil
}

// Stamp answers the time it was served at (`robot_svc.ext.proto`). Its request
// and answer are protobuf's own messages, which is all it is here for.
func (s coreRobot) Stamp(ctx context.Context, req *emptypb.Empty) (*timestamppb.Timestamp, error) {
	return timestamppb.Now(), nil
}
