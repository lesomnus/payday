package pdtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

// At is a time no clock will answer with, for a test that hands one in.
//
// It is here rather than in each test because a golden file holds it, and a
// golden file written against one date and read against another is a diff
// nobody meant. Anything works; what matters is that every test uses the same
// one, so a file moved between them still reads.
var At = time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)

// Clock answers with At, forever.
//
//	sink, err := pd.NewSink(db, bare.WithClock(pdtest.Clock()))
//
// A constant rather than something that advances, because what a golden file is
// for is the answer and not the ordering. A test about ordering wants a clock
// it moves itself, which is a closure over a variable and needs nothing from
// here.
func Clock() func() time.Time { return func() time.Time { return At } }

// Golden compares a message against the file recorded for this test, and writes
// that file when [Update] is set in the environment.
//
//	PDTEST_UPDATE=1 go test ./...
//
// # Why a whole message rather than the fields a test cares about
//
// Because the fields a test cares about are the ones somebody thought of. A
// generated server answers with everything the schema declared, and the way
// that answer goes wrong is a field nobody was asserting -- an edge that came
// back empty, a version that did not move, a column the overlay renamed. A
// comparison of the whole message is the only assertion that covers a field
// added after it was written.
//
// # What makes it possible at all
//
// Two seams, and neither is here. `bare.WithMinter` decides the key of a row,
// so a test can say what identifier it gets; `bare.WithClock` says what time it
// is, so a test can say what a stamp holds. Without both, an answer differs on
// every run and there is nothing to write down.
//
// A test that hands in neither will find this failing on the first line of the
// diff, which is the right way to be told.
//
// # Text and not bytes
//
// prototext, so a failure is a diff a person reads. The binary encoding is not
// stable across protobuf versions in a way that would matter here -- field
// order is -- but a golden file is read by whoever the test failed for, and
// bytes are read by nobody.
func Golden(tb testing.TB, m proto.Message) {
	tb.Helper()

	want := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Format(m)

	// Deterministic, which prototext deliberately is not: it inserts a random
	// space so that nobody depends on its exact output. A golden file is
	// exactly that dependency, honestly declared, so the randomness is undone
	// rather than worked around.
	want = strings.ReplaceAll(want, "  ", " ")

	p := filepath.Join("testdata", strings.ReplaceAll(tb.Name(), "/", "_")+".txtpb")
	if update() {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
			tb.Fatal(err)
		}

		return
	}

	b, err := os.ReadFile(p)
	if err != nil {
		tb.Fatalf("%s: %v\n\n    %s=1 go test ./...\n\nwrites it", p, err, Update)
	}

	if got := string(b); got != want {
		tb.Errorf("%s does not match.\n\n--- recorded\n%s\n--- answered\n%s", p, got, want)
	}
}

// Update is the environment variable that has [Golden] write rather than
// compare.
//
// The environment rather than a flag, which is the usual Go idiom, because a
// flag has to be **registered** -- `go test` refuses one it has not been told
// about, before any of this runs. Registering it here would put it on every
// binary that imports this package, including the ones that never call
// [Golden], and a flag nothing reads is one somebody passes expecting
// something. A variable is read where it is used and nowhere else.
const Update = "PDTEST_UPDATE"

func update() bool {
	v := os.Getenv(Update)

	return v != "" && v != "0" && v != "false"
}
