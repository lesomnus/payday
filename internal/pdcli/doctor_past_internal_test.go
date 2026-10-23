package pdcli

import (
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/lesomnus/payday/internal/pdgen"
)

// schemaOf is a schema written inline, read as the doctor reads an app's --
// descriptors handed to the generator's reader -- and compiled in memory, its
// imports out of what this binary links, rather than by buf: what a case is
// about is the reading, and a schema no app of this repository has.
func schemaOf(t *testing.T, src string) *pdgen.Schema {
	t.Helper()
	x := require.New(t)

	const name = "app/schema.proto"
	c := protocompile.Compiler{
		Resolver: protocompile.CompositeResolver{
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{name: src}),
			},
			protocompile.ResolverFunc(func(p string) (protocompile.SearchResult, error) {
				fd, err := protoregistry.GlobalFiles.FindFileByPath(p)
				if err != nil {
					return protocompile.SearchResult{}, err
				}

				return protocompile.SearchResult{Desc: fd}, nil
			}),
		},
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	fs, err := c.Compile(t.Context(), name)
	x.NoError(err)

	// Every file, its imports before it, and each through its wire form: the
	// options come back as the types this binary links, which is what the
	// reader asks for.
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var add func(fd protoreflect.FileDescriptor)
	add = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true

		is := fd.Imports()
		for i := range is.Len() {
			add(is.Get(i).FileDescriptor)
		}

		b, err := proto.Marshal(protodesc.ToFileDescriptorProto(fd))
		x.NoError(err)
		v := &descriptorpb.FileDescriptorProto{}
		x.NoError(proto.Unmarshal(b, v))
		set.File = append(set.File, v)
	}
	for _, f := range fs {
		add(f)
	}

	s, err := read(set, []string{name})
	x.NoError(err)

	return s
}

// booking is a global entity with a column named for an event, and `option`
// above it.
func booking(option string) string {
	return `edition = "2023";

package app;

import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "example.com/app";
` + option + `
message Booking {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  bytes event_id = 8 [(orm.field) = {type: TYPE_UUID}];

  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 7, global: {}, erase: {hard: {}}};
}
`
}

// TestAnAppWithNoPastIsNotAskedAboutItsColumns: a column named for an event is
// asked about in an app that keeps a past, whose expiry asks only the columns
// that say they name one. An app that keeps none has no event for one to
// name, and `pd gen` refuses the declaration in it: its `event_id` is its own
// business.
func TestAnAppWithNoPastIsNotAskedAboutItsColumns(t *testing.T) {
	x := require.New(t)

	s := schemaOf(t, booking(""))
	x.Nil(s.Past)
	x.Empty(doctorPast(Layout{}, s))

	s = schemaOf(t, booking("option (payday.app) = {past: {event: 20, fact: 21}};\n"))
	x.NotNil(s.Past)
	vs := doctorPast(Layout{}, s)
	x.Len(vs, 1)
	x.Contains(vs[0].What, "app.Booking says nothing of event_id, which is named for an event")
}
