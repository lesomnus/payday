package pdgen_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestFilterPreservesDeclaredUUIDType(t *testing.T) {
	source := entity("Part", `domain:7 global:{} list:{max:100 by:[{name:"subject"},{name:"alias"}]}`,
		`bytes subject=8 [(orm.field)={type:TYPE_UUID nullable:true}];`) + `message PartRef {bytes id=1;}`
	schema, err := read(t, source)
	require.NoError(t, err)
	overlay := pdgen.Proto(schema.Entities, "test")
	require.Contains(t, overlay, `import "orm.proto";`)
	require.Contains(t, overlay, `bytes subject = 1 [(orm.field) = {type: TYPE_UUID}];`)
	require.NotContains(t, overlay, "nullable:")
	require.Contains(t, overlay, "string alias = 2;")
	// Compile the generated overlay and prove an actual descriptor consumer sees
	// the semantic type. Merely matching generated text can miss an unused import
	// or an option that never reaches the compiled field.
	overlay = strings.Replace(overlay, "package test;", "package test;\nimport \"test/schema.proto\";", 1)
	c := protocompile.Compiler{Resolver: protocompile.CompositeResolver{
		&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(map[string]string{"test/schema.proto": inPkg("test", source), "test/filter.proto": overlay})},
		protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
			d, err := protoregistry.GlobalFiles.FindFileByPath(path)
			return protocompile.SearchResult{Desc: d}, err
		}),
	}}
	files, err := c.Compile(t.Context(), "test/filter.proto")
	require.NoError(t, err)
	registry := new(protoregistry.Files)
	protoregistry.GlobalFiles.RangeFiles(func(f protoreflect.FileDescriptor) bool { require.NoError(t, registry.RegisterFile(f)); return true })
	for i := 0; i < files[0].Imports().Len(); i++ {
		f := files[0].Imports().Get(i).FileDescriptor
		if f.Path() == "test/schema.proto" {
			require.NoError(t, registry.RegisterFile(f))
		}
	}
	compiled, err := protodesc.NewFile(rebuild(t, files[0]), registry)
	require.NoError(t, err)
	m := dynamicpb.NewMessage(compiled.Messages().ByName("PartFilter"))
	field := m.Descriptor().Fields().ByName("subject")
	opts := proto.GetExtension(field.Options(), ormpb.E_Field).(*ormpb.FieldOptions)
	require.Equal(t, ormpb.Type_TYPE_UUID, opts.GetType())
	id := pdid.New(7)
	m.Set(field, protoreflect.ValueOfBytes(id.Bytes()))
	var out bytes.Buffer
	require.NoError(t, pdcmd.Json.Print(&out, m))
	require.JSONEq(t, `{"subject":"`+id.String()+`"}`, out.String())
}
