package batch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/batch"
)

// outer is the stream the batch itself arrived on, remembering what it was
// handed.
type outer struct {
	set metadata.MD
}

func (o *outer) Method() string                 { return "/payday.BatchService/Do" }
func (o *outer) SetHeader(md metadata.MD) error { o.set = md; return nil }
func (o *outer) SendHeader(metadata.MD) error   { return nil }
func (o *outer) SetTrailer(metadata.MD) error   { return nil }

// TestAsOpAnswersWithTheOperation is the contract: inside the batch handler,
// gRPC's answer to "what method is being served" is the envelope's, and the
// recorder below the write sites has no other way to ask.
func TestAsOpAnswersWithTheOperation(t *testing.T) {
	x := require.New(t)

	ctx := grpc.NewContextWithServerTransportStream(context.Background(), &outer{})
	ctx = batch.AsOp(ctx, "/app.RobotService/Add")

	m, ok := grpc.Method(ctx)
	x.True(ok)
	x.Equal("/app.RobotService/Add", m)
}

// TestAsOpDelegatesMetadata pins the other choice: a header is handed to the
// wire call rather than absorbed, because the wire call's metadata is the only
// place one can reach the caller.
func TestAsOpDelegatesMetadata(t *testing.T) {
	x := require.New(t)

	o := &outer{}
	ctx := grpc.NewContextWithServerTransportStream(context.Background(), o)
	ctx = batch.AsOp(ctx, "/app.RobotService/Add")

	md := metadata.Pairs("k", "v")
	x.NoError(grpc.SetHeader(ctx, md))
	x.Equal(md, o.set, "what the operation said reached the wire call")

	// And with no wire call at all -- a handler called directly -- there is
	// nowhere for a header to go, and that is an answer rather than a drop.
	ctx = batch.AsOp(context.Background(), "/app.RobotService/Add")
	x.Error(grpc.SetHeader(ctx, md))

	m, ok := grpc.Method(ctx)
	x.True(ok, "the method still answers, which is what the trail needs")
	x.Equal("/app.RobotService/Add", m)
}
