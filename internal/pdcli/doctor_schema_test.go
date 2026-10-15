package pdcli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// `pd doctor` reads the app's own schema, which it said it did and did not.
//
// The sentence in its own comment was *reads the schema the way the generator
// does, so that everything `pd gen` refuses is refused here too*, and what
// stood under it globbed payday's shipped entity files and compared overlay
// filenames. An entity `pd gen` refuses outright got *looks like an app that
// generates* and exit 0 -- which is worse than not checking, because the exit
// code is the part somebody trusts.
//
// Run against this repository's own reference app rather than a fixture: it is
// a real app with a real `buf.lock`, and a check whose whole job is to compile
// a schema has to be given one that compiles.

func apptest(t *testing.T, x *require.Assertions) pdcli.Layout {
	t.Helper()

	dir, err := filepath.Abs(filepath.Join("..", "apptest"))
	x.NoError(err)

	l, err := pdcli.Discover(dir)
	x.NoError(err)

	return l
}

// TestTheReferenceAppIsWell is the other half, and it is the half that fails if
// this check learns to complain about something ordinary.
func TestTheReferenceAppIsWell(t *testing.T) {
	x := require.New(t)

	x.NotContains(found(t, apptest(t, x)), "pd gen would refuse")
}

// TestDoctorRefusesWhatGenRefuses, on the one kind of mistake that is cheapest
// to make and most expensive to find: two entities on one domain.
func TestDoctorRefusesWhatGenRefuses(t *testing.T) {
	x := require.New(t)
	l := apptest(t, x)

	// Written into the app rather than into a copy of it, because what is
	// being tested is a `buf build` of a real workspace and a copy without one
	// is a different app. Removed on cleanup, which runs on failure too.
	at := l.Path(pdcli.DirProto, "app", "clash.proto")
	x.NoError(os.WriteFile(at, []byte(clash), 0o644))
	t.Cleanup(func() { _ = os.Remove(at) })

	// The same words `pd gen` uses, because it is the same reader.
	x.Contains(found(t, l), "both declare domain 14")
}

func TestDoctorRefusesAnAppFieldInAReservedHeaderSlot(t *testing.T) {
	x := require.New(t)
	l := apptest(t, x)
	at := l.Path(pdcli.DirProto, "app", "reserved_header.proto")
	src := strings.Replace(clash, "message Clash", "message Binding", 1)
	src = strings.Replace(src, "domain: 14", "domain: 200", 1)
	src = strings.Replace(src, "global: {}", "global: {}\n    erase: {hard: {}}", 1)
	src = strings.Replace(src, "  google.protobuf.Timestamp", "  string binding_id = 4;\n\n  google.protobuf.Timestamp", 1)
	x.NoError(os.WriteFile(at, []byte(src), 0o644))
	t.Cleanup(func() { _ = os.Remove(at) })

	x.Contains(found(t, l), "reserved for alias")
}

// clash is `Seal`'s domain, on a second entity.
const clash = `edition = "2023";

package app;

import "google/protobuf/timestamp.proto";
import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "github.com/lesomnus/payday/internal/apptest";

message Clash {
  bytes id = 1 [(orm.field) = {
    type: TYPE_UUID
    key: true
    default: ""
  }];

  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];

  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {
    domain: 14
    global: {}
  };
}
`
