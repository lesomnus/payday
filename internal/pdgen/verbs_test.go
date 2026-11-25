package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
)

// patcher and applier are an entity with a Patch and no Apply, and one with an
// Apply and no Patch, each with an edge that can move -- which is what the gate
// asks about -- a name, a secret and something personal, so that every layer
// that says anything of a general write has something to say of these.
const patcher = `
message Patcher {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias = 4;
  Robot robot = 8 [(orm.edge) = {nullable: true}];
  bytes secret = 9 [(payday.field) = {secret: true}];
  string personal = 10 [(payday.field) = {personal: true}];
  option (orm.message) = {rpc: {add: {}, get: {}, patch: {}, erase: {}}};
  option (payday.entity) = {domain: 8, erase: {hard: {}}};
}
`

const applier = `
message Applier {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias = 4;
  Robot robot = 8 [(orm.edge) = {nullable: true}];
  bytes secret = 9 [(payday.field) = {secret: true}];
  string personal = 10 [(payday.field) = {personal: true}];
  option (orm.message) = {rpc: {add: {}, get: {}, apply: {}, erase: {}}};
  option (payday.entity) = {domain: 9, erase: {hard: {}}};
}
`

// TestEachGeneralWriteIsWrittenForWhereItIsAskedFor: `orm` names Apply on its
// own now, and no verb brings another (protobuf-orm/protobuf-orm#4). What
// payday writes for a general write -- the gate's question about an edge that
// moves, the Sink's forgetting, the layer that hides a secret, the resource
// table and the access record, the requests admission is told of, the trail's
// refusal of a hand-written row -- is written for the verb that is there: an
// entity with a Patch and no Apply has all of it for its Patch and nothing
// naming an Apply, and one with an Apply and no Patch the other way round.
// Written for both, it named a request the contract does not have, and the app
// did not compile.
func TestEachGeneralWriteIsWrittenForWhereItIsAskedFor(t *testing.T) {
	x := require.New(t)

	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", ownAll(t)+tenantedRobot+patcher+applier)})
	x.NoError(err)

	g := p.NewGeneratedFile("pd.go", "example.com/app/server/pd")
	g.P("package pd")
	g.P()
	paths := pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"}
	pdgen.EmitGate(g, s, paths, "example.com/app")
	pdgen.EmitSink(g, s, paths, "example.com/app")
	pdgen.EmitSecret(g, s, paths, "example.com/app")
	pdgen.EmitAdmission(g, s, paths, "example.com/app")
	pdgen.EmitResources(g, s, paths, "example.com/app")
	pdgen.EmitAccess(g, s, paths, "example.com/app")
	pdgen.EmitAudit(g, s, paths, "example.com/app")

	b, err := g.Content()
	x.NoError(err)
	out := string(b)

	for _, v := range []string{
		"func (s gatePatcher) Patch(",
		"func (s sinkPatcher) Patch(",
		"func (s secretPatcher) Patch(",
		"app.PatcherService_Patch_FullMethodName",
		"&app.PatcherPatchRequest{}",
		"&app.PatcherRef{}",

		"func (s gateApplier) Apply(",
		"gateApplierEntity",
		"func (s sinkApplier) Apply(",
		"func (s secretApplier) Apply(",
		"app.ApplierService_Apply_FullMethodName",
		"&app.ApplierRef{}",
	} {
		x.Contains(out, v, "not written for a verb the entity has")
	}
	for _, v := range []string{
		"PatcherApplyRequest",
		"PatcherService_Apply_",
		"gatePatcherEntity",
		") Apply(ctx context.Context, req *app.Patcher",

		"ApplierPatchRequest",
		"ApplierService_Patch_",
		") Patch(ctx context.Context, req *app.Applier",
	} {
		x.NotContains(out, v, "written for a verb the entity does not have")
	}

	// And the Sink's Apply of an entity whose name it keeps asks what the
	// document writes to the alias, as its Patch folds the field, and still
	// forgets the row first.
	apply := out[strings.Index(out, "func (s sinkApplier) Apply("):]
	apply = apply[:strings.Index(apply, "\n}\n")]
	for _, v := range []string{`seen.Forget(s.store.Db.Driver(), "Applier", req.GetRef())`, "ormpatch.Compile(sinkApplierEntity", "slug.ParseAlias(given)"} {
		x.Contains(apply, v)
	}
	x.Equal(1, strings.Count(out, "func (s sinkApplier) Apply("))

	// The two kinds each has a record of, by the method that wrote it.
	flat := strings.Join(strings.Fields(out), " ")
	x.Contains(flat, "case app.PatcherService_Patch_FullMethodName: return pdpb.ChangeKind_CHANGE_KIND_UPDATED")
	x.Contains(flat, "case app.ApplierService_Apply_FullMethodName: return pdpb.ChangeKind_CHANGE_KIND_UPDATED")

	// And the trail refuses the writes payday's own trail declares, and
	// writes nothing for one it does not.
	audit := s.Own(pdpb.Own_OWN_AUDIT)
	r := audit.Rpcs()
	for verb, has := range map[string]bool{
		"Add":   r.HasAdd(),
		"Patch": r.HasPatch(),
		"Apply": r.HasApply(),
		"Erase": r.HasErase(),
	} {
		x.Equal(has, strings.Contains(out, "func (s auditService) "+verb+"("), verb)
	}
}
