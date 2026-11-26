package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
)

// answers is an entity with no secret whose edge is one that has, and a
// service of the patcher's own whose answers hold the patcher in every way a
// message holds another -- and one whose answer holds nothing that can be,
// and a service that is no entity's.
const answers = `
message Keeper {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  Patcher patcher = 8 [(orm.edge) = {nullable: true}];
  option (orm.message) = {rpc: {add: {}, get: {}}};
  option (payday.entity) = {domain: 11, erase: {hard: {}}};
}

message Word {}

message Holding {
  Patcher patcher = 1;
}

message Deep {
  Holding one = 1;
  repeated Holding many = 2;
  map<string, Holding> by = 3;
  oneof pick {
    Holding first = 4;
    string none = 5;
  }
  repeated Deep below = 6;
  google.protobuf.Any any = 7;
  bytes raw = 8;
  Word word = 9;
}

message Plain {
  Word word = 1;
  google.protobuf.Any any = 2;
}

service PatcherService {
  rpc Survey(Word) returns (Deep);
  rpc Stream(Word) returns (stream Holding);
  rpc Gather(stream Word) returns (Deep);
  rpc Chat(stream Word) returns (stream Patcher);
  rpc Plainly(Word) returns (Plain);
}

service LoneService {
  rpc Survey(Word) returns (Deep);
}
`

func withAny(src string) string {
	return strings.Replace(inPkg("fleet", src), `import "orm.proto";`, "import \"orm.proto\";\nimport \"google/protobuf/any.proto\";", 1)
}

// TestTheSecretLayerClearsEveryAnswerThatCanHoldOne: what leaves the layer
// carries no secret field, whichever RPC answered it. Each RPC of an entity's
// service whose answer can hold a secret -- through a field, a list, a map's
// values, a oneof's case, to any depth, a message inside itself -- is written
// to clear it, in each shape a method has; what can hold none is left to
// promotion; an `Any` and `bytes` are not looked into; a generated verb of an
// entity whose edge holds a secret clears it there; and a service that is no
// entity's is not one the layer stands before.
func TestTheSecretLayerClearsEveryAnswerThatCanHoldOne(t *testing.T) {
	x := require.New(t)

	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": withAny(ownAll(t) + tenantedRobot + patcher + answers)})
	x.NoError(err)

	g := p.NewGeneratedFile("pd.go", "example.com/app/server/pd")
	g.P("package pd")
	paths := pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"}
	pdgen.EmitSecret(g, s, paths, protogen.GoImportPath("example.com/app"))
	b, err := g.Content()
	x.NoError(err)
	out := string(b)

	for _, v := range []string{
		"func (s secretPatcher) Survey(ctx context.Context, req *app.Word) (*app.Deep, error) {\n\tv, err := s.PatcherServiceServer.Survey(ctx, req)\n\n\treturn hideDeep(v), err\n}",
		"func (s secretPatcher) Stream(req *app.Word, out grpc.ServerStreamingServer[app.Holding]) error {\n\treturn s.PatcherServiceServer.Stream(req, hidingServerStream[app.Holding]{out, hideHolding})\n}",
		"func (s secretPatcher) Gather(out grpc.ClientStreamingServer[app.Word, app.Deep]) error {\n\treturn s.PatcherServiceServer.Gather(hidingClientStream[app.Word, app.Deep]{out, hideDeep})\n}",
		"func (s secretPatcher) Chat(out grpc.BidiStreamingServer[app.Word, app.Patcher]) error {\n\treturn s.PatcherServiceServer.Chat(hidingBidiStream[app.Word, app.Patcher]{out, hidePatcher})\n}",

		// Each way a message holds another, and itself.
		"\thideHolding(v.GetOne())\n",
		"\tfor _, w := range v.GetMany() {\n\t\thideHolding(w)\n\t}\n",
		"\tfor _, w := range v.GetBy() {\n\t\thideHolding(w)\n\t}\n",
		"\thideHolding(v.GetFirst())\n",
		"\tfor _, w := range v.GetBelow() {\n\t\thideDeep(w)\n\t}\n",
		"func hideHolding(v *app.Holding) *app.Holding {\n\tif v == nil {\n\t\treturn nil\n\t}\n\n\thidePatcher(v.GetPatcher())\n",

		// A verb of an entity with no secret, whose edge has one.
		"func (s secretKeeper) Get(ctx context.Context, req *app.KeeperGetRequest) (*app.Keeper, error) {",
		"func (s secretKeeper) Add(ctx context.Context, req *app.KeeperAddRequest) (*app.Keeper, error) {",
		"\thidePatcher(v.GetPatcher())\n\n\treturn v\n}",

		// The patcher's own, with its secret and then what its edge holds,
		// which is nothing.
		"func hidePatcher(v *app.Patcher) *app.Patcher {",
		"[]protoreflect.FieldNumber{9}",
	} {
		x.Contains(out, v)
	}

	for _, v := range []string{
		// What holds nothing that can be is not written.
		"Plainly(", "hidePlain", "hideWord", "hideRobot", "secretRobot",
		// What cannot be looked into is not.
		"GetAny", "GetRaw", "GetWord",
		// And a service that is no entity's is not before the layer.
		"Lone",
	} {
		x.NotContains(out, v)
	}

	// Each message once.
	x.Equal(1, strings.Count(out, "func hideDeep("))
	x.Equal(1, strings.Count(out, "func hidePatcher("))
}

// TestAnRpcCalledSecretIsRefusedWhereTheLayerWritesAType: the layer writes a
// type around the service of an entity whose edge holds a secret, embedding
// its struct, so an RPC of that service called `Secret` is a method beside a
// field of that name, and is refused as one is around an entity that has a
// secret itself.
func TestAnRpcCalledSecretIsRefusedWhereTheLayerWritesAType(t *testing.T) {
	svc := "\nservice KeeperService {\n  rpc Secret(Word) returns (Word);\n}\n"
	_, err := readFiles(t, map[string]string{"test/schema.proto": withAny(ownAll(t) + tenantedRobot + patcher + strings.Replace(answers, "service LoneService", svc+"\nservice LoneService", 1))})
	require.Error(t, err)
	require.Contains(t, err.Error(), "KeeperService/Secret is called what secretKeeper holds as a field")
}
