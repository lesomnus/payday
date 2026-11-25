package grpcx

import (
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/mem"
	protov2 "google.golang.org/protobuf/proto"
)

// Unparsed begins what a server built with [Chain.ServerOptions] says of a
// request it could not unmarshal, before the message's full name: "payday: not
// a protobuf of app.RobotListRequest: " and then the protobuf library's
// reason.
//
// It is what tells such a refusal from any other, and payday's own words for
// that reason. grpc-go answers a request it cannot unmarshal `Internal`, which
// is a server failing, in a sentence of its own around the codec's error, and
// that sentence is one release's: "grpc: error unmarshalling request:" in 1.83,
// "grpc: failed to unmarshal the received message:" in 1.84. What wraps the
// codec's error changes; the codec's error is ours. So the transcoder payday
// serves HTTP through (`web`) finds this, wherever it is in the message, and
// answers 400 `invalid_argument`, the caller's.
const Unparsed = "payday: not a protobuf of "

// codec is grpc-go's own protobuf codec, with what it cannot unmarshal said
// as [Unparsed] says. Nothing else of it changes.
type codec struct{ encoding.CodecV2 }

func (c codec) Unmarshal(data mem.BufferSlice, v any) error {
	err := c.CodecV2.Unmarshal(data, v)
	if err == nil {
		return nil
	}

	name := fmt.Sprintf("%T", v)
	if m, ok := v.(protov2.Message); ok {
		name = string(m.ProtoReflect().Descriptor().FullName())
	}

	return fmt.Errorf("%s%s: %w", Unparsed, name, err)
}

// forceCodec is the server option that makes [codec] the server's.
func forceCodec() grpc.ServerOption {
	return grpc.ForceServerCodecV2(codec{encoding.GetCodecV2(proto.Name)})
}
