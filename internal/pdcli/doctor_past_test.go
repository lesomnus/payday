package pdcli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// What `pd doctor` asks of an app that keeps a past (payday#151): what `pd
// gen` takes and the past then gets wrong, and the lock as a generation would
// hold the schema to it.
//
// Each against a copy of the reference app, which keeps a past and whose
// schema is clean: so what is found is what the case wrote, and the checkout
// is never written into -- a case stopped halfway leaves nothing behind for
// the next generation of it to pick up.

// ownProto writes one of the app's own .proto files into a copy of it.
func ownProto(t *testing.T, l pdcli.Layout, name string, src string) {
	t.Helper()
	require.NoError(t, os.WriteFile(l.Path(pdcli.DirProto, "app", name), []byte(src), 0o644))
}

// findingOf is the one finding whose words hold `what`, and fails when there
// is none or more than one.
func findingOf(t *testing.T, l pdcli.Layout, what string) pdcli.Finding {
	t.Helper()

	var vs []pdcli.Finding
	for _, v := range pdcli.Doctor(t.Context(), l) {
		if strings.Contains(v.What, what) {
			vs = append(vs, v)
		}
	}
	require.Len(t, vs, 1, "findings saying %q", what)

	return vs[0]
}

// shift is an entity of the app with four columns named for an event, and
// `marked` on the first two.
func shift(marked string) string {
	return fmt.Sprintf(`edition = "2023";

package app;

import "app/payday/tenant.proto";
import "google/protobuf/timestamp.proto";
import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "github.com/lesomnus/payday/internal/apptest";

message Shift {
  bytes id = 1 [(orm.field) = {
    type: TYPE_UUID
    key: true
    default: ""
  }];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];

  // The operation that began it, and the one that ended it.
  bytes event_id = 8 [(orm.field) = {type: TYPE_UUID}%[1]s];
  bytes closing_event_id = 9 [(orm.field) = {type: TYPE_UUID, nullable: true}%[1]s];

  // A row of the app's own CalendarEvent, which is no event of the past.
  bytes calendar_event_id = 10 [(orm.field) = {type: TYPE_UUID, nullable: true}];

  // Named for one, and no identifier at all.
  bytes digest_event_id = 11 [(orm.field) = {default: ""}];

  google.protobuf.Timestamp date_created = 15 [(orm.field) = {
    immutable: true
    default: ""
  }];

  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {
    domain: 203
    erase: {hard: {}}
  };
}

message CalendarEvent {
  bytes id = 1 [(orm.field) = {
    type: TYPE_UUID
    key: true
    default: ""
  }];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];

  google.protobuf.Timestamp date_created = 15 [(orm.field) = {
    immutable: true
    default: ""
  }];

  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {
    domain: 204
    erase: {hard: {}}
  };
}
`, marked)
}

// TestDoctorAsksWhetherAColumnNamedForAnEventNamesOne: the past's expiry asks
// only a column that says it names an event, and takes the event while one
// that does not goes on naming it. So a column the declaration would be taken
// on, named for an event and saying nothing, is asked about -- with what to
// write, and where -- and once it says so, it is not.
func TestDoctorAsksWhetherAColumnNamedForAnEventNamesOne(t *testing.T) {
	x := require.New(t)
	l := genApp(t)

	ownProto(t, l, "shift.proto", shift(""))
	v := findingOf(t, l, "app.Shift says nothing of")
	x.Equal("app.Shift says nothing of event_id, closing_event_id, which are named for an event: the past's expiry "+
		"asks only a column that says it names one, and takes the event while the row goes on naming it", v.What)
	x.False(v.Fatal, "advice, and generation does what it did")
	x.Contains(v.Fix, "[(orm.field) = {...}, (payday.field) = {event: true}]", "what to write")
	x.Contains(v.Fix, "an index that begins with it")
	x.Contains(v.Fix, "proto/app/shift.proto:22  event_id", "where it is declared, to the line")
	x.Contains(v.Fix, "proto/app/shift.proto:23  closing_event_id")

	// A row of an entity of the app's own, and bytes that name nothing: the
	// declaration would be taken on neither, as no event's.
	x.NotContains(v.What, "calendar_event_id")
	x.NotContains(v.What, "digest_event_id")

	ownProto(t, l, "shift.proto", shift(", (payday.field) = {event: true}"))
	x.NotContains(found(t, l), "app.Shift says nothing of")
}

// TestDoctorDoesNotAskPaydaysEventAboutAColumnItCannotMark: of payday's
// Event, only its cause says it names an event, and `pd gen` refuses the
// declaration on any other column of it -- so a column an overlay added there
// is not one the doctor tells anybody to mark.
func TestDoctorDoesNotAskPaydaysEventAboutAColumnItCannotMark(t *testing.T) {
	x := require.New(t)
	l := genApp(t)

	// What an overlay at a number the template leaves empty is, once merged.
	p := l.Path(l.DirPd(), "event.proto")
	b, err := os.ReadFile(p)
	x.NoError(err)
	at := "  google.protobuf.Any value = 19 ["
	x.Contains(string(b), at)
	x.NoError(os.WriteFile(p, []byte(strings.Replace(string(b), at,
		"  bytes origin_event_id = 20 [(orm.field) = {type: TYPE_UUID, nullable: true}];\n\n"+at, 1)), 0o644))

	v := found(t, l)
	x.NotContains(v, "pd gen would refuse", "the copy as an overlay leaves it is not one the reader takes")
	x.NotContains(v, "origin_event_id")
}

// visited is two event messages, and what they hold; `host`, `guest` and
// `born` are the three of their fields that are named.
func visited(host, guest, born string) string {
	return `edition = "2023";

package app;

import "google/protobuf/timestamp.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "github.com/lesomnus/payday/internal/apptest";

message VisitNoted {
  option (payday.event) = {kinds: "visit.note"};

  bytes visit = 1;
  string ` + host + ` = 2;
  Guest guest = 3;
  map<string, Guest> companions = 4;

  // A word, and not a part of one.
  string hotel = 5;

  // Who came, by identifier, and how they are reached.
  message Guest {
    bytes holder = 1;
    string ` + guest + ` = 2;
  }

  // Declared here and held by nothing, so in no row.
  message Unused {
    string email = 1;
  }
}

message VisitSigned {
  option (payday.event) = {};

  bytes visit = 1;
  VisitNoted.Guest guest = 2;
  google.protobuf.Timestamp ` + born + ` = 3;
}
`
}

// TestDoctorAsksAboutAFieldOfAnEventMessageNamedForSomebody: an event names
// people by identifier only, and `pd gen` refuses one that declares a person
// -- but not one that says nothing, which is the one that holds somebody's
// email all the same. So a field named for something about a person is asked
// about, in the event message and in every message it holds, once.
func TestDoctorAsksAboutAFieldOfAnEventMessageNamedForSomebody(t *testing.T) {
	x := require.New(t)
	l := genApp(t)

	ownProto(t, l, "visited.proto", visited("host_email", "phone_number", "birthday"))

	v := findingOf(t, l, "event message app.VisitNoted holds")
	x.Equal("event message app.VisitNoted holds host_email, app.VisitNoted.Guest.phone_number, which are named for "+
		"something about a person, and an event names people by identifier only", v.What)
	x.False(v.Fatal)
	x.Contains(v.Fix, "by identifier")
	x.Contains(v.Fix, "json_name", "a field whose rows were written keeps the name they were written with")
	x.Contains(v.Fix, "proto/app/visited.proto:15  host_email", "where it is declared, to the line")
	x.Contains(v.Fix, "proto/app/visited.proto:25  app.VisitNoted.Guest.phone_number")
	x.NotContains(v.What, "hotel")
	x.NotContains(v.What, "Unused", "a message nothing holds is in no row")

	// Guest is held by both, and asked about once, under the first of them
	// by name; the second is asked about what is its own.
	v = findingOf(t, l, "event message app.VisitSigned holds")
	x.Contains(v.What, "event message app.VisitSigned holds birthday, which is named")
	x.NotContains(v.What, "phone_number")

	ownProto(t, l, "visited.proto", visited("host", "reached_at", "closed_at"))
	x.NotContains(found(t, l), "named for something about a person")
}

// TestDoctorSaysANameThatIsTwoFieldsInADomain: a field of one name at two
// numbers across the messages of a domain's kinds is a schema `pd gen`
// refuses, and the doctor says it in the words a generation stops on.
func TestDoctorSaysANameThatIsTwoFieldsInADomain(t *testing.T) {
	x := require.New(t)
	l := genApp(t)

	ownProto(t, l, "repaired.proto", `edition = "2023";

package app;

import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "github.com/lesomnus/payday/internal/apptest";

// Of the domain machine.service is of, holding the machine at another number.
message MachineRepaired {
  option (payday.event) = {kinds: "machine.repair"};

  bytes part = 1;
  bytes machine = 2;
}
`)

	v := found(t, l)
	x.Contains(v, `app.MachineRepaired and app.MachineServiced: machine: is 2 in the first and 1 in the second, `+
		`and both hold kinds of "machine".`)
	x.Contains(v, "    bytes machine = 2;  // app.MachineRepaired\n    bytes machine = 1;  // app.MachineServiced\n")
}

// TestDoctorHoldsTheSchemaToItsLockWithoutWritingIt: the comparison `pd gen`
// makes of the event messages and `past.lock`, made without writing the lock.
// A schema the lock refuses is said in the words a generation would refuse it
// in, as stopping one; a lock behind the schema says what it lacks; a lock
// missing beside event messages, and one written in another way, say so too.
// And the reference app's lock, as committed, says nothing.
func TestDoctorHoldsTheSchemaToItsLockWithoutWritingIt(t *testing.T) {
	// The lock as it is now, which the doctor must leave as it found it.
	still := func(t *testing.T, l pdcli.Layout, want []byte) {
		t.Helper()

		b, err := os.ReadFile(filepath.Join(l.Root, l.DirPd(), pdcli.LockFile))
		if want == nil {
			require.ErrorIs(t, err, os.ErrNotExist, "the doctor wrote a lock")
			return
		}
		require.NoError(t, err)
		require.Equal(t, string(want), string(b), "the doctor wrote the lock")
	}

	t.Run("in step", func(t *testing.T) {
		x := require.New(t)
		l := genApp(t)

		b, _ := lockOf(t, l)
		x.NotContains(found(t, l), "past.lock")
		still(t, l, []byte(b))
	})

	t.Run("a schema it refuses", func(t *testing.T) {
		x := require.New(t)
		l := genApp(t)

		b, _ := lockOf(t, l)
		editMachine(t, l, "  google.protobuf.Timestamp due = 6;\n", "")

		v := findingOf(t, l, "past.lock")
		x.True(v.Fatal, "pd gen stops on it")
		x.Contains(v.What, "pd gen would refuse this schema: proto/app/payday/past.lock holds what this schema no longer declares as it was")
		x.Contains(v.What, "\n  app.MachineServiced (6): was \"due\", and is gone\n")
		x.Contains(v.What, "docs/guide/past.md")
		still(t, l, []byte(b))
	})

	t.Run("behind the schema", func(t *testing.T) {
		x := require.New(t)
		l := genApp(t)

		// An event message, and a kind a message it still holds takes.
		_, v := lockOf(t, l)
		delete(v.Kinds, "app.MachineStarted")
		delete(v.Messages, "app.MachineStarted")
		delete(v.Kinds, "retract")
		delete(v.Messages, "app.MachineServiced.Part")
		delete(v.Messages["app.MachineServiced"], 6)
		delete(v.Enums["app.Mode"], 3)
		b := v.Marshal()
		x.NoError(os.WriteFile(filepath.Join(l.Root, l.DirPd(), pdcli.LockFile), b, 0o644))

		f := findingOf(t, l, "past.lock")
		x.False(f.Fatal, "pd gen adds it, and goes on")
		x.Equal("proto/app/payday/past.lock does not hold all this schema declares: nothing refuses a change to what "+
			"it lacks that leaves rows unreadable, and `pd gen --check` fails on it, until `pd gen` adds it and it "+
			"is committed", f.What)
		x.Equal("go tool pd gen app\n\nwhich adds\n\n"+
			"  kind \"app.MachineStarted\", whose rows hold app.MachineStarted\n"+
			"  kind \"retract\", whose rows hold app.Corrected\n"+
			"  app.MachineServiced (6): \"due\"\n"+
			"  app.MachineServiced.Part: a message an event message holds\n"+
			"  app.Mode: 3, MODE_SERVICE", f.Fix)
		still(t, l, b)
	})

	t.Run("of the payday before a message held several kinds", func(t *testing.T) {
		x := require.New(t)
		l := genApp(t)

		// Each event message by its one kind, as that payday wrote it.
		b := []byte(`{"events": {"app.MachineServiced": "machine.service"}, "messages": {}}` + "\n")
		x.NoError(os.WriteFile(filepath.Join(l.Root, l.DirPd(), pdcli.LockFile), b, 0o644))

		f := findingOf(t, l, "past.lock")
		x.True(f.Fatal, "pd gen stops on it")
		x.Contains(f.What, `pd gen would refuse this schema: proto/app/payday/past.lock records each event message's kind under "events"`)
		x.Contains(f.What, `write each "app.Message": "kind" of "events" as "kind": "app.Message" under "kinds"`)
		x.NotContains(f.What, "hand edit of it has to stay that", "it is JSON, and the JSON is not what is wrong")
		still(t, l, b)
	})

	t.Run("not there", func(t *testing.T) {
		x := require.New(t)
		l := genApp(t)

		_, v := lockOf(t, l)
		x.NoError(os.Remove(filepath.Join(l.Root, l.DirPd(), pdcli.LockFile)))

		f := findingOf(t, l, "past.lock")
		x.False(f.Fatal, "pd gen writes it, and goes on")
		messages := slices.Compact(slices.Sorted(maps.Values(v.Kinds)))
		x.Contains(f.What, fmt.Sprintf("proto/app/payday/past.lock is not there, and this schema declares %d event messages", len(messages)))
		x.Equal("go tool pd gen app", f.Fix)
		still(t, l, nil)
	})

	t.Run("written another way", func(t *testing.T) {
		x := require.New(t)
		l := genApp(t)

		// Everything in it, on one line.
		b, _ := lockOf(t, l)
		v := &bytes.Buffer{}
		x.NoError(json.Compact(v, []byte(b)))
		x.NoError(os.WriteFile(filepath.Join(l.Root, l.DirPd(), pdcli.LockFile), v.Bytes(), 0o644))

		f := findingOf(t, l, "past.lock")
		x.False(f.Fatal)
		x.Contains(f.What, "proto/app/payday/past.lock is not written as `pd gen` writes it")
		still(t, l, v.Bytes())
	})
}
