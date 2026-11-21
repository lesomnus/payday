package cmd_test

import (
	"testing"
	"time"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCommandsUseTheRequestedDisplayZoneBeforeCallingTheServer(t *testing.T) {
	b, ctx := build(t)
	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "zone",
		DateAttested: timestamppb.New(time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)),
	}.Build())
	require.NoError(t, err)
	conn := b.dialed(t, ctx)
	run := func(args ...string) xlitest.Result {
		return (xlitest.Harness{Cmd: rooted(t, conn), Ctx: b.travels(ctx)}).Run(t, args...)
	}
	t.Setenv("APP_TZ", "Asia/Seoul")
	got := run("robot", "get", "@acme/zone")
	require.NoError(t, got.Err)
	require.Contains(t, got.Stdout, "2026-11-01T08:30:00+09:00")
	got = run("robot", "get", "--tz", "UTC", "@acme/zone")
	require.NoError(t, got.Err)
	require.Contains(t, got.Stdout, "2026-10-31T23:30:00Z")
	got = run("robot", "get", "--tz", "Asia/Seoul", "-o", "json", "@acme/zone")
	require.NoError(t, got.Err)
	require.Contains(t, got.Stdout, "2026-10-31T23:30:00Z")
	got = run("robot", "add", "--tz", "No/SuchZone", `{"tenant":"@acme","alias":"must-not-write"}`)
	require.ErrorContains(t, got.Err, "--tz")
	n, err := b.Ent.Robot.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n, "format validation precedes a mutating RPC")
}
