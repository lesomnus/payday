package pdcmd

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHumanTimestampsUseTheNamedZone(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	require.NoError(t, err)
	stamp := timestamppb.New(time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC))
	for _, shape := range []string{"payload", "copies", "mapped"} {
		m := wrapMapped(t, stamp, shape)
		before := proto.Clone(m)
		p, err := PrinterNamed("pretty", PrintOptions{Location: seoul})
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, p.Print(&out, m))
		require.Contains(t, out.String(), "2026-11-01T08:30:00+09:00")
		require.True(t, proto.Equal(before, m))
	}
	m := wrapMapped(t, stamp, "payload")
	// The wrapper's repeated fixture fields would make Table choose list rows;
	// use a response with only the scalar timestamp column for this assertion.
	fd := protodesc.ToFileDescriptorProto(m.ProtoReflect().Descriptor().ParentFile())
	fd.MessageType[0].Field = fd.MessageType[0].Field[:1]
	fd.MessageType[0].NestedType = nil
	file, err := protodesc.NewFile(fd, protoregistry.GlobalFiles)
	require.NoError(t, err)
	tableRow := dynamicpb.NewMessage(file.Messages().Get(0))
	tableRow.Set(tableRow.Descriptor().Fields().Get(0), protoreflect.ValueOfMessage(stamp.ProtoReflect()))
	for _, name := range []string{"table", "wide"} {
		p, err := PrinterNamed(name, PrintOptions{Location: seoul})
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, p.Print(&out, tableRow))
		require.Contains(t, out.String(), "2026-11-01T08:30:00+09:00")
	}
	for _, name := range []string{"json", "protojson", "prototext", "template={{.payload}}"} {
		plain, err := PrinterNamed(name)
		require.NoError(t, err)
		zoned, err := PrinterNamed(name, PrintOptions{Location: seoul})
		require.NoError(t, err)
		var before, after bytes.Buffer
		require.NoError(t, plain.Print(&before, m))
		require.NoError(t, zoned.Print(&after, m))
		require.Equal(t, before.String(), after.String(), name)
	}
	var out bytes.Buffer
	require.NoError(t, Pretty.Print(&out, m))
	require.Contains(t, out.String(), "2026-10-31T23:30:00Z")
}

func TestDisplayZoneUsesTheInstantForDaylightSaving(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	p, err := PrinterNamed("pretty", PrintOptions{Location: berlin})
	require.NoError(t, err)
	for _, tt := range []struct {
		month time.Month
		want  string
	}{
		{time.January, "2026-01-01T01:00:00+01:00"},
		{time.July, "2026-07-01T02:00:00+02:00"},
	} {
		var out bytes.Buffer
		require.NoError(t, p.Print(&out, timestamppb.New(time.Date(2026, tt.month, 1, 0, 0, 0, 0, time.UTC))))
		require.Contains(t, out.String(), tt.want)
	}
}

func TestDisplayZonePrecedenceAndValidation(t *testing.T) {
	t.Setenv("TZ", "Asia/Seoul")
	t.Setenv("MY_APP_TZ", "")
	zone := func(args ...string) (string, error) {
		var got string
		cmd := &xli.Command{Name: "my-app", Flags: flg.Flags{TimeZoneFlag()}, Handler: xli.OnRun(func(_ context.Context, c *xli.Command, _ xli.Next) error {
			loc, err := Location(c)
			if err == nil {
				got = loc.String()
			}
			return err
		})}
		result := (xlitest.Harness{Cmd: cmd}).Run(t, args...)
		return got, result.Err
	}
	got, err := zone()
	require.NoError(t, err)
	require.Equal(t, "UTC", got)
	t.Setenv("MY_APP_TZ", "Asia/Seoul")
	got, err = zone()
	require.NoError(t, err)
	require.Equal(t, "Asia/Seoul", got)
	got, err = zone("--tz", "UTC")
	require.NoError(t, err)
	require.Equal(t, "UTC", got)
	_, err = zone("--tz", "No/SuchZone")
	require.ErrorContains(t, err, "--tz")
}
