package cmd_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli/xlitest"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestUUIDTypesReachGeneratedRequestsAndCLI(t *testing.T) {
	b, ctx := build(t)
	seen := make(chan proto.Message, 1)
	conn := pdtest.Serve(t, b.grpc(t, grpc.ChainUnaryInterceptor(func(ctx context.Context, in any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		switch info.FullMethod {
		case app.HolderService_Add_FullMethodName, app.HolderService_Patch_FullMethodName:
			seen <- proto.Clone(in.(proto.Message))
		}
		return next(ctx, in)
	})))
	id := pdid.New(2)
	for _, tc := range []struct {
		verb, value string
		strict      bool
	}{
		{"add", id.String(), false}, {"patch", id.String(), false},
		{"add", base64.StdEncoding.EncodeToString(id.Bytes()), false},
		{"add", base64.StdEncoding.EncodeToString(id.Bytes()), true},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			args := []string{"holder", tc.verb}
			if tc.strict {
				args = append(args, "--in", "protojson")
			}
			args = append(args, "@acme/admin", fmt.Sprintf(`{"account":%q}`, tc.value))
			got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: b.travels(ctx)}.Run(t, args...)
			// Account is server-owned and remains refused. This test checks the actual
			// protobuf request received before that policy runs, not a bypass of it.
			require.Error(t, got.Err)
			var request proto.Message
			select {
			case request = <-seen:
			default:
				t.Fatalf("the CLI never sent the request: %v", got.Err)
			}
			m := request.ProtoReflect()
			f := m.Descriptor().Fields().ByName("account")
			opts, _ := proto.GetExtension(f.Options(), ormpb.E_Field).(*ormpb.FieldOptions)
			require.Equal(t, ormpb.Type_TYPE_UUID, opts.GetType())
			require.Equal(t, id.Bytes(), m.Get(f).Bytes())
			require.False(t, opts.GetKey())
			require.False(t, opts.GetNullable())
			var out bytes.Buffer
			require.NoError(t, pdcmd.Json.Print(&out, request))
			require.Contains(t, out.String(), `"account": "`+id.String()+`"`)
		})
	}
}

func TestUUIDFilterMetadataTravelsTheStack(t *testing.T) {
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)
	added := xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.Run(t, "holder", "add", "-o", "json", "@acme/uuid-filter")
	require.NoError(t, added.Err)
	var row struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(added.Stdout), &row))
	require.NotEmpty(t, row.ID)
	got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.Run(t, "audit", "list", "-o", "json", fmt.Sprintf(`{"filters":[{"object_id":%q}]}`, row.ID))
	require.NoError(t, got.Err)
	require.Contains(t, got.Stdout, row.ID)
	field := (&app.AuditFilter{}).ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name("object_id"))
	opts, _ := proto.GetExtension(field.Options(), ormpb.E_Field).(*ormpb.FieldOptions)
	require.Equal(t, ormpb.Type_TYPE_UUID, opts.GetType())
}
