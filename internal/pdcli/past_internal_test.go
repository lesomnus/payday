package pdcli

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/schema"
)

// TestThePastThatIsCopiedIsThePastThatShips: `pd gen` copies the files it
// names, and payday ships what is in `schema/past`. A file added there and not
// here would be a past an app declares and does not get -- which `CheckOwn`
// refuses, after the copy is written without it.
func TestThePastThatIsCopiedIsThePastThatShips(t *testing.T) {
	x := require.New(t)

	es, err := fs.ReadDir(schema.FS(), schema.DirPast)
	x.NoError(err)

	ships := []string{}
	for _, e := range es {
		ships = append(ships, e.Name())
	}

	names := []string{}
	for _, v := range pastFiles {
		names = append(names, v.Name)
	}
	x.ElementsMatch(ships, names)

	opted, err := schema.Opted()
	x.NoError(err)
	for _, v := range pastFiles {
		x.True(opted[v.Own], "%s is copied as the past's and is not", v.Own)
	}
	features, err := schema.Features()
	x.NoError(err)
	pastCount := 0
	for _, feature := range features {
		if feature == schema.DirPast {
			pastCount++
		}
	}
	x.Equal(len(pastFiles), pastCount)
}

// TestAnAppWithNoPastHasNoNamesTakenFromIt: an `Event` of an app's own, or a
// file of its own called `event.proto`, is somebody else's business until the
// app declares a past -- a calendar's events are its own -- and a message
// nested in another is named after what it is in.
func TestAnAppWithNoPastHasNoNamesTakenFromIt(t *testing.T) {
	x := require.New(t)

	root := t.TempDir()
	x.NoError(os.MkdirAll(filepath.Join(root, DirProto, "app"), 0o755))
	for name, src := range map[string]string{
		"event.proto": "edition = \"2023\";\npackage app;\nmessage Event {}\n",
		"robot.proto": "edition = \"2023\";\npackage app;\nmessage Robot {\n  message Fact {}\n}\n",
	} {
		x.NoError(os.WriteFile(filepath.Join(root, DirProto, "app", name), []byte(src), 0o644))
	}

	l := Layout{Root: root, ProtoPkg: "app"}
	x.NoError(checkPastNames(l), "an app with no past was refused its own Event")

	// With one, the file and the message are both refused, and the message
	// nested in a robot is not.
	l.Past = &Past{Event: 17, Fact: 16, At: "proto/app/app.proto"}
	err := checkPastNames(l)
	x.Error(err)
	x.Contains(err.Error(), "proto/app/event.proto")

	x.NoError(os.Remove(filepath.Join(root, DirProto, "app", "event.proto")))
	x.NoError(checkPastNames(l), "a message nested in another was taken for the past's")

	// And a file nothing can parse is buf's to say.
	x.NoError(os.WriteFile(filepath.Join(root, DirProto, "app", "broken.proto"), []byte("message {"), 0o644))
	x.NoError(checkPastNames(l))
}
