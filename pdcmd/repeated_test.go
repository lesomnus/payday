package pdcmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// reaches is an answer of the shape `roster holder reaches` has: lists of
// identifiers, said so on the field, beside a list of bytes that are none, a
// list that is secret, and a map whose values are identifiers.
func reaches(t *testing.T) protoreflect.Message {
	t.Helper()

	uuid := func() *descriptorpb.FieldOptions {
		o := &descriptorpb.FieldOptions{}
		proto.SetExtension(o, ormpb.E_Field, ormpb.FieldOptions_builder{Type: ormpb.Type_TYPE_UUID.Enum()}.Build())
		return o
	}
	secret := func() *descriptorpb.FieldOptions {
		o := &descriptorpb.FieldOptions{}
		proto.SetExtension(o, pdpb.E_Field, pdpb.Field_builder{Secret: true}.Build())
		return o
	}
	field := func(name string, n int32, repeated bool, opts *descriptorpb.FieldOptions) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(n), Type: descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum(), Options: opts}
		if repeated {
			f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		}
		return f
	}
	owners := field("owners", 5, true, uuid())
	owners.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	owners.TypeName = proto.String(".reach.Reaches.OwnersEntry")

	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("reach.proto"), Package: proto.String("reach"), Syntax: proto.String("proto3"),
		Dependency: []string{"orm.proto", "payday.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Reaches"),
			Field: []*descriptorpb.FieldDescriptorProto{
				field("sites", 1, true, uuid()),
				field("blobs", 2, true, nil),
				field("previous", 3, true, secret()),
				field("site_id", 4, false, nil),
				owners,
				field("key", 6, false, secret()),
			},
			NestedType: []*descriptorpb.DescriptorProto{{
				Name:    proto.String("OwnersEntry"),
				Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("key"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					{Name: proto.String("value"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum()},
				},
			}},
		}},
	}, protoregistry.GlobalFiles)
	require.NoError(t, err)

	m := dynamicpb.NewMessage(fd.Messages().Get(0))
	fs := m.Descriptor().Fields()
	set := func(name string, vs ...[]byte) {
		l := m.Mutable(fs.ByName(protoreflect.Name(name))).List()
		for _, v := range vs {
			l.Append(protoreflect.ValueOfBytes(v))
		}
	}
	set("sites", siteA.Bytes(), siteB.Bytes())
	set("blobs", []byte{1, 162, 18}, []byte{16, 97})
	set("previous", []byte("hunter2"), []byte("hunter3"))
	m.Set(fs.ByName("site_id"), protoreflect.ValueOfBytes(siteA.Bytes()))
	m.Set(fs.ByName("key"), protoreflect.ValueOfBytes([]byte("sesame")))
	m.Mutable(fs.ByName("owners")).Map().Set(protoreflect.ValueOfString("north").MapKey(), protoreflect.ValueOfBytes(siteB.Bytes()))

	return m
}

var siteA, siteB = pdid.New(1), pdid.New(2)

// TestAListOfIdentifiersIsPrintedAsIdentifiers: the pretty printer printed
// every element of a scalar list as Go prints it, so a list of identifiers
// was `[1 162 18 16 97 41 ...]`, element by element, and a list that was
// secret was in the terminal's scrollback. An element is printed as a single
// field of its kind is: an identifier where the field says it is one
// (`(orm.field) = {type: TYPE_UUID}`), `(n bytes)` where it does not, and a
// field that is secret is not printed element by element at all. A map's
// bytes values the same. The table and wide printers print no list or map,
// and so never Go's rendering of one.
func TestAListOfIdentifiersIsPrintedAsIdentifiers(t *testing.T) {
	x := require.New(t)
	m := reaches(t)

	var w bytes.Buffer
	x.NoError(pretty(&w, m, "", nil))
	out := w.String()
	t.Log(out)

	x.Contains(out, "sites    (2)\n  "+siteA.String()+"\n  "+siteB.String()+"\n")
	x.Contains(out, "blobs    (2)\n  (3 bytes)\n  (2 bytes)\n")
	x.Contains(out, "previous (2 values)\n")
	x.Contains(out, "site_id  "+siteA.String()+"\n", "a single identifier, by the rule of names")
	x.Contains(out, "owners   \n  north: "+siteB.String()+"\n")
	x.Contains(out, "key      (6 bytes)\n", "a single secret, as before")
	x.NotContains(out, "hunter")
	x.NotContains(out, "[", "Go's rendering of a slice")

	for _, name := range []string{"table", "wide"} {
		p, err := builtinAs(name, true)
		x.NoError(err)
		var w bytes.Buffer
		x.NoError(p.Print(&w, m.Interface()))
		x.NotContains(w.String(), "[", name)
		x.NotContains(w.String(), "hunter", name)
		x.False(strings.Contains(w.String(), "c2VzYW1l"), "%s: a secret as base64", name)
	}
}
