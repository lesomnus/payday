package pdcmd

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// TestJsonMap is the map fields `-o json` printed as `{}`: a map is a message
// field of its entry type, and walking the protojson object as that entry found
// neither `key` nor `value`.
func TestJsonMap(t *testing.T) {
	entry := func(name string, value *descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
		return &descriptorpb.DescriptorProto{
			Name: proto.String(name),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("key"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), JsonName: proto.String("key")},
				value,
			},
			Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
		}
	}
	value := func(t descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{Name: proto.String("value"), Number: proto.Int32(2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: t.Enum(), JsonName: proto.String("value")}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		return f
	}
	repeated := func(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(number),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(typeName),
			JsonName: proto.String(name),
		}
	}

	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("jsonmap_test.proto"),
		Package: proto.String("jsonmap"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Item"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("name"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), JsonName: proto.String("name")},
				},
			},
			{
				Name: proto.String("Thing"),
				Field: []*descriptorpb.FieldDescriptorProto{
					repeated("labels", 1, ".jsonmap.Thing.LabelsEntry"),
					repeated("items", 2, ".jsonmap.Thing.ItemsEntry"),
				},
				NestedType: []*descriptorpb.DescriptorProto{
					entry("LabelsEntry", value(descriptorpb.FieldDescriptorProto_TYPE_STRING, "")),
					entry("ItemsEntry", value(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".jsonmap.Item")),
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	thing := fd.Messages().ByName("Thing")
	item := fd.Messages().ByName("Item")

	m := dynamicpb.NewMessage(thing)
	labels := m.Mutable(thing.Fields().ByName("labels")).Map()
	labels.Set(protoreflect.ValueOfString("b").MapKey(), protoreflect.ValueOfString("2"))
	labels.Set(protoreflect.ValueOfString("a").MapKey(), protoreflect.ValueOfString("1"))

	i := dynamicpb.NewMessage(item)
	i.Set(item.Fields().ByName("name"), protoreflect.ValueOfString("x"))
	m.Mutable(thing.Fields().ByName("items")).Map().Set(protoreflect.ValueOfString("k").MapKey(), protoreflect.ValueOfMessage(i))

	b, err := jsonOf(m, "")
	require.NoError(t, err)
	require.Equal(t, `{
  "labels": {
    "a": "1",
    "b": "2"
  },
  "items": {
    "k": {
      "name": "x"
    }
  }
}`, string(b))
}
