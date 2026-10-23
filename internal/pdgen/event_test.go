package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
)

// past is an app that declares a past, with payday's entities and the two
// copies written, and `src` beside them.
func past(t *testing.T, src string) string {
	t.Helper()
	return declare(20, 21) + ownAll(t) + copies(t, 20, 21) + src
}

// TestAnEventMessageIsReadWithItsKind: a message that says it is what an
// event holds is read with the kind it says, or its full name when it says
// none -- and found where it is declared, inside another message too, since a
// registry missing one is a row of it that a page cannot read.
func TestAnEventMessageIsReadWithItsKind(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "fleet", past(t, `
message RobotMoved {
  option (payday.event) = {kind: "robot.move"};
  bytes robot = 1;
}

message RobotStopped {
  option (payday.event) = {};
  bytes robot = 1;
  google.protobuf.Timestamp at = 2;
}

message Shift {
  message Began {
    option (payday.event) = {};
    bytes by = 1;
  }
}

// What an event holds may be another event's message, and a message of its own.
message Handover {
  option (payday.event) = {kind: "shift.handover"};
  Shift.Began began = 1;
  Note note = 2;
}

message Note {
  string text = 1;
}
`))
	x.NoError(err)

	kinds := map[string]string{}
	for _, v := range s.Events {
		kinds[string(v.FullName())] = v.Kind
	}
	x.Equal(map[string]string{
		"fleet.RobotMoved":   "robot.move",
		"fleet.RobotStopped": "fleet.RobotStopped",
		"fleet.Shift.Began":  "fleet.Shift.Began",
		"fleet.Handover":     "shift.handover",
	}, kinds)
}

// TestAnEventMessageNeedsAPast: an event is a row of payday's Event, which an
// app has once it declares a past -- and the declaration it is told to write
// names two numbers nothing holds.
func TestAnEventMessageNeedsAPast(t *testing.T) {
	x := require.New(t)

	_, err := readAs(t, "fleet", ownAll(t)+`
message RobotMoved {
  option (payday.event) = {};
  bytes robot = 1;
}
`)
	x.Error(err, "an event message was read in an app with no past")
	x.Contains(err.Error(), "fleet.RobotMoved: says it is an event message, and this app declares no past")
	x.Contains(err.Error(), "option (payday.app) = {past: {event: 7, fact: 8}};")
}

// TestAnEntityIsNotAnEventMessage: an event holds what happened to a row, in
// its own row; a row of its own is a table.
func TestAnEntityIsNotAnEventMessage(t *testing.T) {
	x := require.New(t)

	_, err := readAs(t, "fleet", past(t, entity("Robot", `domain: 7`,
		`Tenant tenant = 2 [(orm.edge) = {}];`,
		`option (payday.event) = {};`)))
	x.Error(err, "an entity was read as an event message")
	x.Contains(err.Error(), "fleet.Robot: says it is an event message, and is an entity")
	x.Contains(err.Error(), "`bytes robot = 1`")
}

// TestAKindIsOneMessages: a row says which message its value is by its kind,
// whether the kind is written out or is another message's full name.
func TestAKindIsOneMessages(t *testing.T) {
	for name, src := range map[string]string{
		"both written out": `
message RobotMoved {
  option (payday.event) = {kind: "robot.move"};
}
message RobotWalked {
  option (payday.event) = {kind: "robot.move"};
}`,
		"one the other's name": `
message RobotMoved {
  option (payday.event) = {};
}
message RobotWalked {
  option (payday.event) = {kind: "fleet.RobotMoved"};
}`,
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			_, err := readAs(t, "fleet", past(t, src))
			x.Error(err, "two messages of one kind were read")
			x.Contains(err.Error(), "fleet.RobotMoved and fleet.RobotWalked are both of kind")
			x.Contains(err.Error(), "a kind is one message's")
		})
	}
}

// TestAnEventMessageNamesPeopleByIdentifierOnly: any `(payday.field)`, in the
// event message or in anything it holds -- by a field, a list or a map's
// values. It is refused as the event's, and not with the advice for a message
// an entity's column holds, which is to say it on that column: an event has
// none, and what is wrong is that somebody is in it at all.
func TestAnEventMessageNamesPeopleByIdentifierOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want string
	}{
		"its own field": {`
message RobotMoved {
  option (payday.event) = {};
  string keeper = 1 [(payday.field) = {personal: true}];
}`, "fleet.RobotMoved.keeper: (payday.field) in an event message."},

		"one said to be about no one": {`
message RobotMoved {
  option (payday.event) = {};
  string note = 1 [(payday.field) = {personal: false}];
}`, "fleet.RobotMoved.note: (payday.field) in an event message."},

		"a message it holds": {`
message RobotMoved {
  option (payday.event) = {};
  Place to = 1;
}
message Place {
  string label = 1 [(payday.field) = {personal: true}];
}`, "fleet.Place.label: (payday.field) in an event message, which event message fleet.RobotMoved holds"},

		"a list of them": {`
message RobotMoved {
  option (payday.event) = {};
  repeated Place via = 1;
}
message Place {
  string label = 1 [(payday.field) = {personal: true}];
}`, "fleet.Place.label: (payday.field) in an event message, which event message fleet.RobotMoved holds"},

		"a map of them": {`
message RobotMoved {
  option (payday.event) = {};
  map<string, Place> stops = 1;
}
message Place {
  string label = 1 [(payday.field) = {personal: true}];
}`, "fleet.Place.label: (payday.field) in an event message, which event message fleet.RobotMoved holds"},

		"two deep": {`
message RobotMoved {
  option (payday.event) = {};
  Leg leg = 1;
}
message Leg {
  Place to = 1;
}
message Place {
  string label = 1 [(payday.field) = {personal: true}];
}`, "fleet.Place.label: (payday.field) in an event message, which event message fleet.RobotMoved holds"},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			_, err := readAs(t, "fleet", past(t, tc.src))
			x.Error(err, "a (payday.field) in an event message was read")
			x.Contains(err.Error(), tc.want)
			x.Contains(err.Error(), "name them by identifier")
			x.NotContains(err.Error(), "say it on the column", "the advice is for a message an entity holds")
		})
	}
}

// TestAnEventMessageHoldsNoRow: an entity, or a message of a generated
// contract, held by an event message or by anything it holds. A row kept
// whole keeps its personal fields for as long as the event is kept, and a
// contract's messages are the API's -- a Ref carries a slug, which is
// somebody's name for the row.
func TestAnEventMessageHoldsNoRow(t *testing.T) {
	robot := entity("Robot", `domain: 7`, `Tenant tenant = 2 [(orm.edge) = {}];`)

	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"an entity": {map[string]string{"test/schema.proto": inPkg("fleet", past(t, robot+`
message RobotMoved {
  option (payday.event) = {};
  Robot robot = 1;
}`))}, "fleet.RobotMoved.robot: holds fleet.Robot, an entity, in an event message. An event names a row by its identifier -- `bytes robot = 1`"},

		"an entity a message of it holds": {map[string]string{"test/schema.proto": inPkg("fleet", past(t, robot+`
message RobotMoved {
  option (payday.event) = {};
  repeated Leg legs = 1;
}
message Leg {
  Robot by = 3;
}`))}, "fleet.Leg.by: holds fleet.Robot, an entity, in an event message, which event message fleet.RobotMoved holds"},

		"a message of a contract": {map[string]string{
			"test/schema.proto": inPkg("fleet", past(t, `
import "test/robot_svc.g.proto";

message RobotMoved {
  option (payday.event) = {};
  RobotRef robot = 1;
}`)),
			"test/robot_svc.g.proto": "edition = \"2023\";\npackage fleet;\noption go_package = \"example.com/app\";\n" +
				"message RobotRef {\n  bytes id = 1;\n  string slug = 2;\n}\n",
		}, "fleet.RobotMoved.robot: holds fleet.RobotRef, a message of a generated contract (test/robot_svc.g.proto), in an event message"},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			_, err := readFiles(t, tc.files)
			x.Error(err, "an event message holding a row was read")
			x.Contains(err.Error(), tc.want)
		})
	}
}

// TestAnEventMessageHoldsNoAny: the event's value is the one Any of its row,
// and one inside it holds a message nothing declared -- held by a field, in a
// list, as a map's value, or by a message it holds.
func TestAnEventMessageHoldsNoAny(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want string
	}{
		"a field": {`
message RobotMoved {
  option (payday.event) = {};
  google.protobuf.Any detail = 1;
}`, "fleet.RobotMoved.detail: holds a google.protobuf.Any, in an event message."},
		"a list": {`
message RobotMoved {
  option (payday.event) = {};
  repeated google.protobuf.Any details = 1;
}`, "fleet.RobotMoved.details: holds a google.protobuf.Any, in an event message."},
		"a map": {`
message RobotMoved {
  option (payday.event) = {};
  map<string, google.protobuf.Any> details = 1;
}`, "fleet.RobotMoved.details: holds a google.protobuf.Any, in an event message."},
		"a message it holds": {`
message RobotMoved {
  option (payday.event) = {};
  Leg leg = 1;
}
message Leg {
  google.protobuf.Any detail = 1;
}`, "fleet.Leg.detail: holds a google.protobuf.Any, in an event message, which event message fleet.RobotMoved holds"},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			_, err := readAs(t, "fleet", past(t, tc.src))
			x.Error(err, "an Any in an event message was read")
			x.Contains(err.Error(), tc.want)
		})
	}

	t.Run("and protobuf's own other types are held as they are", func(t *testing.T) {
		x := require.New(t)

		_, err := readAs(t, "fleet", past(t, `
message RobotMoved {
  option (payday.event) = {};
  google.protobuf.Timestamp at = 1;
  repeated google.protobuf.Timestamp stops = 2;
}`))
		x.NoError(err, "a Timestamp in an event message was refused")
	})
}

// TestTheGoRegistryIsWrittenWithAPast: `pd.EventKinds` is every event message
// by its kind, the nested ones by their Go names -- and nothing at all in an
// app with no past, which has no Event to hold one.
func TestTheGoRegistryIsWrittenWithAPast(t *testing.T) {
	emit := func(t *testing.T, src string) string {
		t.Helper()

		s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", src)})
		require.NoError(t, err)

		g := p.NewGeneratedFile("pd.go", "example.com/app/server/pd")
		g.P("package pd")
		g.P()
		pdgen.EmitEventKinds(g, s)

		b, err := g.Content()
		require.NoError(t, err)

		return string(b)
	}

	x := require.New(t)

	out := emit(t, past(t, `
message RobotMoved {
  option (payday.event) = {kind: "robot.move"};
}
message Shift {
  message Began {
    option (payday.event) = {};
  }
}`))
	x.Contains(out, "var EventKinds = map[string]protoreflect.MessageType{")
	x.Contains(out, `"robot.move":        (*app.RobotMoved)(nil).ProtoReflect().Type(),`)
	x.Contains(out, `"fleet.Shift.Began": (*app.Shift_Began)(nil).ProtoReflect().Type(),`)

	out = emit(t, past(t, ""))
	x.Contains(out, "var EventKinds = map[string]protoreflect.MessageType{}", "a past with no events yet has an empty registry")

	x.NotContains(emit(t, ownAll(t)), "EventKinds", "an app with no past was given a registry")
}

// TestTheTypeScriptRegistryIsWrittenForEveryApp: `events.ts` names each event
// message's schema by protobuf-es's name for it, from the file protobuf-es
// wrote it into, and the kinds as literals TypeScript reads as written -- and
// is there, empty, for an app with no past, whose page passes it all the same.
func TestTheTypeScriptRegistryIsWrittenForEveryApp(t *testing.T) {
	x := require.New(t)

	// An accent, which is written as it is, and a tag character, which Go's
	// `%q` writes as a `\U` escape -- one JavaScript does not have.
	s, err := readAs(t, "fleet", past(t, `
message RobotMoved {
  option (payday.event) = {kind: "robot.move é\U000e0001"};
}
message Shift {
  message Began {
    option (payday.event) = {};
  }
}`))
	x.NoError(err)

	out := pdgen.TsEvents(s, pdgen.TsEventSource)
	x.Contains(out, "import { createRegistry, type DescMessage } from '@bufbuild/protobuf'")
	x.Contains(out, "import { RobotMovedSchema, Shift_BeganSchema } from './test/schema_pb.js'")
	x.Contains(out, "\t\"fleet.Shift.Began\": Shift_BeganSchema,\n\t\"robot.move é\U000e0001\": RobotMovedSchema,\n")
	x.NotContains(out, `\U`)
	x.Contains(out, "export const events = createRegistry(Shift_BeganSchema, RobotMovedSchema)")

	s, err = readAs(t, "fleet", ownAll(t))
	x.NoError(err)

	out = pdgen.TsEvents(s, pdgen.TsEventSource)
	x.Contains(out, "export const kinds = {} as const satisfies Readonly<Record<string, DescMessage>>")
	x.Contains(out, "export const events = createRegistry()")
	x.Equal(1, strings.Count(out, "import "), "an app with no events imports a message:\n%s", out)
}

var _ = protogen.GoImportPath("")

// TestThePastsStoreIsWrittenWithAPast: `pd.Past` is written into an app that
// declares a past and into no other, and it says the tenant of a subject the
// way the kind of the subject has one -- read for a kind behind the wall, a
// row of no tenant for one behind none, and no subject at all for one of
// payday's own records or a kind no server writes and the trail does not
// read.
func TestThePastsStoreIsWrittenWithAPast(t *testing.T) {
	emit := func(t *testing.T, src string) string {
		t.Helper()

		s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", src)})
		require.NoError(t, err)

		g := p.NewGeneratedFile("pd.go", "example.com/app/server/pd")
		g.P("package pd")
		g.P()
		pdgen.EmitPast(g, s, pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"}, "example.com/app")

		b, err := g.Content()
		require.NoError(t, err)

		return string(b)
	}

	x := require.New(t)

	out := emit(t, past(t, entity("Robot", `domain: 7`, `Tenant tenant = 2 [(orm.edge) = {immutable: true}];`)+
		entity("Fleet", `domain: 9, global: {}`)+`
message Lock {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  option (orm.message) = {};
  option (payday.entity) = {domain: 10, erase: {hard: {}}, trail: {off: {why: "a lock"}}};
}
`))
	x.Contains(out, "func Past(next app.Server, drv dialect.Driver, content audit.Contents) (past.Store, error) {")
	x.Contains(out, "s, err := enttx.Rebind(next, drv)", "the stack is put on the operation's transaction, not trusted to be")
	x.Contains(out, "\t\t\tcase RobotDomain:\n\t\t\t\trows, err = rowsRobot(ctx, s.db, robot.IdIn(uuids(page)...))\n\t\t\tcase")
	x.Contains(out, "\t\t\tcase FleetDomain:\n\t\t\t\trows, err = rowsFleet(ctx, s.db, fleet.IdIn(uuids(page)...))\n\t\t\t\tglobal = true\n")
	x.NotContains(out, "case AuditDomain:", "payday's own records are no subject")
	x.NotContains(out, "case LockDomain:", "a kind behind the wall no server writes and the trail does not read")
	x.Contains(out, "func (s pastStore) Redact(")
	x.Contains(out, "Record(ctx, s.db, s.content, action,")

	x.NotContains(emit(t, ownAll(t)+tenantedRobot), "func Past(", "an app with no past was given a store of one")
}

// tenantedRobot is a robot behind the wall, for a schema that needs an entity
// of the app's own.
const tenantedRobot = `
message Robot {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 7, erase: {hard: {}}};
}
`
