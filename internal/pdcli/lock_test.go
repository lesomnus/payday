package pdcli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
	"github.com/lesomnus/payday/internal/pdgen"
)

// lockOf is the past's lock of a generated app, as committed.
func lockOf(t *testing.T, l pdcli.Layout) (string, *pdgen.Lock) {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(l.Root, l.DirPd(), pdcli.LockFile))
	require.NoError(t, err)

	v, err := pdgen.ReadLock(b)
	require.NoError(t, err)

	return string(b), v
}

// editMachine rewrites the app's `machine.proto`, where its event messages
// are, by replacing each `old` with its `new` -- each of which has to be there,
// or the case is about nothing.
func editMachine(t *testing.T, l pdcli.Layout, pairs ...string) {
	t.Helper()
	require.Zero(t, len(pairs)%2)

	p := l.Path(pdcli.DirProto, "app", "machine.proto")
	b, err := os.ReadFile(p)
	require.NoError(t, err)

	src := string(b)
	for i := 0; i < len(pairs); i += 2 {
		require.Contains(t, src, pairs[i], "machine.proto has no %q to change", pairs[i])
		src = strings.Replace(src, pairs[i], pairs[i+1], 1)
	}

	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))
}

// TestTheLockIsWrittenBesideTheCopies: an app that declares a past has its
// lock beside the copies of payday's entities, written by the generation from
// what its event messages hold, and in step with a schema that has not
// changed -- the committed one is the same bytes a generation from nothing
// writes.
func TestTheLockIsWrittenBesideTheCopies(t *testing.T) {
	x := require.New(t)

	l := genApp(t)
	committed, v := lockOf(t, l)
	x.Equal("app.MachineServiced", v.Kinds["machine.service"])
	x.Equal("app.MachineStarted", v.Kinds["app.MachineStarted"], "a message that names no kind is its full name")
	x.Equal("app.Corrected", v.Kinds["correct"], "a message of two kinds, by each")
	x.Equal("app.Corrected", v.Kinds["retract"])
	x.Len(v.Messages["app.Corrected"], 2, "and its fields once")

	x.NoError(os.Remove(filepath.Join(l.Root, l.DirPd(), pdcli.LockFile)))
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))

	again, _ := lockOf(t, l)
	x.Equal(committed, again, "the lock a generation writes is not the one committed")

	b, err := os.ReadFile(filepath.Join(l.Root, l.DirPd(), "README.md"))
	x.NoError(err)
	x.Contains(string(b), "`past.lock`", "the directory's README does not say what the lock is")
}

// TestTheLockIsAddedTo: a field added to an event message, a message added, a
// kind a message takes, and a field renamed that keeps the JSON name its rows
// are written with are what a schema may do -- the first three are added to
// the lock, and the rename is no change to it, as it is none to a row.
func TestTheLockIsAddedTo(t *testing.T) {
	x := require.New(t)

	l := genApp(t)
	before, _ := lockOf(t, l)

	editMachine(t, l,
		"  bytes by = 2;\n", "  bytes serviced_by = 2 [json_name = \"by\"];\n",
	)
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()), "a rename that keeps the JSON name was refused")

	after, _ := lockOf(t, l)
	x.Equal(before, after, "a rename that keeps the JSON name changed the lock")

	editMachine(t, l,
		"  google.protobuf.Timestamp due = 6;\n", "  google.protobuf.Timestamp due = 6;\n  string site = 7;\n",
		"message MachineStarted {", "message MachineStopped {\n  option (payday.event) = {};\n  bytes machine = 1;\n}\n\nmessage MachineStarted {",
		`{kinds: "machine.service"}`, `{kinds: ["machine.service", "machine.repair"]}`,
	)
	x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))

	_, v := lockOf(t, l)
	x.Equal(pdgen.LockField{JSON: "site", Kind: "string"}, v.Messages["app.MachineServiced"][7])
	x.Equal("app.MachineStopped", v.Kinds["app.MachineStopped"])
	x.Equal("app.MachineServiced", v.Kinds["machine.repair"], "a kind a message took")
	x.Equal("app.MachineServiced", v.Kinds["machine.service"], "and the one it held")
	x.Equal(pdgen.LockField{JSON: "by", Kind: "bytes"}, v.Messages["app.MachineServiced"][2], "what was there is kept")
}

// TestTheLockRefusesWhatARowCouldNotBeReadBackWithout: every way a schema can
// lose what a row of an event was written with, each refused by the
// generation in a line that says what went -- and refused before the code is
// generated from it, so the code and the lock stay as they were.
func TestTheLockRefusesWhatARowCouldNotBeReadBackWithout(t *testing.T) {
	for name, tc := range map[string]struct {
		edit []string
		want string
	}{
		"an event message deleted": {
			[]string{"message MachineStarted {\n  option (payday.event) = {};\n\n  bytes machine = 1;\n}\n", ""},
			`kind "app.MachineStarted": its rows hold app.MachineStarted, and no message declares it now`,
		},
		"an event message that is not one now": {
			[]string{"  option (payday.event) = {};\n\n  bytes machine = 1;", "  bytes machine = 1;"},
			`kind "app.MachineStarted": its rows hold app.MachineStarted, and no message declares it now`,
		},
		"a kind changed": {
			[]string{`{kinds: "machine.service"}`, `{kinds: "machine.serviced"}`},
			`kind "machine.service": its rows hold app.MachineServiced, and no message declares it now`,
		},
		"a kind of several dropped": {
			[]string{`{kinds: ["correct", "retract"]}`, `{kinds: "correct"}`},
			`kind "retract": its rows hold app.Corrected, and no message declares it now`,
		},
		"a kind that is its message's full name, named another": {
			[]string{"message MachineStarted {\n  option (payday.event) = {};", "message MachineStarted {\n  option (payday.event) = {kinds: \"machine.start\"};"},
			`kind "app.MachineStarted": its rows hold app.MachineStarted, and no message declares it now`,
		},
		"a kind moved to another message": {
			[]string{
				`{kinds: ["correct", "retract"]}`, `{kinds: "correct"}`,
				"message MachineStarted {\n  option (payday.event) = {};", "message MachineStarted {\n  option (payday.event) = {kinds: [\"app.MachineStarted\", \"retract\"]};",
			},
			`kind "retract": its rows hold app.Corrected, and app.MachineStarted declares it now`,
		},
		"a field removed": {
			[]string{"  google.protobuf.Timestamp due = 6;\n", ""},
			`app.MachineServiced (6): was "due", and is gone`,
		},
		"a field renamed, and its JSON name with it": {
			[]string{"  bytes by = 2;\n", "  bytes serviced_by = 2;\n"},
			`app.MachineServiced (2): was written as "by", and is "servicedBy" now`,
		},
		"a field of a message it holds removed": {
			[]string{"    string serial = 1;\n", ""},
			`app.MachineServiced.Part (1): was "serial", and is gone`,
		},
		"the kind of a field changed": {
			[]string{"    uint32 count = 2;\n", "    uint64 count = 2;\n"},
			`app.MachineServiced.Part (2): "count" held uint32, and holds uint64 now`,
		},
		"a list made one value": {
			[]string{"  repeated Part parts = 4;\n", "  Part parts = 4;\n"},
			`app.MachineServiced (4): "parts" was a list, and is one value now`,
		},
		"a map's key changed": {
			[]string{"  map<string, Part> swapped = 5;\n", "  map<int32, Part> swapped = 5;\n"},
			`app.MachineServiced (5): "swapped" was a map keyed by string, and is a map keyed by int32 now`,
		},
		"a message it holds renamed": {
			[]string{
				"  repeated Part parts = 4;\n", "  repeated Piece parts = 4;\n",
				"  map<string, Part> swapped = 5;\n", "  map<string, Piece> swapped = 5;\n",
				"  message Part {", "  message Piece {",
			},
			`app.MachineServiced.Part: was held by an event message, and none holds it now`,
		},
		"an enum value renamed": {
			[]string{"  MODE_RUN = 2;\n", "  MODE_RUNNING = 2;\n"},
			"app.Mode: 2 was MODE_RUN, and is MODE_RUNNING now",
		},
		"an enum value removed": {
			[]string{"  MODE_SERVICE = 3;\n", ""},
			"app.Mode: 3 was MODE_SERVICE, and is gone",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			x := require.New(t)

			l := genApp(t)
			before, _ := lockOf(t, l)
			code, err := os.ReadFile(l.Path("machine.pb.go"))
			x.NoError(err)

			editMachine(t, l, tc.edit...)

			err = pdcli.Gen{Layout: l}.Run(t.Context())
			x.Error(err, "what the lock holds went, and the generation went on")
			x.Contains(err.Error(), "the event messages, held to the past's lock: proto/app/payday/past.lock holds what this schema no longer declares")
			x.Contains(err.Error(), "\n  "+tc.want+"\n")
			x.Contains(err.Error(), "docs/guide/past.md", "the refusal does not say where taking something back is written up")

			after, _ := lockOf(t, l)
			x.Equal(before, after, "the lock was written by a generation it refused")

			now, err := os.ReadFile(l.Path("machine.pb.go"))
			x.NoError(err)
			x.Equal(string(code), string(now), "the code was generated from a schema the lock refused")
		})
	}
}

// TestCheckSeesAStaleLock: `pd gen --check` answers with the lock when it is
// not what the schema adds up to -- behind it, or not there at all -- as it
// does with any file a generation writes.
func TestCheckSeesAStaleLock(t *testing.T) {
	x := require.New(t)

	l := genApp(t)
	at := filepath.Join(l.Root, l.DirPd(), pdcli.LockFile)

	vs, err := pdcli.Gen{Layout: l}.Check(t.Context())
	x.NoError(err)
	x.Empty(vs, "the committed app is not in step")

	_, v := lockOf(t, l)
	delete(v.Messages["app.MachineServiced"], 6)
	x.NoError(os.WriteFile(at, v.Marshal(), 0o644))

	vs, err = pdcli.Gen{Layout: l}.Check(t.Context())
	x.NoError(err)
	x.Equal([]pdcli.Changed{{Path: "proto/app/payday/past.lock", How: pdcli.Stale}}, vs)

	x.NoError(os.Remove(at))

	vs, err = pdcli.Gen{Layout: l}.Check(t.Context())
	x.NoError(err)
	x.Equal([]pdcli.Changed{{Path: "proto/app/payday/past.lock", How: pdcli.Added}}, vs)
}

// TestARefusedSchemaIsNotLocked: what the lock records is what payday
// generated from, so an event message `pdgen.Read` refuses is said by the
// step that generates the code, in the plugin's words, and nothing of it
// reaches the lock.
//
// The one such message the lock step meets is in a contract's overlay: one
// in the app's own files is refused when the contracts are generated, before
// the lock is reached, and an overlay is merged into a contract after that.
// This one holds the contract's own Ref, a slug and all.
func TestARefusedSchemaIsNotLocked(t *testing.T) {
	x := require.New(t)

	l := genApp(t)
	before, _ := lockOf(t, l)

	p := l.Path(pdcli.DirExt, "app", "robot_svc.ext.proto")
	b, err := os.ReadFile(p)
	x.NoError(err)
	src := strings.Replace(string(b), "import \"app/robot.proto\";\n", "import \"app/robot.proto\";\nimport \"payday.proto\";\n", 1)
	x.NoError(os.WriteFile(p, []byte(src+`
message RobotTouched {
  option (payday.event) = {};
  RobotRef robot = 1;
}
`), 0o644))

	err = pdcli.Gen{Layout: l}.Run(t.Context())
	x.Error(err, "an event message holding a Ref was generated")
	x.Contains(err.Error(), "messages, servers, ent schema: ")
	x.Contains(err.Error(), "app.RobotTouched.robot: holds app.RobotRef, a message of a generated contract (app/robot_svc.g.proto), in an event message")

	after, _ := lockOf(t, l)
	x.Equal(before, after, "a schema the generator refused was locked")
}
