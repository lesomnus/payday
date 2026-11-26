package core

import (
	"context"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/frame"

	app "github.com/lesomnus/payday/internal/apptest"
)

// TokenScope reads the robot named, through the wall, and answers the scope of
// the token the call came with (`robot_svc.ext.proto`).
//
// The scope is read off the frame the call already has, which is the whole of
// what a handler does to get at it. An app that narrows by it would look up
// the row it names here, or in a scope of its own beside the wall; this one
// answers it, so that a test can say which token's it was.
func (s coreRobot) TokenScope(ctx context.Context, req *app.RobotRef) (*app.RobotTokenScope, error) {
	if _, err := s.read(ctx, req); err != nil {
		return nil, err
	}

	return app.RobotTokenScope_builder{Scope: z.Ptr(frame.Must(ctx).TokenScope())}.Build(), nil
}
