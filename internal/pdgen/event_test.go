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

// TestAnEventMessageIsReadWithItsKinds: a message that says it is what an
// event holds is read with the kinds it says, in the order it says them, or
// its full name when it says none -- and found where it is declared, inside
// another message too, since a registry missing one is a row of it that a
// page cannot read.
func TestAnEventMessageIsReadWithItsKinds(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "fleet", past(t, `
message RobotMoved {
  option (payday.event) = {kinds: "robot.move"};
  bytes robot = 1;
}

// One message of two kinds: a robot parked, and one taken out of its bay,
// which holds no bay to say.
message RobotParked {
  option (payday.event) = {kinds: ["robot.park", "robot.unpark"]};
  bytes robot = 1;
  bytes bay = 2;
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
  option (payday.event) = {kinds: "shift.handover"};
  Shift.Began began = 1;
  Note note = 2;
}

message Note {
  string text = 1;
}
`))
	x.NoError(err)

	kinds := map[string][]string{}
	for _, v := range s.Events {
		kinds[string(v.FullName())] = v.Kinds
	}
	x.Equal(map[string][]string{
		"fleet.RobotMoved":   {"robot.move"},
		"fleet.RobotParked":  {"robot.park", "robot.unpark"},
		"fleet.RobotStopped": {"fleet.RobotStopped"},
		"fleet.Shift.Began":  {"fleet.Shift.Began"},
		"fleet.Handover":     {"shift.handover"},
	}, kinds)

	by := map[string]string{}
	for _, v := range s.EventKinds() {
		by[v.Kind] = string(v.Event.FullName())
	}
	x.Equal(map[string]string{
		"robot.move":         "fleet.RobotMoved",
		"robot.park":         "fleet.RobotParked",
		"robot.unpark":       "fleet.RobotParked",
		"fleet.RobotStopped": "fleet.RobotStopped",
		"fleet.Shift.Began":  "fleet.Shift.Began",
		"shift.handover":     "fleet.Handover",
	}, by, "each kind, with its message")
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
// whether the kind is written out or is another message's full name, and
// whichever of a message's several kinds it is.
func TestAKindIsOneMessages(t *testing.T) {
	for name, src := range map[string]string{
		"both written out": `
message RobotMoved {
  option (payday.event) = {kinds: "robot.move"};
}
message RobotWalked {
  option (payday.event) = {kinds: "robot.move"};
}`,
		"one the other's name": `
message RobotMoved {
  option (payday.event) = {};
}
message RobotWalked {
  option (payday.event) = {kinds: "fleet.RobotMoved"};
}`,
		"one of several": `
message RobotMoved {
  option (payday.event) = {kinds: ["robot.move", "robot.walk"]};
}
message RobotWalked {
  option (payday.event) = {kinds: ["robot.stroll", "robot.walk"]};
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

// TestAKindIsAWordSaidOnce: a kind said empty is no kind -- saying none is
// how a message is of its full name -- and one said twice by a message is the
// declaration saying something it did not mean.
func TestAKindIsAWordSaidOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		kinds string
		want  string
	}{
		"empty":           {`""`, "fleet.RobotMoved: says a kind that is empty"},
		"empty, of two":   {`["robot.move", ""]`, "fleet.RobotMoved: says a kind that is empty"},
		"twice":           {`["robot.move", "robot.move"]`, `fleet.RobotMoved: says kind "robot.move" twice`},
		"twice, of three": {`["robot.move", "robot.walk", "robot.move"]`, `fleet.RobotMoved: says kind "robot.move" twice`},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			_, err := readAs(t, "fleet", past(t, `
message RobotMoved {
  option (payday.event) = {kinds: `+tc.kinds+`};
  bytes robot = 1;
}`))
			x.Error(err)
			x.Contains(err.Error(), tc.want)
		})
	}
}

// TestAFieldNameIsOneFieldAcrossADomain: across the event messages of a
// domain's kinds -- a kind's domain being what it says before its first `.` --
// a field of one name has one number and one type, and a schema that gives it
// two is refused with both messages, the field and both numbers or types. A
// message of kinds of two domains is held to both. What is not asked: a name
// in two domains, a message one holds, and a field renamed with its JSON name
// kept, which answers to its new name.
func TestAFieldNameIsOneFieldAcrossADomain(t *testing.T) {
	assigned := `
message AssetAssigned {
  option (payday.event) = {kinds: ["asset.assign", "asset.release"]};
  bytes asset = 1;
  bytes holder = 2;
  bytes role = 3;
  repeated string tags = 4;
}
`
	for name, tc := range map[string]struct {
		src  string
		want []string
	}{
		"another number": {assigned + `
message AssetCorrected {
  option (payday.event) = {kinds: "asset.correct"};
  bytes asset = 1;
  bytes event = 2;
  bytes role = 4;
}`, []string{
			`fleet.AssetAssigned and fleet.AssetCorrected: role: is 3 in the first and 4 in the second, and both hold kinds of "asset".`,
			"\n\n    bytes role = 3;  // fleet.AssetAssigned\n    bytes role = 4;  // fleet.AssetCorrected\n\n",
			"`asset` of `asset.assign`",
		}},

		"another type": {assigned + `
message AssetCorrected {
  option (payday.event) = {kinds: "asset.correct"};
  bytes asset = 1;
  bytes event = 2;
  string role = 3;
}`, []string{
			`fleet.AssetAssigned and fleet.AssetCorrected: role: holds bytes in the first and string in the second, and both hold kinds of "asset".`,
			"\n\n    bytes role = 3;  // fleet.AssetAssigned\n    string role = 3;  // fleet.AssetCorrected\n\n",
		}},

		"a list where it is one value": {assigned + `
message AssetTagged {
  option (payday.event) = {kinds: "asset.tag"};
  bytes asset = 1;
  string tags = 4;
}`, []string{
			`fleet.AssetAssigned and fleet.AssetTagged: tags: holds repeated string in the first and string in the second`,
			"\n\n    repeated string tags = 4;  // fleet.AssetAssigned\n    string tags = 4;  // fleet.AssetTagged\n\n",
		}},

		"another message": {assigned + `
message AssetMoved {
  option (payday.event) = {kinds: "asset.move"};
  Place asset = 1;
}
message Place {
  string label = 1;
}`, []string{
			`fleet.AssetAssigned and fleet.AssetMoved: asset: holds bytes in the first and fleet.Place in the second`,
		}},

		"both": {assigned + `
message AssetCorrected {
  option (payday.event) = {kinds: "asset.correct"};
  string role = 5;
}`, []string{
			`fleet.AssetAssigned and fleet.AssetCorrected: role: is 3 holding bytes in the first and 5 holding string in the second`,
		}},

		"a message of two domains, by its second": {assigned + `
message RobotMoved {
  option (payday.event) = {kinds: "robot.move"};
  bytes robot = 1;
}
message AssetCarried {
  option (payday.event) = {kinds: ["asset.carry", "robot.carry"]};
  bytes asset = 1;
  bytes robot = 2;
}`, []string{
			`fleet.AssetCarried and fleet.RobotMoved: robot: is 2 in the first and 1 in the second, and both hold kinds of "robot".`,
			"`robot` of `robot.carry`",
		}},

		"kinds that are their messages' full names": {`
message RobotStopped {
  option (payday.event) = {};
  google.protobuf.Timestamp at = 1;
}
message MachineStopped {
  option (payday.event) = {};
  bytes machine = 1;
  google.protobuf.Timestamp at = 2;
}`, []string{
			`fleet.MachineStopped and fleet.RobotStopped: at: is 2 in the first and 1 in the second, and both hold kinds of "fleet".`,
		}},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			x := require.New(t)

			_, err := readAs(t, "fleet", past(t, tc.src))
			x.Error(err, "a name that is two fields in a domain was read")
			for _, v := range tc.want {
				x.Contains(err.Error(), v)
			}
			x.Contains(err.Error(), "keeps its `json_name`")
		})
	}

	t.Run("as the guide says it", func(t *testing.T) {
		_, err := readAs(t, "app", past(t, `
message RobotMoved {
  option (payday.event) = {kinds: "robot.move"};
  bytes robot = 1;
}
message RobotParked {
  option (payday.event) = {kinds: ["robot.park", "robot.unpark"]};
  bytes bay = 1;
  bytes robot = 2;
}`))
		require.ErrorContains(t, err, "app.RobotMoved and app.RobotParked: robot: is 1 in the first and 2 in the second, "+
			"and both hold kinds of \"robot\".\n\n"+
			"    bytes robot = 1;  // app.RobotMoved\n"+
			"    bytes robot = 2;  // app.RobotParked\n", "docs/guide/past.md quotes it so")
	})

	t.Run("and what is not asked", func(t *testing.T) {
		x := require.New(t)

		_, err := readAs(t, "fleet", past(t, assigned+`
// The same field at the same number, holding the same, in another message
// of the domain -- and another presence, which its JSON does not show.
message AssetCorrected {
  option (payday.event) = {kinds: "asset.correct"};
  bytes asset = 1;
  bytes event = 2;
  bytes role = 3 [features.field_presence = EXPLICIT];
  repeated string tags = 4;
}

// The same names, at other numbers, in another domain.
message RobotAssigned {
  option (payday.event) = {kinds: "robot.assign"};
  bytes robot = 1;
  string role = 2;
  bytes asset = 5;
}

// A field renamed with its JSON name kept answers to the name it has now,
// and a message an event message holds has fields of its own.
message AssetHandedOver {
  option (payday.event) = {kinds: "asset.hand"};
  bytes asset = 1;
  bytes given_role = 5 [json_name = "role"];
  Hand hand = 6;
}
message Hand {
  string role = 1;
  bytes holder = 7;
}
`))
		x.NoError(err)
	})
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

// TestTheGoRegistryIsWrittenWithAPast: `pd.EventKinds` is the message of
// every kind, the nested ones by their Go names and a message of two kinds
// for each -- and nothing at all in an app with no past, which has no Event to
// hold one.
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
  option (payday.event) = {kinds: "robot.move"};
}
message RobotParked {
  option (payday.event) = {kinds: ["robot.park", "robot.unpark"]};
}
message Shift {
  message Began {
    option (payday.event) = {};
  }
}`))
	x.Contains(out, "var EventKinds = map[string]protoreflect.MessageType{\n"+
		"\t\"fleet.Shift.Began\": (*app.Shift_Began)(nil).ProtoReflect().Type(),\n"+
		"\t\"robot.move\":        (*app.RobotMoved)(nil).ProtoReflect().Type(),\n"+
		"\t\"robot.park\":        (*app.RobotParked)(nil).ProtoReflect().Type(),\n"+
		"\t\"robot.unpark\":      (*app.RobotParked)(nil).ProtoReflect().Type(),\n"+
		"}\n", "every kind, in order, and a message of two once for each")

	out = emit(t, past(t, ""))
	x.Contains(out, "var EventKinds = map[string]protoreflect.MessageType{}", "a past with no events yet has an empty registry")

	x.NotContains(emit(t, ownAll(t)), "EventKinds", "an app with no past was given a registry")
}

// TestTheTypeScriptRegistryIsWrittenForEveryApp: `events.ts` names each event
// message's schema by protobuf-es's name for it, from the file protobuf-es
// wrote it into, and the kinds as literals TypeScript reads as written, a
// message of two kinds by each -- with the registry holding each message once
// -- and is there, empty, for an app with no past, whose page passes it all
// the same.
func TestTheTypeScriptRegistryIsWrittenForEveryApp(t *testing.T) {
	x := require.New(t)

	// An accent, which is written as it is, and a tag character, which Go's
	// `%q` writes as a `\U` escape -- one JavaScript does not have.
	s, err := readAs(t, "fleet", past(t, `
message RobotMoved {
  option (payday.event) = {kinds: "robot.move é\U000e0001"};
}
message RobotParked {
  option (payday.event) = {kinds: ["robot.park", "robot.unpark"]};
}
message Shift {
  message Began {
    option (payday.event) = {};
  }
}`))
	x.NoError(err)

	out := pdgen.TsEvents(s, pdgen.TsEventSource)
	x.Contains(out, "import { createRegistry, type DescMessage } from '@bufbuild/protobuf'")
	x.Contains(out, "import { RobotMovedSchema, RobotParkedSchema, Shift_BeganSchema } from './test/schema_pb.js'")
	x.Contains(out, "export const kinds = {\n"+
		"\t\"fleet.Shift.Began\": Shift_BeganSchema,\n"+
		"\t\"robot.move é\U000e0001\": RobotMovedSchema,\n"+
		"\t\"robot.park\": RobotParkedSchema,\n"+
		"\t\"robot.unpark\": RobotParkedSchema,\n"+
		"} as const")
	x.NotContains(out, `\U`)
	x.Contains(out, "export const events = createRegistry(RobotMovedSchema, RobotParkedSchema, Shift_BeganSchema)\n",
		"each message once, by its full name")

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

	out := emit(t, past(t, entity("Robot", `domain: 7`, `Tenant tenant = 2 [(orm.edge) = {immutable: true}];`,
		`bytes moved_by = 8 [(orm.field) = {type: TYPE_UUID, nullable: true}, (payday.field) = {event: true}];`)+
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

	// What the expiry asks of it: every column that names an event, the
	// past's and the app's, by the names ent gives them.
	x.Contains(out, "var pastPointers = []past.Pointer{\n"+
		"\t{Domain: EventDomain, Table: event.Table, Column: event.FieldCauseId, Own: pdpb.Own_OWN_EVENT},\n"+
		"\t{Domain: FactDomain, Table: fact.Table, Column: fact.FieldEventId, Own: pdpb.Own_OWN_FACT},\n"+
		"\t{Domain: FactDomain, Table: fact.Table, Column: fact.FieldSupersededBy, Own: pdpb.Own_OWN_FACT},\n"+
		"\t{Domain: RobotDomain, Table: robot.Table, Column: robot.FieldMovedBy, Own: pdpb.Own_OWN_UNSPECIFIED},\n"+
		"}\n")
	x.Contains(out, "func (s pastStore) DeleteEvents(ctx context.Context, ids []pdid.Id) (int, error) {")
	x.Contains(out, "func (s pastStore) DeleteFacts(ctx context.Context, ids []pdid.Id) (int, error) {")
	x.Contains(out, "func (s pastStore) CountFacts(ctx context.Context, where []func(*sql.Selector)) (int, error) {")
	x.Contains(out, "_, err := RecordErased(ctx, s.db, s.content, action, ids)", "what an expiry takes is on the trail")
	x.Contains(out, "func PastIn(ungated app.Server, drv dialect.Driver, content audit.Contents) func(")
	x.Contains(out, "ctx = frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Grant{}).WithScope(frame.Only(tenant)))",
		"the deployment's work in the tenant, and in it alone")
	x.Contains(out, "s, err := Past(ungated, tdrv, content)", "on the transaction it began")
	x.Contains(out, "func PastTenants(db *ent.Client) func(")

	x.NotContains(emit(t, ownAll(t)+tenantedRobot), "func Past(", "an app with no past was given a store of one")
}

// TestThePastsNamesAreWrittenWithAPast: `pd.PastNames` is written into an app
// that declares a past and into no other, and names a row of each entity that
// has something to be called by -- an alias, a name -- through the wall, as
// the title the header reads of it: whose data that is when the field it was
// read off is declared personal, and nobody's otherwise.
func TestThePastsNamesAreWrittenWithAPast(t *testing.T) {
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

	out := emit(t, past(t, entity("Robot", `domain: 7`, `Tenant tenant = 2 [(orm.edge) = {immutable: true}];`)+`
message Person {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias = 4 [(payday.field) = {personal: false}];
  string name = 5 [(payday.field) = {personal: true}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 8, erase: {hard: {}}};
}

message Lock {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 10, erase: {hard: {}}};
}
`))
	x.Contains(out, "func PastNames(drv dialect.Driver) past.Names {")
	at, end := strings.Index(out, "func (n pastNames) Name("), strings.Index(out, "func pastUnnamed(")
	x.True(at >= 0 && end > at, "Name, and the helper after it")
	names := out[at:end]

	// The block a kind is named by: its case, to the next.
	block := func(kind string) string {
		i := strings.Index(names, "\tcase "+kind+"Domain:\n")
		if i < 0 {
			return ""
		}
		v := names[i+1:]
		if j := strings.Index(v, "\tcase "); j >= 0 {
			v = v[:j]
		} else if j := strings.Index(v, "\tdefault:"); j >= 0 {
			v = v[:j]
		}
		return v
	}

	x.Contains(block("Person"), "p, err := bare.PersonNarrow(ctx, Wall(), person.IdEQ(id.Uuid()))", "through the wall")
	x.Contains(block("Person"), "v, err := n.db.Person.Query().Where(p).Only(ctx)")
	x.Contains(block("Person"), "row, alias, name = v.Proto(), false, true", "a person's name is theirs, and the handle they declare is not")
	x.Contains(block("Holder"), "row, alias, name = v.Proto(), true, true", "payday's holder: both declared personal")
	x.Contains(block("Robot"), "row, alias, name = v.Proto(), false, false", "an alias nothing declares is nobody's")
	x.Contains(block("Tenant"), "row, alias, name = v.Proto(), false, false")
	x.Empty(block("Lock"), "nothing to be called by")
	x.Empty(block("Audit"), "payday's own records are named by nothing")
	x.Contains(names, "h := header.Of(row)", "what a row is called is the header's title")

	x.NotContains(emit(t, ownAll(t)+tenantedRobot), "func PastNames(", "an app with no past was given names of one")
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

// TestThePastIsWrittenByItsStoreAlone: the Audit layer of an app that keeps a
// past refuses every verb of its Event and Fact that writes, unless the
// context is one the store marked, and the store marks every context it hands
// the servers and no other. Reads are not touched, the verbs are payday's to
// say, and an app with no past has none of it.
func TestThePastIsWrittenByItsStoreAlone(t *testing.T) {
	emit := func(t *testing.T, src string) string {
		t.Helper()

		s, p, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", src)})
		require.NoError(t, err)

		g := p.NewGeneratedFile("pd.go", "example.com/app/server/pd")
		g.P("package pd")
		g.P()
		paths := pdgen.Paths{Ent: "example.com/app/internal/ent", Bare: "example.com/app/server/bare"}
		pdgen.EmitAudit(g, s, paths, "example.com/app")
		pdgen.EmitPast(g, s, paths, "example.com/app")

		b, err := g.Content()
		require.NoError(t, err)

		return string(b)
	}
	refuses := func(recv, rpc, in, out string) string {
		return "func (s " + recv + ") " + rpc + "(ctx context.Context, req *app." + in + ") (*app." + out + ", error) {\n" +
			"\tif !isPastWrite(ctx) {\n" +
			"\t\treturn nil, errPast()\n" +
			"\t}\n\n" +
			"\treturn s." + strings.TrimPrefix(recv, "audit") + "ServiceServer." + rpc + "(ctx, req)\n}\n"
	}

	x := require.New(t)

	out := emit(t, past(t, ""))
	x.Contains(out, "func (s Audit) Event() app.EventServiceServer {\n\treturn auditEvent{s, s.Next().Event()}\n}")
	x.Contains(out, "func (s Audit) Fact() app.FactServiceServer {\n\treturn auditFact{s, s.Next().Fact()}\n}")
	for _, v := range [][4]string{
		{"auditEvent", "Add", "EventAddRequest", "Event"},
		{"auditEvent", "AddMany", "EventAddManyRequest", "EventAddManyResponse"},
		{"auditFact", "Add", "FactAddRequest", "Fact"},
		{"auditFact", "AddMany", "FactAddManyRequest", "FactAddManyResponse"},
		{"auditFact", "Patch", "FactPatchRequest", "Fact"},
		{"auditFact", "Apply", "FactApplyRequest", "Fact"},
	} {
		x.Contains(out, refuses(v[0], v[1], v[2], v[3]))
	}
	for _, v := range []string{"auditEvent) Get(", "auditEvent) List(", "auditFact) Get(", "auditFact) List(", "auditEvent) Patch(", "auditEvent) Erase(", "auditFact) Erase("} {
		x.NotContains(out, v, "a verb that reads, or one the entity does not have")
	}
	x.Contains(out, "codes.PermissionDenied")
	x.Contains(out, `"a past is written by the past package: `)

	// Each write the store makes, on a context it marked.
	for _, v := range []string{
		"func (s pastStore) AddEvents(ctx context.Context, vs []past.EventRow) ([]pdid.Id, error) {\n\tctx = pastWriting(ctx)\n",
		"func (s pastStore) AddFacts(ctx context.Context, vs []past.FactRow) error {\n\tctx = pastWriting(ctx)\n",
		"func (s pastStore) Supersede(ctx context.Context, vs []past.FactRow, at time.Time, by pdid.Id) error {\n\tctx = pastWriting(ctx)\n",
	} {
		x.Contains(out, v)
	}
	x.Equal(3, strings.Count(out, "pastWriting(ctx)\n"), "and nothing else: not what PastIn hands its work")

	// And which verbs they are is payday's: an app cannot give either one
	// that the layer would have to know of.
	src := past(t, "")
	more := strings.Replace(src, "      get: {}\n    }", "      get: {}\n      erase: {}\n    }", 1)
	x.NotEqual(src, more, "no Event rpc to add a verb to")
	_, _, err := readPlugin(t, map[string]string{"test/schema.proto": inPkg("fleet", more)})
	x.ErrorContains(err, "fleet.Event: (orm.message) rpc.erase is not payday's")

	none := emit(t, ownAll(t)+tenantedRobot)
	x.NotContains(none, "func (s Audit) Event()", "an app with no past was given a layer of one")
	x.NotContains(none, "pastWrite")
}
