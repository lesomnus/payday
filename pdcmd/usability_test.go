package pdcmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// This is the generated Ref contract, including scalar and composite indices.
// The unusual field numbers keep the shorthand dependent on names and types.
const usabilitySchema = `syntax="proto3"; package reftest;
import "payday.proto";
import "orm.proto";
import "google/protobuf/struct.proto";
message TenantRef { oneof key { bytes id=1; string alias=4; } }
message ThingRef { oneof key { bytes id=1; Number number=8; Slug slug=9; string serial_number=10; Slot slot=11; } string note=12; }
message Number { string number=3; }
message Slug { TenantRef tenant=1; string alias=2; }
message Slot { TenantRef tenant=1; uint64 number=3; }
message Input { ThingRef ref=1; repeated ThingRef refs=2; map<string,ThingRef> mapping=3; google.protobuf.Struct data=4; string number=5; bool active=6; uint64 serial=7; Number ordinary=8; }
message Plain { string result=1; uint64 count=2; bool ready=3; string token=4 [(payday.field).secret=true]; }
message PlainList { repeated Plain results=1; string next=2; }
message Ambiguous { repeated Plain first=1; repeated Plain second=2; string next=3; }
message Thing {
 option (payday.entity)={domain:201 global:{} columns:[{field:"serial" title:"NUMBER"},{field:"detail.result"},{field:"id" wide:true}]};
 bytes id=1 [(orm.field).type=TYPE_UUID];
 string serial=9;
 Plain detail=10;
}
message WatchItem { bytes id=1; Thing value=2; string action=3; }
service Lookup { rpc Find(Input) returns(Plain); }
`

func usabilityFile(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	c := protocompile.Compiler{Resolver: protocompile.CompositeResolver{
		&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(map[string]string{"usability.proto": usabilitySchema})},
		protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
			d, err := protoregistry.GlobalFiles.FindFileByPath(path)
			return protocompile.SearchResult{Desc: d}, err
		}),
	}}
	files, err := c.Compile(t.Context(), "usability.proto")
	require.NoError(t, err)
	raw, err := proto.Marshal(protodesc.ToFileDescriptorProto(files[0]))
	require.NoError(t, err)
	var fp descriptorpb.FileDescriptorProto
	require.NoError(t, proto.Unmarshal(raw, &fp))
	file, err := protodesc.NewFile(&fp, protoregistry.GlobalFiles)
	require.NoError(t, err)
	return file
}

func TestUniqueReferencesAndNestedShorthand(t *testing.T) {
	file := usabilityFile(t)
	refdesc := file.Messages().ByName("ThingRef")
	for text, want := range map[string]string{
		"number=MOTOR-0042": `{"number":{"number":"MOTOR-0042"}}`,
		"number=0042":       `{"number":{"number":"0042"}}`,
		"serialNumber=a=b":  `{"serialNumber":"a=b"}`,
		`slot={"tenant":"@acme","number":18446744073709551615}`: `{"slot":{"tenant":{"alias":"acme"},"number":"18446744073709551615"}}`,
	} {
		t.Run(text, func(t *testing.T) {
			ref, err := RefParser{}.Parse(text)
			require.NoError(t, err)
			require.Equal(t, text, RefParser{}.ToString(ref))
			m := dynamicpb.NewMessage(refdesc)
			require.NoError(t, ref.Fill(m))
			raw, err := protojson.Marshal(m)
			require.NoError(t, err)
			require.JSONEq(t, want, string(raw))
		})
	}
	for _, text := range []string{"typo=hello", "note=not-a-key", `slot={"number":"wrong"}`, `number={"unknown":"value"}`} {
		ref, err := RefParser{}.Parse(text)
		require.NoError(t, err)
		require.Error(t, ref.Fill(dynamicpb.NewMessage(refdesc)), text)
	}
	id := pdid.New(201)
	other := pdid.New(202)
	for _, text := range []string{"id=" + id.String(), id.String()} {
		ref, err := RefParser{}.Parse(text)
		require.NoError(t, err)
		require.NoError(t, ref.Expect(201))
		require.Error(t, ref.Expect(202))
	}
	m := dynamicpb.NewMessage(file.Messages().ByName("Input"))
	raw := []byte(`{"ref":{"number":"0042"},"refs":["@acme/a#thing","number=b"],"mapping":{"first":"serialNumber=c"},"data":{"ref":"@other/x","number":"0042"},"serial":18446744073709551615}`)
	require.NoError(t, unmarshalJson(raw, m, false))
	encoded, err := protojson.Marshal(m)
	require.NoError(t, err)
	require.JSONEq(t, `{"ref":{"number":{"number":"0042"}},"refs":[{"slug":{"tenant":{"alias":"acme"},"alias":"a"}},{"number":{"number":"b"}}],"mapping":{"first":{"serialNumber":"c"}},"data":{"ref":"@other/x","number":"0042"},"serial":"18446744073709551615"}`, string(encoded))
	for _, bad := range []string{`{"ref":"@acme/a#other"}`, fmt.Sprintf(`{"ref":%q}`, other.String()), `{"ordinary":"not-a-ref"}`, `{"ref":"bad-key"}`, `{} {}`} {
		require.Error(t, unmarshalJson([]byte(bad), m, false), bad)
	}
	require.Error(t, unmarshalJson(raw, m, true), "strict protojson has no shorthand")
}

func TestColumnsAndTemplateUseTheDeclaredContract(t *testing.T) {
	file := usabilityFile(t)
	id := pdid.New(201)
	m := dynamicpb.NewMessage(file.Messages().ByName("Thing"))
	require.NoError(t, unmarshalJson([]byte(fmt.Sprintf(`{"id":%q,"serial":"0042","detail":{"result":"done"}}`, id.String())), m, false))
	for format, want := range map[string]string{"table": "NUMBER DETAIL.RESULT 0042 done", "wide": "NUMBER DETAIL.RESULT ID 0042 done " + id.String(), "template={{.id}}": id.String()} {
		printer, err := PrinterNamed(format)
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, printer.Print(&out, m))
		require.Equal(t, want, strings.Join(strings.Fields(out.String()), " "))
	}
	watch := dynamicpb.NewMessage(file.Messages().ByName("WatchItem"))
	require.NoError(t, unmarshalJson([]byte(fmt.Sprintf(`{"id":%q,"value":{"id":%q,"serial":"0042"},"action":"Patch"}`, id.String(), id.String())), watch, false))
	var watched bytes.Buffer
	require.NoError(t, Table(true).Print(&watched, watch))
	require.Equal(t, "ACTION NUMBER DETAIL.RESULT ID Patch 0042 - "+id.String(), strings.Join(strings.Fields(watched.String()), " "))
	require.NoError(t, unmarshalJson([]byte(fmt.Sprintf(`{"id":%q,"action":"Erase"}`, id.String())), watch, false))
	watched.Reset()
	require.NoError(t, Table(true).Print(&watched, watch))
	require.Equal(t, "ACTION NUMBER DETAIL.RESULT ID Erase - - "+id.String(), strings.Join(strings.Fields(watched.String()), " "))

	require.NoError(t, unmarshalJson([]byte(`{"serial":"x"}`), m, false))
	var out bytes.Buffer
	require.NoError(t, Table(false).Print(&out, m))
	require.Equal(t, "NUMBER DETAIL.RESULT x -", strings.Join(strings.Fields(out.String()), " "))
	for typename, tc := range map[string][2]string{
		"Plain":     {`{"result":"done","token":"hidden"}`, "RESULT COUNT READY done 0 false"},
		"PlainList": {`{"results":[{"result":"done"},{"result":"queued","count":"2","ready":true}],"next":"cursor"}`, "RESULT COUNT READY done 0 false queued 2 true"},
		"Ambiguous": {`{"first":[{"result":"a"}],"second":[{"result":"b"}],"next":"cursor"}`, "NEXT cursor"},
	} {
		m := dynamicpb.NewMessage(file.Messages().ByName(protoreflect.Name(typename)))
		require.NoError(t, protojson.Unmarshal([]byte(tc[0]), m))
		out.Reset()
		require.NoError(t, Table(false).Print(&out, m))
		require.Equal(t, tc[1], strings.Join(strings.Fields(out.String()), " "))
	}
}

// A connection that captures the actual CLI request tests argument parsing,
// optional JSON bodies and merge precedence without teaching a fake server
// how to resolve a key (the generated app tests cover that server path).
type unaryCapture struct {
	grpc.ClientConnInterface
	input proto.Message
}

func (c *unaryCapture) Invoke(ctx context.Context, method string, in, out any, opts ...grpc.CallOption) error {
	c.input = proto.Clone(in.(proto.Message))
	return nil
}
func TestUnaryPositionalOptions(t *testing.T) {
	file := usabilityFile(t)
	require.NoError(t, protoregistry.GlobalFiles.RegisterFile(file))
	for _, tc := range []struct {
		args []string
		opts []UnaryOption
		want string
		fail bool
	}{
		{args: []string{"number=0042"}, want: `{"ref":{"number":{"number":"0042"}}}`},
		{args: nil, fail: true},
		{opts: []UnaryOption{OptionalArg()}, want: `{}`},
		{args: []string{`{"number":"body"}`}, opts: []UnaryOption{OptionalArg()}, want: `{"number":"body"}`},
		{args: []string{"0042", `{"number":"body"}`}, opts: []UnaryOption{Arg("number")}, want: `{"number":"body"}`},
		{args: []string{"0042"}, opts: []UnaryOption{Arg("number")}, want: `{"number":"0042"}`},
		{args: []string{"true"}, opts: []UnaryOption{Arg("active")}, want: `{"active":true}`},
		{args: []string{"18446744073709551615"}, opts: []UnaryOption{Arg("serial")}, want: `{"serial":"18446744073709551615"}`},
	} {
		t.Run(fmt.Sprint(tc.args, tc.want), func(t *testing.T) {
			capture := &unaryCapture{}
			tree := &Tree{run: &runner{conn: Static(capture)}}
			command, err := tree.Unary("reftest.Lookup.Find", tc.opts...)
			require.NoError(t, err)
			got := xlitest.Harness{Cmd: &xli.Command{Name: "app", Commands: xli.Commands{command}}}.Run(t, append([]string{"find"}, tc.args...)...)
			if tc.fail {
				require.Error(t, got.Err)
				require.Nil(t, capture.input)
				return
			}
			require.NoError(t, got.Err)
			require.NotNil(t, capture.input)
			raw, err := protojson.Marshal(capture.input)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(raw))
		})
	}
	tree := &Tree{run: &runner{}}
	for _, opts := range [][]UnaryOption{{Arg("missing")}, {Arg("refs")}, {Arg("mapping")}, {Arg("")}, {Arg("number"), Arg("active")}, {OptionalArg(), OptionalArg()}} {
		_, err := tree.Unary("reftest.Lookup.Find", opts...)
		require.Error(t, err)
	}
}
