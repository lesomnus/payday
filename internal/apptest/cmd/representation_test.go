package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
	"uuid"
)

func TestRepresentedWriteRecordsBothPartiesAndDirectCallsRemainNull(t *testing.T) {
	b, ctx := build(t)
	f := frame.New(b.Holder, b.Tenant, frame.Whole()).WithScope(frame.Only(b.Tenant))
	caller := pdid.New(pd.HolderDomain)
	f.Caller = &frame.Principal{Actor: caller, Tenant: b.Tenant}
	f.Presence = "session-a"
	for _, represented := range []bool{true, false} {
		if !represented {
			f.Caller = nil
			f.Presence = ""
		}
		row, err := b.Walled.Robot().Add(frame.Into(ctx, f), app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: map[bool]string{true: "represented", false: "direct"}[represented]}.Build())
		require.NoError(t, err)
		lines, err := b.Ungated.Audit().List(ctx, app.AuditListRequest_builder{Filters: []*app.AuditFilter{app.AuditFilter_builder{ObjectId: row.GetId()}.Build()}}.Build())
		require.NoError(t, err)
		require.Len(t, lines.GetItems(), 1)
		line := lines.GetItems()[0]
		require.Equal(t, b.Holder.Bytes(), line.GetActorId())
		if represented {
			require.Equal(t, caller.Bytes(), line.GetCallerId())
			require.Equal(t, b.Tenant.Bytes(), line.GetCallerTenantId())
			require.Equal(t, "session-a", line.GetPresence())
		} else {
			require.Empty(t, line.GetCallerId())
			require.Empty(t, line.GetCallerTenantId())
			require.Empty(t, line.GetPresence())
			count, err := b.Ent.Audit.Query().Where(entaudit.IdEQ(uuid.UUID(line.GetId())), entaudit.CallerIdIsNil(), entaudit.CallerTenantIdIsNil(), entaudit.PresenceIsNil()).Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, count, "direct calls store SQL NULL, not a zero UUID")
		}
	}
}

func TestDirectMethodRefusesRepresentationAtTransportAndInsideTheProcess(t *testing.T) {
	b, ctx := build(t)
	f := frame.New(b.Holder, b.Tenant, frame.Whole()).WithScope(frame.Only(b.Tenant))
	f.Caller = &frame.Principal{Actor: pdid.New(pd.HolderDomain), Tenant: b.Tenant}
	ctx = frame.Into(ctx, f)
	_, err := gate.Decide(ctx, nil, app.RobotService_Move_FullMethodName)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	// The outer RPC could be any permitted operation. The generated gate checks
	// the actual inner method, before even reading the deliberately absent robot.
	_, err = b.Walled.Robot().Move(ctx, app.RobotMoveRequest_builder{}.Build())
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	direct := *f
	direct.Caller = nil
	_, err = b.Walled.Robot().Move(frame.Into(ctx, &direct), app.RobotMoveRequest_builder{}.Build())
	require.NotEqual(t, codes.PermissionDenied, status.Code(err))
}

func TestDelegatedWriteRecordsTheCallerAsAnAskedOneDoes(t *testing.T) {
	b, ctx := build(t)
	agent := pdid.New(pd.HolderDomain)
	for _, delegated := range []bool{true, false} {
		f := frame.New(b.Holder, b.Tenant, frame.Whole()).WithScope(frame.Only(b.Tenant))
		f.Caller = &frame.Principal{Actor: agent, Tenant: b.Tenant}
		f.Presence = "delegation-a"
		f.Delegated = delegated
		row, err := b.Walled.Robot().Add(frame.Into(ctx, f), app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: map[bool]string{true: "delegated", false: "asked"}[delegated]}.Build())
		require.NoError(t, err)
		lines, err := b.Ungated.Audit().List(ctx, app.AuditListRequest_builder{Filters: []*app.AuditFilter{app.AuditFilter_builder{ObjectId: row.GetId()}.Build()}}.Build())
		require.NoError(t, err)
		require.Len(t, lines.GetItems(), 1)
		line := lines.GetItems()[0]
		require.Equal(t, b.Holder.Bytes(), line.GetActorId())
		require.Equal(t, agent.Bytes(), line.GetCallerId())
		require.Equal(t, b.Tenant.Bytes(), line.GetCallerTenantId())
		require.Equal(t, "delegation-a", line.GetPresence())
	}
}

func TestDirectMethodRefusesADelegatedCall(t *testing.T) {
	b, ctx := build(t)
	f := frame.New(b.Holder, b.Tenant, frame.Whole()).WithScope(frame.Only(b.Tenant))
	f.Caller = &frame.Principal{Actor: pdid.New(pd.HolderDomain), Tenant: b.Tenant}
	f.Delegated = true
	ctx = frame.Into(ctx, f)
	_, err := gate.Decide(ctx, nil, app.RobotService_Move_FullMethodName)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = b.Walled.Robot().Move(ctx, app.RobotMoveRequest_builder{}.Build())
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
