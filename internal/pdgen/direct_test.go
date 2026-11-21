package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
)

func TestDirectDeclarationsNeedNoAccountsAndGuardEveryStreamShape(t *testing.T) {
	src := accounted("", "", `
message Answer {}
service HolderService {
 rpc Approve(Answer) returns (Answer) { option (payday.method) = {direct: {}}; }
 rpc Listen(Answer) returns (stream Answer) { option (payday.method) = {direct: {}}; }
 rpc Upload(stream Answer) returns (Answer) { option (payday.method) = {direct: {}}; }
 rpc Discuss(stream Answer) returns (stream Answer) { option (payday.method) = {direct: {}}; }
}`)
	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", src)})
	require.NoError(t, err)
	require.Nil(t, s.Accounts)
	g := p.NewGeneratedFile("gate.go", "example.com/app/server/pd")
	g.P("package pd")
	pdgen.EmitGate(g, s, pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"}, protogen.GoImportPath("example.com/app"))
	body, err := g.Content()
	require.NoError(t, err)
	out := string(body)
	for _, name := range []string{"Approve", "Listen", "Upload", "Discuss"} {
		require.Contains(t, out, "HolderService_"+name+"_FullMethodName")
	}
	require.Equal(t, 4, strings.Count(out, "gate.Direct("))
	require.Contains(t, out, "grpc.ClientStreamingServer[")
	require.Contains(t, out, "grpc.BidiStreamingServer[")
	require.NotContains(t, out, "gate.AsAccount(")
}
