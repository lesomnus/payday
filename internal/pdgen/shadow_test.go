package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
)

// namesakes is a service whose RPCs are called what entities are called, one
// of each shape a method of a service has.
const namesakes = `
message Word {}
service PatcherService {
  rpc Robot(Word) returns (Word);
  rpc Holder(Word) returns (stream Word);
  rpc Tenant(stream Word) returns (Word);
  rpc Audit(stream Word) returns (stream Word);
}
`

// TestAnRpcCalledWhatAnEntityIsCalledIsStillTheServices: every type a layer
// writes around a service embeds the layer's struct beside it, and the struct
// has an accessor for every entity -- so an RPC called what one is called was
// promoted from neither, and the type was not the service it is returned as
// (an app's `ProducerService/Relay`, beside its entity `Relay`). Each such
// type says the RPC itself now, in each of the four shapes, and the intercept
// -- which writes every RPC but a client stream through an interceptor --
// says only the client streams again.
func TestAnRpcCalledWhatAnEntityIsCalledIsStillTheServices(t *testing.T) {
	x := require.New(t)

	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", ownAll(t)+tenantedRobot+patcher+namesakes)})
	x.NoError(err)

	g := p.NewGeneratedFile("pd.go", "example.com/app/server/pd")
	g.P("package pd")
	paths := pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"}
	root := protogen.GoImportPath("example.com/app")
	pdgen.EmitGate(g, s, paths, root)
	pdgen.EmitAudit(g, s, paths, root)
	pdgen.EmitSecret(g, s, paths, root)
	pdgen.EmitIntercept(g, s, paths, root, p.Files)
	b, err := g.Content()
	x.NoError(err)
	out := string(b)

	for _, recv := range []string{"gatePatcher", "secretPatcher"} {
		x.Contains(out, "func (s "+recv+") Robot(ctx context.Context, req *app.Word) (*app.Word, error) {\n\treturn s.PatcherServiceServer.Robot(ctx, req)")
		x.Contains(out, "func (s "+recv+") Holder(req *app.Word, out grpc.ServerStreamingServer[app.Word]) error {\n\treturn s.PatcherServiceServer.Holder(req, out)")
		x.Contains(out, "func (s "+recv+") Tenant(out grpc.ClientStreamingServer[app.Word, app.Word]) error {\n\treturn s.PatcherServiceServer.Tenant(out)")
		x.Contains(out, "func (s "+recv+") Audit(out grpc.BidiStreamingServer[app.Word, app.Word]) error {\n\treturn s.PatcherServiceServer.Audit(out)")
	}

	// The intercept says the two it wraps once, through the interceptor, and
	// the two it cannot wrap as the service's.
	x.Equal(1, strings.Count(out, "func (s interceptPatcher) Robot("))
	x.Equal(1, strings.Count(out, "func (s interceptPatcher) Holder("))
	x.Contains(out, "func (s interceptPatcher) Tenant(out grpc.ClientStreamingServer[app.Word, app.Word]) error {\n\treturn s.PatcherServiceServer.Tenant(out)")
	x.Contains(out, "func (s interceptPatcher) Audit(out grpc.BidiStreamingServer[app.Word, app.Word]) error {\n\treturn s.PatcherServiceServer.Audit(out)")

	// And nothing else: the robot's service has no namesake, and the audit's
	// types are around payday's trail alone.
	x.Equal(4+4+2, strings.Count(out, " is the service's: "))
}

// TestAnRpcCalledWhatATypeAroundItsServiceHoldsIsRefused: what cannot be
// forwarded is a field -- a type cannot have a method and a field of one name,
// and the field is what the type's own code reads. Refused when the schema is
// read, so `pd gen` and `pd doctor` both say it, naming the service, the RPC
// and what holds the name.
func TestAnRpcCalledWhatATypeAroundItsServiceHoldsIsRefused(t *testing.T) {
	refused := func(t *testing.T, rpc string) error {
		t.Helper()

		svc := "\nmessage Word {}\nservice PatcherService {\n  rpc " + rpc + "(Word) returns (Word);\n}\n"
		_, err := readFiles(t, map[string]string{"test/schema.proto": inPkg("fleet", ownAll(t)+tenantedRobot+patcher+svc)})

		return err
	}

	for rpc, holder := range map[string]string{
		// The layers' own, of the ones written around this service: the
		// patcher is behind the wall and has a secret.
		"Gate":   "gatePatcher",
		"Secret": "secretPatcher",
		// The intercept is written for every entity with an RPC it wraps.
		"Intercept": "interceptPatcher",
		// Every type around the service holds it.
		"PatcherServiceServer": "every layer's type around the service",
		// And the ORM's server, which is not this generator's to write on.
		"Store":                             "bare.PatcherServiceServer",
		"UnimplementedPatcherServiceServer": "bare.PatcherServiceServer",
		"Db":                                "bare.PatcherServiceServer (from bare.Store)",
		"Now":                               "bare.PatcherServiceServer (from bare.Store)",
	} {
		t.Run(rpc, func(t *testing.T) {
			err := refused(t, rpc)
			require.Error(t, err)
			require.Contains(t, err.Error(), "PatcherService/"+rpc+" is called what "+holder+" holds as a field")
		})
	}

	// The audit's type is around payday's trail and the past alone, so an RPC
	// of the patcher's called `Audit` is the gate's accessor of that name,
	// forwarded, and not refused.
	t.Run("Audit", func(t *testing.T) {
		require.NoError(t, refused(t, "Audit"))
	})
}
