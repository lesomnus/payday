package grpcx

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// AsMethod answers with `ctx` as a call to `method` is served in: one in which
// gRPC's own question -- what method is being served? -- answers with
// `method`, whatever the call on the wire was.
//
// It is for a call served as another. What is below the handler asks gRPC
// which method the caller asked for, because nothing else knows it -- one call
// writes through several servers, and only the transport knows the name it
// all happened under: the trail files a write under it, and the generated
// gate holds an account's frame to the methods the schema made for it by it.
// A handler that answers a method of its own API by calling the schema's
// hands them the schema's method this way; and a test calling a server
// directly, the method a caller over the wire would have asked for.
//
// What the handler says back goes to the call on the wire: SetHeader,
// SendHeader and SetTrailer are handed to the stream `ctx` already had, the
// only place a header can reach the caller, rather than absorbed. With none --
// a handler called directly -- they answer an error rather than drop what
// they were given, which is what gRPC answers a handler called so.
// `grpc.SetSendCompressor` and `grpc.ClientSupportedCompressors` reach past
// the interface for gRPC's own stream, which no wrapper is, and answer that
// they find none.
func AsMethod(ctx context.Context, method string) context.Context {
	return grpc.NewContextWithServerTransportStream(ctx, servedAs{
		outer:  grpc.ServerTransportStreamFromContext(ctx),
		method: method,
	})
}

// servedAs is a [grpc.ServerTransportStream] that answers with the method a
// call is served as, and hands everything else it is asked to the stream the
// call arrived on, or `outer`, which is nil for a handler called without one.
type servedAs struct {
	outer  grpc.ServerTransportStream
	method string
}

// errNoTransport is metadata with no call on the wire to ride on.
var errNoTransport = status.Error(codes.Internal, "grpcx: there is no transport stream to carry it")

func (s servedAs) Method() string { return s.method }

func (s servedAs) SetHeader(md metadata.MD) error {
	if s.outer == nil {
		return errNoTransport
	}

	return s.outer.SetHeader(md)
}

func (s servedAs) SendHeader(md metadata.MD) error {
	if s.outer == nil {
		return errNoTransport
	}

	return s.outer.SendHeader(md)
}

func (s servedAs) SetTrailer(md metadata.MD) error {
	if s.outer == nil {
		return errNoTransport
	}

	return s.outer.SetTrailer(md)
}
