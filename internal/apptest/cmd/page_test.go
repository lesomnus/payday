package cmd_test

import (
	"context"
	"errors"
	"testing"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/page"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestAllPagesPreserveTheRequestAndStopWhenTheConsumerStops(t *testing.T) {
	filter := app.RobotFilter_builder{Tenant: app.TenantRef_builder{Alias: proto.String("acme")}.Build()}.Build()
	req := app.RobotListRequest_builder{Size: 2, After: "start", Reverse: true, Filters: []*app.RobotFilter{filter}}.Build()
	want := proto.Clone(req)
	var after []string
	list := func(ctx context.Context, r *app.RobotListRequest) (*app.RobotListResponse, error) {
		require.Same(t, t.Context(), ctx)
		require.Equal(t, int32(2), r.GetSize())
		require.True(t, r.GetReverse())
		require.True(t, proto.Equal(filter, r.GetFilters()[0]))
		require.NotSame(t, filter, r.GetFilters()[0], "request copies include nested filters")
		after = append(after, r.GetAfter())
		switch r.GetAfter() {
		case "start":
			return app.RobotListResponse_builder{Items: []*app.Robot{app.Robot_builder{Alias: "a"}.Build()}, Next: "empty"}.Build(), nil
		case "empty":
			return app.RobotListResponse_builder{Next: "last"}.Build(), nil
		default:
			return app.RobotListResponse_builder{Items: []*app.Robot{app.Robot_builder{Alias: "b"}.Build()}}.Build(), nil
		}
	}
	all := page.All(t.Context(), list, req)
	require.Empty(t, after, "creation does not fetch a page")
	var aliases []string
	for v, err := range all {
		require.NoError(t, err)
		aliases = append(aliases, v.GetAlias())
	}
	require.Equal(t, []string{"a", "b"}, aliases)
	require.Equal(t, []string{"start", "empty", "last"}, after)
	require.True(t, proto.Equal(want, req), "pagination must not consume the caller's cursor")
	after = nil
	for _, err := range all {
		require.NoError(t, err)
		break
	}
	require.Equal(t, []string{"start"}, after, "a repeated traversal starts fresh; break fetches no next page")
}

func TestAllPagesYieldAPartialFailureWithoutRetrying(t *testing.T) {
	calls := 0
	failed := errors.New("second page failed")
	list := func(context.Context, *app.RobotListRequest) (*app.RobotListResponse, error) {
		calls++
		if calls == 1 {
			return app.RobotListResponse_builder{Items: []*app.Robot{app.Robot_builder{Alias: "first"}.Build()}, Next: "next"}.Build(), nil
		}
		return nil, failed
	}
	var rows int
	var failures []error
	for row, err := range page.All(t.Context(), list, &app.RobotListRequest{}) {
		if err != nil {
			require.Nil(t, row)
			failures = append(failures, err)
		} else {
			rows++
		}
	}
	require.Equal(t, 1, rows)
	require.Equal(t, []error{failed}, failures)
	require.Equal(t, 2, calls)

	var nilReq *app.RobotListRequest
	for _, err := range page.All(t.Context(), list, nilReq) {
		require.ErrorContains(t, err, "nil request")
	}
	require.Equal(t, 2, calls, "an invalid request is not sent")
}

func TestAllPagesStopOnFailureAndCancellation(t *testing.T) {
	failed := errors.New("source unavailable")
	for _, mode := range []string{"error", "cycle", "nil", "cancel", "already cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "already cancelled" {
				cancel()
			}
			calls := 0
			list := func(ctx context.Context, req *app.RobotListRequest) (*app.RobotListResponse, error) {
				calls++
				switch mode {
				case "error":
					return nil, failed
				case "nil":
					return nil, nil
				case "cancel":
					cancel()
				}
				return app.RobotListResponse_builder{Items: []*app.Robot{{}}, Next: "same"}.Build(), nil
			}
			var got []error
			for _, err := range page.All(ctx, list, app.RobotListRequest_builder{After: "same"}.Build()) {
				if err != nil {
					got = append(got, err)
				}
			}
			require.Len(t, got, 1)
			switch mode {
			case "error":
				require.ErrorIs(t, got[0], failed)
			case "cycle":
				require.ErrorIs(t, got[0], page.ErrCursor)
			case "nil":
				require.ErrorContains(t, got[0], "nil List response")
			default:
				require.ErrorIs(t, got[0], context.Canceled)
			}
			if mode == "already cancelled" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestAllPagesWalkTheRealServerAndClient(t *testing.T) {
	b, ctx := build(t)
	for _, alias := range []string{"a", "b", "c", "d", "e"} {
		_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: alias}.Build())
		require.NoError(t, err)
	}
	req := app.RobotListRequest_builder{Size: 2}.Build()
	var direct []string
	for robot, err := range page.All(b.as(ctx), b.Walled.Robot().List, req) {
		require.NoError(t, err)
		direct = append(direct, robot.GetAlias())
	}
	require.Equal(t, []string{"a", "b", "c", "d", "e"}, direct)
	client := app.NewRobotServiceClient(b.dialed(t, ctx))
	var wire []string
	for robot, err := range page.All(b.travels(ctx), page.Client(client.List, grpc.WaitForReady(true)), req) {
		require.NoError(t, err)
		wire = append(wire, robot.GetAlias())
	}
	require.Equal(t, direct, wire)
	require.Empty(t, req.GetAfter())
}
