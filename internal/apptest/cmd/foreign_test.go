package cmd_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdcmd"

	app "github.com/lesomnus/payday/internal/apptest"
)

// TestAnRpcOfProtobufsOwnMessagesIsServed: `RobotService/Stamp` takes a
// `google.protobuf.Empty` and answers a `google.protobuf.Timestamp`
// (`proto/ext/app/robot_svc.ext.proto`), messages of another package than
// this app's. Every type a layer writes around the service named them under
// the app's package, and the server did not compile (payday#312); that it
// compiles is most of the claim. What is left is that it is served through
// every layer, in process, over the wire and from a command.
func TestAnRpcOfProtobufsOwnMessagesIsServed(t *testing.T) {
	// Each answer is a time the call was served at: after the call began and
	// before it was answered.
	within := func(t *testing.T, call func() (*timestamppb.Timestamp, error)) {
		t.Helper()

		before := time.Now()
		v, err := call()
		after := time.Now()
		require.NoError(t, err)
		require.NoError(t, v.CheckValid())
		require.WithinRange(t, v.AsTime(), before, after)
	}

	t.Run("in process", func(t *testing.T) {
		b, ctx := build(t)
		within(t, func() (*timestamppb.Timestamp, error) {
			return b.Walled.Robot().Stamp(b.as(ctx), &emptypb.Empty{})
		})
	})

	t.Run("over the wire", func(t *testing.T) {
		b, ctx := build(t)
		c := app.NewClient(b.dialed(t, ctx)).Robot()
		within(t, func() (*timestamppb.Timestamp, error) {
			return c.Stamp(b.travels(ctx), &emptypb.Empty{})
		})
	})

	t.Run("from a command", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		tree, err := pdcmd.New(pdcmd.Static(b.dialed(t, ctx)))
		x.NoError(err)
		c, err := tree.Unary("app.RobotService.Stamp")
		x.NoError(err)
		x.NoError(tree.Add("robot/stamp", c))

		within(t, func() (*timestamppb.Timestamp, error) {
			got := xlitest.Harness{Cmd: &xli.Command{Name: "app", Commands: tree.Commands()}, Ctx: b.travels(ctx)}.Run(t,
				"robot", "stamp", "-o", "json")
			if got.Err != nil {
				return nil, got.Err
			}

			// A Timestamp's JSON is the time, as a string.
			var text string
			if err := json.Unmarshal([]byte(strings.TrimSpace(got.Stdout)), &text); err != nil {
				return nil, err
			}
			at, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return nil, err
			}

			return timestamppb.New(at), nil
		})
	})
}
