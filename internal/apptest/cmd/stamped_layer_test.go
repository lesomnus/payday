package cmd_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A real app layer supplies a scalar and a timestamp after the caller's
// request passes the generated gate. AddMany must keep that same boundary.
type stampedLayer struct {
	app.Overlay
	calls *atomic.Int32
}

func (s stampedLayer) WithDriver(drv dialect.Driver) (app.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}
	return stampedLayer{app.NewOverlay(next), s.calls}, nil
}

func (s stampedLayer) Robot() app.RobotServiceServer {
	return bare.RobotWithAddRule(s.Next().Robot(), func(_ context.Context, req *app.RobotAddRequest) (*app.RobotAddRequest, error) {
		s.calls.Add(1)
		v := proto.Clone(req).(*app.RobotAddRequest)
		v.SetDateAttested(timestamppb.New(time.Unix(1700000000, 0)))
		return v, nil
	})
}

func (s stampedLayer) Holder() app.HolderServiceServer {
	return bare.HolderWithAddRule(s.Next().Holder(), func(_ context.Context, req *app.HolderAddRequest) (*app.HolderAddRequest, error) {
		s.calls.Add(1)
		v := proto.Clone(req).(*app.HolderAddRequest)
		v.SetDirectory("server-issued")
		return v, nil
	})
}

func TestAppLayerFillsStampedAddFieldsBehindTheGate(t *testing.T) {
	b, ctx := build(t)
	var calls atomic.Int32
	sink, err := pd.NewSink(b.Ent, bare.WithMinter(pd.Minter()), bare.WithScope(pd.Wall()))
	require.NoError(t, err)
	fill := app.BuilderFunc(func(next app.Server) (app.Server, error) {
		return stampedLayer{app.NewOverlay(next), &calls}, nil
	})
	b.Walled, err = app.Build(sink, fill, pd.GateBuild())
	require.NoError(t, err)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)
	robots := app.NewRobotServiceClient(conn)
	holders := app.NewHolderServiceClient(conn)
	tenant := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()
	req := func(alias string) *app.RobotAddRequest {
		return app.RobotAddRequest_builder{Tenant: tenant, Alias: alias}.Build()
	}
	forged := req("forged")
	forged.SetDateAttested(timestamppb.Now())
	_, err = robots.Add(as, forged)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = holders.Add(as, app.HolderAddRequest_builder{Tenant: tenant, Alias: "forged", Directory: "caller-issued"}.Build())
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Zero(t, calls.Load(), "caller input must be refused before the app layer can overwrite it")

	v, err := robots.Add(as, req("stamped"))
	require.NoError(t, err)
	require.Equal(t, int64(1700000000), v.GetDateAttested().GetSeconds())
	h, err := holders.Add(as, app.HolderAddRequest_builder{Tenant: tenant, Alias: "stamped"}.Build())
	require.NoError(t, err)
	require.Equal(t, "server-issued", h.GetDirectory())
	before := calls.Load()
	_, err = robots.AddMany(as, app.RobotAddManyRequest_builder{Items: []*app.RobotAddRequest{req("not-written"), forged}}.Build())
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, before, calls.Load(), "all items are admitted before the layer runs")
	vs, err := robots.List(as, app.RobotListRequest_builder{}.Build())
	require.NoError(t, err)
	require.Len(t, vs.GetItems(), 1, "a refused batch writes nothing")
	batch, err := robots.AddMany(as, app.RobotAddManyRequest_builder{Items: []*app.RobotAddRequest{req("batch-a"), req("batch-b")}}.Build())
	require.NoError(t, err)
	require.Len(t, batch.GetItems(), 2)
	for _, row := range batch.GetItems() {
		require.Equal(t, int64(1700000000), row.GetDateAttested().GetSeconds())
	}
}
