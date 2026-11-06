package server_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	app "github.com/lesomnus/payday/internal/apptest"
	entmigrate "github.com/lesomnus/payday/internal/apptest/internal/ent/migrate"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The computed value is a projection of an ordinary authorized read. Its id is
// the parent's opaque id, not a new domain or a lookup in a second table.
type computedReadings struct {
	app.UnimplementedComputedReadingServiceServer
	parents app.RobotServiceServer
	changed <-chan struct{}
}

func (s *computedReadings) Get(ctx context.Context, req *app.ComputedReadingGetRequest) (*app.ComputedReading, error) {
	row, err := s.parents.Get(ctx, app.RobotGetRequest_builder{Ref: app.RobotRef_builder{Id: req.GetRef().GetId()}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	return app.ComputedReading_builder{Id: row.GetId(), Value: "computed:" + row.GetAlias()}.Build(), nil
}

func (s *computedReadings) List(ctx context.Context, req *app.ComputedReadingListRequest) (*app.ComputedReadingListResponse, error) {
	if len(req.GetFilters()) != 1 || req.GetFilters()[0].GetRobot() == nil {
		return nil, status.Error(codes.InvalidArgument, "one parent scope is required")
	}
	value, err := s.Get(ctx, app.ComputedReadingGetRequest_builder{Ref: app.ComputedReadingRef_builder{Id: req.GetFilters()[0].GetRobot().GetId()}.Build()}.Build())
	if status.Code(err) == codes.NotFound {
		return app.ComputedReadingListResponse_builder{Metadata: app.ComputedReadingSnapshot_builder{Revision: "absent", Requested: 1}.Build()}.Build(), nil
	}
	if err != nil {
		return nil, err
	}
	return app.ComputedReadingListResponse_builder{Items: []*app.ComputedReading{value}, Metadata: app.ComputedReadingSnapshot_builder{Revision: "present", Requested: 1}.Build()}.Build(), nil
}

func (s *computedReadings) Watch(req *app.ComputedReadingWatchRequest, stream grpc.ServerStreamingServer[app.ComputedReadingWatchResponse]) error {
	for i := 0; i < 2; i++ {
		if i > 0 {
			select {
			case <-s.changed:
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}
		// Each snapshot goes through current parent authorization, rather than
		// continuing to serve an earlier cached value after that row is gone.
		values, err := s.List(stream.Context(), app.ComputedReadingListRequest_builder{Filters: req.GetFilters()}.Build())
		if err != nil {
			return err
		}
		if err := stream.Send(app.ComputedReadingWatchResponse_builder{Items: values.GetItems(), Metadata: values.GetMetadata()}.Build()); err != nil {
			return err
		}
	}
	return nil
}

type scopedReadStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s scopedReadStream) Context() context.Context { return s.ctx }

func TestComputedReadResourceUsesGeneratedContractAndAuthorizedParentWithoutTable(t *testing.T) {
	a := New(t)
	x := require.New(t)
	ctx := t.Context()
	tenant := a.tenantOf(ctx, x, "reading-owner")
	other := a.tenantOf(ctx, x, "reading-other")
	parent := a.robotOf(ctx, x, tenant, "source")
	foreign := a.robotOf(ctx, x, other, "foreign")
	changed := make(chan struct{})
	projection := &computedReadings{parents: a.Walled.Robot(), changed: changed}
	combined := app.ComposeServer(a.Walled, app.StaticResourceServer{ComputedReadingServer: projection})
	sink, ok := app.SinkOf(combined).(bare.Server)
	x.True(ok, "composition preserves traversal to the original middleware sink")
	x.Same(a.Db, sink.Db)
	found, ok := app.Find[bare.Server](combined)
	x.True(ok, "capabilities remain discoverable through the composed server")
	x.Same(a.Db, found.Db)
	g := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			return next(As(ctx, tenant), req)
		}),
		grpc.StreamInterceptor(func(server any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			return next(server, scopedReadStream{stream, As(stream.Context(), tenant)})
		}),
	)
	app.RegisterServerWithResources(g, combined)
	descriptor := app.ComputedReadingService_ServiceDesc
	x.Len(descriptor.Methods, 2, "only Get and List are unary reads")
	x.Equal("Get", descriptor.Methods[0].MethodName)
	x.Equal("List", descriptor.Methods[1].MethodName)
	x.Len(descriptor.Streams, 1)
	x.Equal("Watch", descriptor.Streams[0].StreamName)
	x.True(descriptor.Streams[0].ServerStreams)
	x.False(descriptor.Streams[0].ClientStreams)
	conn := pdtest.Serve(t, g)
	client := app.NewComputedReadingServiceClient(conn)
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	value, err := client.Get(call, app.ComputedReadingGetRequest_builder{Ref: app.ComputedReadingRef_builder{Id: parent.Bytes()}.Build()}.Build())
	x.NoError(err)
	x.Equal("computed:source", value.GetValue())
	x.Equal(parent.Bytes(), value.GetId())
	_, err = client.Get(call, app.ComputedReadingGetRequest_builder{Ref: app.ComputedReadingRef_builder{Id: foreign.Bytes()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err), "a computed service cannot bypass the parent's wall")
	filters := []*app.ComputedReadingFilter{app.ComputedReadingFilter_builder{Robot: app.RobotRef_builder{Id: parent.Bytes()}.Build()}.Build()}
	page, err := client.List(call, app.ComputedReadingListRequest_builder{Filters: filters, Size: 1}.Build())
	x.NoError(err)
	x.Len(page.GetItems(), 1)
	x.Equal("present", page.GetMetadata().GetRevision())
	stream, err := client.Watch(call, app.ComputedReadingWatchRequest_builder{Filters: filters, Revision: "older"}.Build())
	x.NoError(err)
	first, err := stream.Recv()
	x.NoError(err)
	x.Len(first.GetItems(), 1)
	_, err = a.Ungated.Robot().Erase(ctx, app.RobotRef_builder{Id: parent.Bytes()}.Build())
	x.NoError(err)
	close(changed)
	last, err := stream.Recv()
	x.NoError(err)
	x.Empty(last.GetItems(), "an empty collection replaces the previous snapshot")
	x.Equal("absent", last.GetMetadata().GetRevision())
	x.EqualValues(1, last.GetMetadata().GetRequested(), "metadata survives an empty collection")
	_, err = stream.Recv()
	x.Equal(io.EOF, err)
	// The existing generated CRUD remains registered on the same composed server.
	row, err := app.NewRobotServiceClient(conn).Get(call, app.RobotGetRequest_builder{Ref: app.RobotRef_builder{Id: foreign.Bytes()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
	x.Nil(row)
	for _, table := range entmigrate.Tables {
		x.NotContains(strings.ToLower(table.Name), "computed", "read resources must not enter Ent migrations")
	}
	query := "SELECT count(*) FROM sqlite_master WHERE type='table' AND lower(name) LIKE '%computed%'"
	if a.Dialect == "postgres" {
		query = "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND lower(table_name) LIKE '%computed%'"
	}
	var tables int
	x.NoError(a.SQL.QueryRowContext(ctx, query).Scan(&tables))
	x.Zero(tables, "the actual migrated database has no computed resource table")
	x.Equal("computed-reading", app.ReadResources["app.ComputedReading"])
	_, registered := pdid.Lookup("app.ComputedReading")
	x.False(registered, "a read resource must not invent an identifier domain")
}

func TestComputedReadResourceDefaultImplementationStaysClosed(t *testing.T) {
	g := grpc.NewServer()
	app.RegisterServerWithResources(g, app.ComposeServer(app.UnimplementedServer{}, app.StaticResourceServer{}))
	conn := pdtest.Serve(t, g)
	_, err := app.NewComputedReadingServiceClient(conn).Get(t.Context(), &app.ComputedReadingGetRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err))
}
