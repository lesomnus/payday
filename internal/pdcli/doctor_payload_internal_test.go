package pdcli

import (
	"strings"
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

// What `pd doctor` asks of an app's notification payloads (payday#193): of
// each string that says nothing about itself, and of a field named for
// somebody -- in the payload, and in what it holds.

// payloadsOf is what the doctor says of the payloads `names` of `src`.
func payloadsOf(t *testing.T, src string, names ...string) string {
	t.Helper()

	said := []string{}
	for _, v := range doctorPayloads(&pdgen.Schema{Payloads: payloadsOfSchema(t, src, names...)}) {
		said = append(said, v.String())
	}

	return strings.Join(said, "\n")
}

// payloadsOfSchema is the payloads `names` of `src`, compiled.
func payloadsOfSchema(t *testing.T, src string, names ...string) []*pdgen.Payload {
	t.Helper()
	x := require.New(t)

	const name = "app/payload.proto"
	c := protocompile.Compiler{
		Resolver: protocompile.CompositeResolver{
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(map[string]string{name: src})},
			protocompile.ResolverFunc(func(p string) (protocompile.SearchResult, error) {
				fd, err := protoregistry.GlobalFiles.FindFileByPath(p)
				return protocompile.SearchResult{Desc: fd}, err
			}),
		},
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	fs, err := c.Compile(t.Context(), name)
	x.NoError(err)

	// Through its wire form, so that the options come back as the types this
	// binary links, as they do from buf.
	b, err := proto.Marshal(protodesc.ToFileDescriptorProto(fs[0]))
	x.NoError(err)
	v := &descriptorpb.FileDescriptorProto{}
	x.NoError(proto.Unmarshal(b, v))
	fd, err := protodesc.NewFile(v, protoregistry.GlobalFiles)
	x.NoError(err)

	var vs []*pdgen.Payload
	for _, n := range names {
		md := fd.Messages().ByName(protoreflect.Name(n))
		x.NotNil(md, n)
		vs = append(vs, &pdgen.Payload{Desc: md})
	}

	return vs
}

const payloads = `edition = "2023";

package app;

import "google/protobuf/timestamp.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;

message JobDone {
  option (payday.notification) = {};

  bytes job = 1;
  bytes robot = 2;
  string outcome = 3 [(payday.field) = {personal: false}];
  google.protobuf.Timestamp at = 4;
  uint32 tries = 5;
}

message JobNoted {
  option (payday.notification) = {};

  bytes job = 1;
  string note = 2;
  repeated string tags = 3;
  bytes email = 4;
  Where at = 5;
}

message Where {
  string bay = 1;
  string label = 2 [(payday.field) = {personal: false}];
}
`

func TestAPayloadOfIdentifiersAndCodesIsWell(t *testing.T) {
	require.Empty(t, payloadsOf(t, payloads, "JobDone"))
	require.Empty(t, payloadsOf(t, payloads))
}

// TestAPayloadsFreeTextIsAsked: a string that says nothing about itself, a
// list of them, one in a message the payload holds, and a field named for
// somebody whatever it holds.
func TestAPayloadsFreeTextIsAsked(t *testing.T) {
	x := require.New(t)

	v := payloadsOf(t, payloads, "JobDone", "JobNoted")
	x.Contains(v, "notification payload app.JobNoted holds note, tags, email, app.Where.bay, which are free text")
	x.NotContains(v, "app.JobDone", "a payload of identifiers and codes said about no one was asked about")
	x.NotContains(v, "label")
	x.Contains(v, "[(payday.field) = {personal: false}]")
	x.Contains(v, "proto/app/payload.proto:")
}

// TestNotificationsWithNoPayloadMessageAreSaid: an app that declares
// notifications and marks no message one may carry has every payload it
// publishes refused when it runs: an app that has not marked what it
// publishes.
func TestNotificationsWithNoPayloadMessageAreSaid(t *testing.T) {
	x := require.New(t)
	notified := &pdgen.Notify{Notification: 40, Receipt: 41, At: "proto/app/app.proto", KeepDays: 30}

	vs := doctorPayloads(&pdgen.Schema{Notify: notified})
	x.Len(vs, 1)
	x.Contains(vs[0].What, "proto/app/app.proto declares notifications and no message one may carry")
	x.Contains(vs[0].What, "NotificationService.Add refuses every notification this app publishes with a payload")
	x.False(vs[0].Fatal, "a notification with no payload is a notification, and the schema is fine without one")
	x.Contains(vs[0].Fix, "option (payday.notification) = {};")
	x.Contains(vs[0].Fix, "docs/notify.md")

	// An app that marks one, and one with no notifications, are not asked.
	x.Empty(doctorPayloads(&pdgen.Schema{}))
	x.Empty(doctorPayloads(&pdgen.Schema{Notify: notified, Payloads: payloadsOfSchema(t, payloads, "JobDone")}))
}
