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

	v := found(t, apptest(t, x))
	x.NotContains(v, "pd gen would refuse")
	x.NotContains(v, "says nothing of", "every field of the reference app that may hold a person says which it is")

	// It keeps a past, and what it keeps is as the doctor would have it: every
	// column that names an event says so, no event message holds a person,
	// the lock is what a generation writes, and no transport of its page
	// speaks JSON without the registry.
	x.NotContains(v, "named for an event")
	x.NotContains(v, "named for something about a person")
	x.NotContains(v, "past.lock")
	x.NotContains(v, "hands it no registry")

	// And it declares meters (payday#186), which its server reads, with a
	// sink, in its spin list.
	x.NotContains(v, "nothing reads them")
	x.NotContains(v, "meter.Reader")

	// And entitlements (payday#185), whose copy a follower keeps, with a
	// source and a copy, in its spin list.
	x.NotContains(v, "nothing keeps the copy")
	x.NotContains(v, "entitle.Follower")

	// And its holders are memberships of accounts (payday#184): the one
	// method an account calls as itself says what it reads, and its server
	// resolves a call by them.
	x.NotContains(v, "declare nothing they read")
	x.NotContains(v, "no resolver serves a call as one")
}

// TestDoctorAsksWhatAFieldOfAnEntityThatHoldsPeopleIs: an entity with a
// personal field holds people, and a field of free text on it that says
// nothing is one the trail keeps as a value. `personal: false` is the answer
// that is not "personal", and it is an answer.
func TestDoctorAsksWhatAFieldOfAnEntityThatHoldsPeopleIs(t *testing.T) {
	x := require.New(t)
	l := apptest(t, x)
	at := l.Path(pdcli.DirProto, "app", "visit.proto")
	t.Cleanup(func() { _ = os.Remove(at) })

	x.NoError(os.WriteFile(at, []byte(visit("string purpose = 8;")), 0o644))
	v := found(t, l)
	x.Contains(v, "app.Visit holds people and says nothing of purpose")
	x.Contains(v, "proto/app/visit.proto:18  purpose", "where it is declared, to the line")

	x.NoError(os.WriteFile(at, []byte(visit("string purpose = 8 [(payday.field) = {personal: false}];")), 0o644))
	x.NotContains(found(t, l), "app.Visit")
}

// TestDoctorAsksAboutAFieldNamedForSomebody: an entity that declares nothing
// is not one the rule above reaches, and a field whose name is about a person
// is asked about anyway -- a word of the name, and not a part of a word.
func TestDoctorAsksAboutAFieldNamedForSomebody(t *testing.T) {
	x := require.New(t)
	l := apptest(t, x)
	at := l.Path(pdcli.DirProto, "app", "visit.proto")
	t.Cleanup(func() { _ = os.Remove(at) })

	src := visit("string host_email = 8;\n  string hotel = 10;")
	src = strings.Replace(src, "string visitor = 9 [(payday.field) = {personal: true}];", "", 1)
	x.NoError(os.WriteFile(at, []byte(src), 0o644))
	v := found(t, l)
	x.Contains(v, "app.Visit says nothing of host_email, which is named for something about a person")
	x.NotContains(v, "hotel")
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

// TestDoctorAsksAboutAKindKeptOffTheTrailThatHoldsASecret: a kind a check of
// the rows does not read, whose rows are a way in, is one whose row added in
// the database nothing finds (payday#115). The doctor says so, with the reason
// the schema gave and where the secret is; recorded instead, it says nothing.
func TestDoctorAsksAboutAKindKeptOffTheTrailThatHoldsASecret(t *testing.T) {
	x := require.New(t)
	l := apptest(t, x)
	at := l.Path(pdcli.DirProto, "app", "token.proto")
	t.Cleanup(func() { _ = os.Remove(at) })

	x.NoError(os.WriteFile(at, []byte(keyed(`off: {why: "a key, rotated every hour"}`)), 0o644))
	v := found(t, l)
	x.Contains(v, "app.Token is kept off the trail and holds a secret (secret)")
	x.Contains(v, `off: {why: "a key, rotated every hour"}`)
	x.Contains(v, "proto/app/token.proto:", "where the secret is declared")

	x.NoError(os.WriteFile(at, []byte(keyed(`recorded: {}`)), 0o644))
	x.NotContains(found(t, l), "app.Token")
}

// keyed is an entity no server writes, holding a secret, and saying `trail`
// how its writes reach the trail.
func keyed(trail string) string {
	return `edition = "2023";

package app;

import "google/protobuf/timestamp.proto";
import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "github.com/lesomnus/payday/internal/apptest";

message Token {
  bytes id = 1 [(orm.field) = {
    type: TYPE_UUID
    key: true
    default: ""
  }];
  bytes secret = 8 [(payday.field) = {secret: true}];

  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];

  option (orm.message) = {};
  option (payday.entity) = {
    domain: 202
    global: {}
    erase: {hard: {}}
    trail: {` + trail + `}
  };
}
`
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

// visit is an entity with a personal field, and `field` beside it.
func visit(field string) string {
	return `edition = "2023";

package app;

import "google/protobuf/timestamp.proto";
import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "github.com/lesomnus/payday/internal/apptest";

message Visit {
  bytes id = 1 [(orm.field) = {
    type: TYPE_UUID
    key: true
    default: ""
  }];
  ` + field + `
  string visitor = 9 [(payday.field) = {personal: true}];

  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];

  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {
    domain: 201
    global: {}
    erase: {hard: {}}
  };
}
`
}
