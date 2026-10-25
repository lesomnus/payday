package schema_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	_ "google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/schema"
)

// The standalone contract is what a reviewer and client author read. Keep its
// public messages and RPCs equal to the source copied into an app, while leaving
// private storage records out of that wire-only document.
func TestNotificationWireContractMatchesWhatAppsReceive(t *testing.T) {
	x := require.New(t)
	preview, err := os.ReadFile("../docs/contracts/notify/notification.proto")
	x.NoError(err)
	compiler := protocompile.Compiler{Resolver: protocompile.CompositeResolver{
		&protocompile.SourceResolver{Accessor: func(name string) (io.ReadCloser, error) {
			if name == "preview.proto" {
				return io.NopCloser(strings.NewReader(string(preview))), nil
			}
			return schema.FS().Open(name)
		}},
		protocompile.ResolverFunc(func(name string) (protocompile.SearchResult, error) {
			file, err := protoregistry.GlobalFiles.FindFileByPath(name)
			return protocompile.SearchResult{Desc: file}, err
		}),
	}}
	files, err := compiler.Compile(context.Background(), "preview.proto", "notify/notification.proto")
	x.NoError(err)
	want, actual := files[0], files[1]
	for i := range want.Messages().Len() {
		message := want.Messages().Get(i)
		found := actual.Messages().ByName(message.Name())
		x.NotNil(found, "public message %s disappeared", message.Name())
		x.True(proto.Equal(normalized(protodesc.ToDescriptorProto(message), want.Package()), normalized(protodesc.ToDescriptorProto(found), actual.Package())), "public message %s differs from its contract", message.Name())
	}
	for i := range want.Services().Len() {
		service := want.Services().Get(i)
		found := actual.Services().ByName(service.Name())
		x.NotNil(found, "public service %s disappeared", service.Name())
		x.True(proto.Equal(normalized(protodesc.ToServiceDescriptorProto(service), want.Package()), normalized(protodesc.ToServiceDescriptorProto(found), actual.Package())), "service %s differs from its contract", service.Name())
	}
}

func normalized(message proto.Message, namespace protoreflect.FullName) proto.Message {
	clone := proto.Clone(message)
	var visit func(protoreflect.Message)
	visit = func(m protoreflect.Message) {
		m.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
			normalize := func(v protoreflect.Value) protoreflect.Value {
				if field.Kind() == protoreflect.MessageKind {
					visit(v.Message())
				} else if field.Kind() == protoreflect.StringKind {
					v = protoreflect.ValueOfString(strings.ReplaceAll(v.String(), "."+string(namespace)+".", ".contract."))
				}
				return v
			}
			if field.IsList() {
				for i := range value.List().Len() {
					value.List().Set(i, normalize(value.List().Get(i)))
				}
			} else {
				m.Set(field, normalize(value))
			}
			return true
		})
	}
	visit(clone.ProtoReflect())
	return clone
}
