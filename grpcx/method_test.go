package grpcx_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/grpcx"
)

// wire is the stream a call arrived on, remembering what it was handed.
type wire struct {
	set metadata.MD
}

func (w *wire) Method() string                 { return "/app.v1.RobotService/Add" }
func (w *wire) SetHeader(md metadata.MD) error { w.set = md; return nil }
func (w *wire) SendHeader(metadata.MD) error   { return nil }
func (w *wire) SetTrailer(metadata.MD) error   { return nil }

// TestACallServedAsAnotherAnswersWithThatMethod is the contract: what is below
// the handler asks gRPC which method is being served, and has no other way to
// ask.
func TestACallServedAsAnotherAnswersWithThatMethod(t *testing.T) {
	x := require.New(t)

	ctx := grpc.NewContextWithServerTransportStream(context.Background(), &wire{})
	ctx = grpcx.AsMethod(ctx, "/app.RobotService/Add")

	m, ok := grpc.Method(ctx)
	x.True(ok)
	x.Equal("/app.RobotService/Add", m)
}

// TestACallServedAsAnotherSendsItsMetadataOnTheWire pins the other choice: a
// header is handed to the call on the wire rather than absorbed, because its
// metadata is the only place one can reach the caller.
func TestACallServedAsAnotherSendsItsMetadataOnTheWire(t *testing.T) {
	x := require.New(t)

	w := &wire{}
	ctx := grpc.NewContextWithServerTransportStream(context.Background(), w)
	ctx = grpcx.AsMethod(ctx, "/app.RobotService/Add")

	md := metadata.Pairs("k", "v")
	x.NoError(grpc.SetHeader(ctx, md))
	x.Equal(md, w.set, "what the handler said reached the call on the wire")

	// And with no call on the wire at all -- a handler called directly --
	// there is nowhere for a header to go, and that is an answer rather than
	// a drop.
	ctx = grpcx.AsMethod(context.Background(), "/app.RobotService/Add")
	x.Error(grpc.SetHeader(ctx, md))

	m, ok := grpc.Method(ctx)
	x.True(ok, "the method still answers, which is what is below the handler asks")
	x.Equal("/app.RobotService/Add", m)
}
