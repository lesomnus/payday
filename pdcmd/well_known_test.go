package pdcmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// These objects deliberately contain keys from the well-known descriptors too.
// Treating free-form JSON as protobuf fields must not remove or reinterpret them.
func mappedMessages(t *testing.T) map[string]proto.Message {
	t.Helper()
	s, err := structpb.NewStruct(map[string]any{"a": 1, "fields": map[string]any{"value": "kept"}, "values": []any{true, nil, "x"}})
	require.NoError(t, err)
	list, err := structpb.NewList([]any{true, "hello", nil, map[string]any{"b": 2}})
	require.NoError(t, err)
	pack := func(v proto.Message) *anypb.Any { out, err := anypb.New(v); require.NoError(t, err); return out }
	return map[string]proto.Message{
		"struct": s, "object": structpb.NewStructValue(s), "string": structpb.NewStringValue("hello"), "number": structpb.NewNumberValue(1.25), "null": structpb.NewNullValue(), "boolean": structpb.NewBoolValue(false), "array": structpb.NewListValue(list), "list": list,
		"any_struct": pack(s), "any_wrapper": pack(wrapperspb.String("packed")), "any_message": pack(&descriptorpb.FileDescriptorProto{Name: proto.String("kept.proto")}),
		"bool_wrapper": wrapperspb.Bool(false), "string_wrapper": wrapperspb.String(""), "bytes_wrapper": wrapperspb.Bytes([]byte{0, 1, 2}), "int32_wrapper": wrapperspb.Int32(-1), "int64_wrapper": wrapperspb.Int64(1 << 62), "uint32_wrapper": wrapperspb.UInt32(1 << 31), "uint64_wrapper": wrapperspb.UInt64(1 << 63), "float_wrapper": wrapperspb.Float(1.5), "double_wrapper": wrapperspb.Double(2.5),
		"timestamp": timestamppb.New(time.Date(2026, 11, 21, 12, 30, 0, 0, time.UTC)), "duration": durationpb.New(1500 * time.Millisecond), "mask": &fieldmaskpb.FieldMask{Paths: []string{"foo_bar", "nested.field"}},
	}
}

func wrapMapped(t *testing.T, v proto.Message, shape string) proto.Message {
	t.Helper()
	md := v.ProtoReflect().Descriptor()
	typename := "." + string(md.FullName())
	field := func(name string, n int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(n), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(typename)}
	}
	one := field("payload", 1)
	many := field("copies", 2)
	many.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	mapped := field("mapped", 3)
	mapped.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	mapped.TypeName = proto.String(".mapped.Envelope.MappedEntry")
	value := field("value", 2)
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{Name: proto.String("mapped.proto"), Package: proto.String("mapped"), Syntax: proto.String("proto3"), Dependency: []string{md.ParentFile().Path()}, MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Envelope"), Field: []*descriptorpb.FieldDescriptorProto{one, many, mapped}, NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("MappedEntry"), Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}, Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("key"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}, value}}}}}}, protoregistry.GlobalFiles)
	require.NoError(t, err)
	out := dynamicpb.NewMessage(file.Messages().Get(0))
	f := out.Descriptor().Fields().ByName(protoreflect.Name(shape))
	switch shape {
	case "payload":
		out.Set(f, protoreflect.ValueOfMessage(v.ProtoReflect()))
	case "copies":
		out.Mutable(f).List().Append(protoreflect.ValueOfMessage(v.ProtoReflect()))
	case "mapped":
		out.Mutable(f).Map().Set(protoreflect.ValueOfString("a").MapKey(), protoreflect.ValueOfMessage(v.ProtoReflect()))
	}
	return out
}

func TestJsonMappedWellKnownTypes(t *testing.T) {
	for name, v := range mappedMessages(t) {
		t.Run(name, func(t *testing.T) {
			for _, shape := range []string{"root", "payload", "copies", "mapped"} {
				t.Run(shape, func(t *testing.T) {
					m := v
					if shape != "root" {
						m = wrapMapped(t, v, shape)
					}
					want, err := protojson.Marshal(m)
					require.NoError(t, err)
					var out bytes.Buffer
					require.NoError(t, Json.Print(&out, m))
					require.JSONEq(t, string(want), out.String())
				})
			}
		})
	}
}

func TestPrettyMappedWellKnownTypes(t *testing.T) {
	for name, v := range mappedMessages(t) {
		t.Run(name, func(t *testing.T) {
			want, err := protojson.Marshal(v)
			require.NoError(t, err)
			for _, shape := range []string{"root", "payload", "copies", "mapped"} {
				t.Run(shape, func(t *testing.T) {
					// Existing singular timestamp/duration formatting stays human-readable.
					if shape == "payload" && (name == "timestamp" || name == "duration") {
						return
					}
					m := v
					if shape != "root" {
						m = wrapMapped(t, v, shape)
					}
					var out bytes.Buffer
					require.NoError(t, Pretty.Print(&out, m))
					got := strings.TrimSpace(out.String())
					switch shape {
					case "payload":
						got = strings.TrimSpace(strings.TrimPrefix(got, "payload"))
					case "copies":
						_, got, _ = strings.Cut(got, "\n")
					case "mapped":
						_, got, _ = strings.Cut(got, "\n")
						got = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(got), "a:"))
					}
					require.JSONEq(t, string(want), strings.TrimSpace(got))
				})
			}
		})
	}
}

func TestMappedAnyStillReportsUnresolvablePayload(t *testing.T) {
	v := &anypb.Any{TypeUrl: "type.googleapis.com/missing.Message", Value: []byte{1}}
	for _, p := range []Printer{Json, Pretty} {
		var out bytes.Buffer
		require.Error(t, p.Print(&out, v))
	}
}
