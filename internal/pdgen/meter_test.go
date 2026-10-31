package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
)

// What `pd gen` holds a meter to (payday#186): every field and value it names
// has to be the entity's, and every condition one the generated query can say
// safely -- each a bill that would otherwise be wrong without a word.

// metered is a schema with an entity of every kind of field a meter's
// condition is asked about, a global one, and the trail's shape of two tenant
// columns -- and `meters` declared over them.
func metered(meters string) string {
	return `
option (payday.app) = {meters: [` + meters + `]};
` + tenant + `
enum Mode {
  MODE_UNSPECIFIED = 0;
  MODE_IDLE = 1;
  MODE_RUN = 2;
}

message Dock {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  option (orm.message) = {rpc: {crud: true}, indexes: [{name: "by_tenant", refs: [{name: "tenant", number: 2}]}]};
  option (payday.entity) = {domain: 8, erase: {hard: {}}};
}

message Machine {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias = 4;
  string model = 8;
  string colour = 9 [features.field_presence = EXPLICIT];
  int32 axes = 10;
  bool running = 11;
  Mode mode = 12;
  double load = 16;
  bytes serial = 17 [(orm.field) = {default: ""}];
  bytes maker_id = 18 [(orm.field) = {type: TYPE_UUID, nullable: true}];
  google.protobuf.Timestamp date_serviced = 19 [(orm.field) = {nullable: true}];
  repeated string tags = 20;
  uint64 cycles = 21;
  Dock dock = 22 [(orm.edge) = {nullable: true}];
  bool spare = 23 [features.field_presence = EXPLICIT];
  google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];
  option (orm.message) = {rpc: {crud: true}, indexes: [{name: "by_tenant", refs: [{name: "tenant", number: 2}]}]};
  option (payday.entity) = {domain: 7};
}

message Fleet {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 9, global: {}, erase: {hard: {}}};
}

message Record {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  bytes tenant_id = 8 [(orm.field) = {type: TYPE_UUID}];
  bytes actor_tenant_id = 9 [(orm.field) = {type: TYPE_UUID}];
  option (orm.message) = {rpc: {get: {}}};
  option (payday.entity) = {domain: 10, tenanted: {field: ["tenant_id", "actor_tenant_id"]}, erase: {hard: {}}, trail: {off: {why: "a test"}}};
}
`
}

// counted is one count meter of Machine with `where`.
func counted(where string) string {
	return `{key: "acme.machines", unit: "{machine}", count: {of: "Machine", where: [` + where + `]}}`
}

func TestAMeterResolvesAgainstTheEntityItCounts(t *testing.T) {
	x := require.New(t)

	s, err := read(t, metered(counted(`{field: {name: "mode", number: 12}, in: ["MODE_RUN"]}`)+`,
		{key: "acme.machines.cycles", unit: "{cycle}", sum: {of: "Machine", field: {name: "cycles", number: 21}}},
		{key: "acme.docks", unit: "{dock}", count: {of: "test.Dock"}}`))
	x.NoError(err)

	x.Len(s.Meters, 3)
	m := s.Meters[0]
	x.Equal("acme.machines", m.Key)
	x.Equal("{machine}", m.Unit)
	x.Equal("test.Machine", string(m.Of.FullName()))
	x.Empty(m.Sum)
	x.Len(m.Where, 1)
	x.Equal([]string{"int32(2) /* MODE_RUN */"}, m.Where[0].In)
	x.Equal("test/schema.proto", m.At)

	x.Equal("cycles", s.Meters[1].Sum)
	x.Equal("test.Dock", string(s.Meters[2].Of.FullName()), "a full name is a name too")
}

// TestWhatAMeterNamesIsTheEntitys: every refusal, each in words that say what
// to write instead.
func TestWhatAMeterNamesIsTheEntitys(t *testing.T) {
	for name, tc := range map[string]struct {
		meters string
		says   string
	}{
		"no such entity": {`{key: "acme.x", unit: "1", count: {of: "Robot"}}`, `"Robot" is no entity`},
		"no entity":      {`{key: "acme.x", unit: "1", count: {}}`, "says no `of:`"},
		"a global one":   {`{key: "acme.x", unit: "1", count: {of: "Fleet"}}`, "declared `global`"},
		"two tenants":    {`{key: "acme.x", unit: "1", count: {of: "Record"}}`, "held by none of them more than another"},
		"no measure":     {`{key: "acme.x", unit: "1"}`, "neither `count:` nor `sum:`"},
		"a bad key":      {`{key: "machines", unit: "1", count: {of: "Machine"}}`, "is not a key"},
		"no unit":        {`{key: "acme.x", count: {of: "Machine"}}`, "says no unit"},
		"a key twice": {
			`{key: "acme.x", unit: "1", count: {of: "Machine"}}, {key: "acme.x", unit: "1", count: {of: "Dock"}}`,
			`two are called "acme.x"`,
		},

		"a name and no number": {counted(`{field: {name: "mode"}, in: ["MODE_RUN"]}`), "both its name and its number"},
		"a number and no name": {counted(`{field: {number: 12}, in: ["MODE_RUN"]}`), "both its name and its number"},
		"a name that is another field's number": {
			counted(`{field: {name: "mode", number: 11}, in: ["MODE_RUN"]}`), `11 of test.Machine is "running" and this says "mode"`,
		},
		"no field at the number": {counted(`{field: {name: "mode", number: 99}, in: ["MODE_RUN"]}`), "has nothing at 99"},

		"no value of the enum": {counted(`{field: {name: "mode", number: 12}, in: ["MODE_FLY"]}`), `test.Mode has no value "MODE_FLY": it has MODE_UNSPECIFIED, MODE_IDLE, MODE_RUN`},
		"a bool that is not":   {counted(`{field: {name: "running", number: 11}, in: ["yes"]}`), "is not a bool"},
		"both bools":           {counted(`{field: {name: "running", number: 11}, in: ["true", "false"]}`), "says both of its values"},
		"an int too big":       {counted(`{field: {name: "axes", number: 10}, in: ["2147483648"]}`), "is not an int32"},
		"a negative uint":      {counted(`{field: {name: "cycles", number: 21}, in: ["-1"]}`), "is not a uint64"},
		"a value twice":        {counted(`{field: {name: "model", number: 8}, in: ["a", "a"]}`), `says "a" twice`},
		"a float":              {counted(`{field: {name: "load", number: 16}, in: ["1"]}`), "is a float"},
		"bytes":                {counted(`{field: {name: "serial", number: 17}, in: ["x"]}`), "is bytes"},
		"an identifier":        {counted(`{field: {name: "maker_id", number: 18}, in: ["x"]}`), "is uuid"},
		"a time's value":       {counted(`{field: {name: "date_serviced", number: 19}, in: ["x"]}`), "is time"},
		"a list":               {counted(`{field: {name: "tags", number: 20}, in: ["x"]}`), "holds many values"},
		"an edge's value":      {counted(`{field: {name: "dock", number: 22}, in: ["x"]}`), "is an edge"},
		"set of a NOT NULL":    {counted(`{field: {name: "model", number: 8}, set: true}`), "is never null"},
		"the erased field":     {counted(`{field: {name: "date_erased", number: 14}, set: true}`), "an erased row is never measured"},
		"the tenant":           {counted(`{field: {name: "tenant", number: 2}, set: true}`), "says whose a row is"},
		"nothing said":         {counted(`{field: {name: "model", number: 8}}`), "says none of"},
		"two things said":      {counted(`{field: {name: "model", number: 8}, in: ["a"], not_in: ["b"]}`), "more than one of"},
		"one field twice": {
			counted(`{field: {name: "model", number: 8}, in: ["a"]}, {field: {name: "model", number: 8}, not_in: ["b"]}`),
			`says two things of "model"`,
		},

		"a float summed": {`{key: "acme.x", unit: "1", sum: {of: "Machine", field: {name: "load", number: 16}}}`, "a bill is whole numbers"},
		"a string summed": {
			`{key: "acme.x", unit: "1", sum: {of: "Machine", field: {name: "model", number: 8}}}`, "a bill is whole numbers",
		},
		"an enum summed": {`{key: "acme.x", unit: "1", sum: {of: "Machine", field: {name: "mode", number: 12}}}`, "a bill is whole numbers"},
		"an edge summed": {`{key: "acme.x", unit: "1", sum: {of: "Machine", field: {name: "dock", number: 22}}}`, "is an edge"},
		"a sum by name":  {`{key: "acme.x", unit: "1", sum: {of: "Machine", field: {name: "cycles"}}}`, "both its name and its number"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := read(t, metered(tc.meters))
			require.ErrorContains(t, err, tc.says)
			require.ErrorContains(t, err, "test/schema.proto: meters:", "it says where")
		})
	}
}

// TestMetersAreSaidOnce, in the file that says the package is the app's.
func TestMetersAreSaidOnce(t *testing.T) {
	_, err := readFiles(t, map[string]string{
		"test/schema.proto": inPkg("test", metered(`{key: "acme.x", unit: "1", count: {of: "Machine"}}`)),
		"test/other.proto":  inPkg("test", `option (payday.app) = {meters: [{key: "acme.y", unit: "1", count: {of: "Dock"}}]};`),
	})
	require.ErrorContains(t, err, "both declare meters")
}

// TestTheMeasuringIsTheDeclaration: the query `pd.Meters` writes is the
// tenant's rows that are not erased, and each condition as ent's predicate of
// it -- a field that may be null saying so of a value it may not hold, since
// SQL leaves a null out of NOT IN.
func TestTheMeasuringIsTheDeclaration(t *testing.T) {
	x := require.New(t)

	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("test", metered(
		counted(`
			{field: {name: "mode", number: 12}, in: ["MODE_IDLE", "MODE_RUN"]},
			{field: {name: "running", number: 11}, in: ["true"]},
			{field: {name: "spare", number: 23}, not_in: ["true"]},
			{field: {name: "model", number: 8}, not_in: ["prototype", "scrap"]},
			{field: {name: "colour", number: 9}, not_in: ["red"]},
			{field: {name: "axes", number: 10}, in: ["-3", "6"]},
			{field: {name: "maker_id", number: 18}, set: true},
			{field: {name: "date_serviced", number: 19}, set: false},
			{field: {name: "dock", number: 22}, set: false}`)+`,
		{key: "acme.machines.cycles", unit: "{cycle}", sum: {of: "Machine", field: {name: "cycles", number: 21}}}`))})
	x.NoError(err)

	g := p.NewGeneratedFile("meters.go", "example.com/app/server/pd")
	g.P("package pd")
	g.P()
	pdgen.EmitMeters(g, s, pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"})
	b, err := g.Content()
	x.NoError(err)
	out := string(b)

	for _, v := range []string{
		"func Meters(db *ent.Client) []meter.Meter",
		`Key:  "acme.machines"`,
		`Unit: "{machine}"`,
		"entMachine.TenantIdIn(vs...)",
		"entMachine.DateErasedIsNil()",
		"entMachine.ModeIn(int32(1) /* MODE_IDLE */, int32(2) /* MODE_RUN */)",
		"entMachine.RunningEQ(true)",
		"entMachine.Or(entMachine.SpareIsNil(), entMachine.SpareNEQ(true))",
		`entMachine.ModelNotIn("prototype", "scrap")`,
		`entMachine.Or(entMachine.ColourIsNil(), entMachine.ColourNotIn("red"))`,
		"entMachine.AxesIn(-3, 6)",
		"entMachine.MakerIdNotNil()",
		"entMachine.DateServicedIsNil()",
		"entMachine.DockIdIsNil()",
		"q.Count(ctx)",
		"q.Aggregate(meterSum(entMachine.FieldCycles)).Int(ctx)",
		`"CAST(COALESCE(" + sql.Sum(s.C(column)) + ", 0) AS BIGINT)"`,
		"func MeterTenants(db *ent.Client) func(ctx context.Context, after pdid.Id, limit int) ([]pdid.Id, error)",
		"Where(entTenant.IdGT(after.Uuid()))",
		"Order(entTenant.ById())",
	} {
		x.Contains(out, v)
	}
	x.NotContains(out, "entMachine.Or(entMachine.ModelIsNil()", "a field that may not be null is not asked whether it is")
}

// TestAnAppThatDeclaresNoMetersGetsNoneOfIt: nothing generated, and nothing
// that imports the runtime.
func TestAnAppThatDeclaresNoMetersGetsNoneOfIt(t *testing.T) {
	x := require.New(t)

	src := strings.Replace(metered(""), "option (payday.app) = {meters: []};", "", 1)
	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("test", src)})
	x.NoError(err)
	x.Empty(s.Meters)

	g := p.NewGeneratedFile("meters.go", "example.com/app/server/pd")
	g.P("package pd")
	pdgen.EmitMeters(g, s, pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"})
	b, err := g.Content()
	x.NoError(err)
	x.NotContains(string(b), "meter")
}

// TestAMeterOfATableNoIndexServesByTenantIsWarnedOf: a pass reads one tenant's
// rows at a time, and with no index by tenant each of them is a scan of the
// whole table.
func TestAMeterOfATableNoIndexServesByTenantIsWarnedOf(t *testing.T) {
	x := require.New(t)

	src := strings.Replace(metered(`{key: "acme.machines", unit: "{machine}", count: {of: "Machine"}}`),
		`indexes: [{name: "by_tenant", refs: [{name: "tenant", number: 2}]}]};
  option (payday.entity) = {domain: 7};`, `};
  option (payday.entity) = {domain: 7};`, 1)
	s, err := read(t, src)
	x.NoError(err)

	ws := strings.Join(pdgen.Warnings(s), "\n")
	x.Contains(ws, "meters: acme.machines: no index of test.Machine begins with tenant")
	x.Contains(ws, `indexes: [{name: "tenant", refs: [{name: "tenant", number: 2}]}]`)

	// And the one with an index is not.
	s, err = read(t, metered(`{key: "acme.machines", unit: "{machine}", count: {of: "Machine"}}`))
	x.NoError(err)
	x.NotContains(strings.Join(pdgen.Warnings(s), "\n"), "meters:")
}
