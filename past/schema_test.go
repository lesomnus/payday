package past

import (
	"context"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// thingProto is the resource these tests write facts of: one field of every
// kind a fact holds, with the options the ORM types them by -- a key, a
// version, an immutable edge, a nullable one, an identifier.
//
// It is compiled here rather than generated, because what a fact is typed by
// is a descriptor and nothing else: this package names no Go type of an app's.
const thingProto = `
edition = "2023";
package past.test;

import "google/protobuf/duration.proto";
import "google/protobuf/timestamp.proto";
import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;

enum Mode {
  MODE_UNSPECIFIED = 0;
  MODE_IDLE = 1;
  MODE_RUN = 2;
}

message Other {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true}];
  string alias = 4;

  option (orm.message) = {};
}

message Thing {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true}];
  Other home = 2 [(orm.edge) = {immutable: true}];

  string name = 5;
  int64 count = 8;
  uint64 big = 9;
  double load = 10;
  float ratio = 11;
  bool on = 12;
  bytes blob = 16;
  bytes maker_id = 17 [(orm.field) = {type: TYPE_UUID, nullable: true}];
  Other near = 18 [(orm.edge) = {nullable: true}];
  Mode mode = 19;
  google.protobuf.Timestamp at = 20 [(orm.field) = {nullable: true}];
  Spec spec = 21;
  repeated string tags = 22;
  map<string, string> settings = 23;
  string person = 24;
  map<string, string> contacts = 25;
  int32 small = 26;
  sint64 low = 27;
  int32 explicit = 28 [features.field_presence = EXPLICIT];
  map<int32, Spec> slots = 29;
  bytes secret = 30;

  google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true}];

  message Spec {
    uint32 axes = 1;
    double reach = 2;
    repeated string notes = 3;
    map<string, int64> parts = 4;
    google.protobuf.Timestamp due = 5;
    uint32 tenth = 10;
  }

  option (orm.message) = {};
}

message Happened {
  option (payday.event) = {kind: "thing.happen"};

  bytes thing = 1;
}

message Named {
  option (payday.event) = {};

  bytes thing = 1;
}

message Plain {
  bytes thing = 1;
}

// What a sentence nothing says is made of: one field of every kind an event
// message holds.
message Serviced {
  option (payday.event) = {kind: "thing.service"};

  bytes thing = 1;
  bytes by = 2;
  Mode mode = 3;
  repeated Part parts = 4;
  map<string, Part> swapped = 5;
  google.protobuf.Timestamp due = 6;
  string note = 7;
  bytes blob = 8;
  google.protobuf.Duration took = 9;
  double load = 10;
  bool done = 11;
  int64 count = 12;
  float ratio = 13;
  uint32 left = 14;

  message Part {
    string serial = 1;
    uint32 count = 2;
  }
}
`

// testFile is [thingProto], compiled once.
var testFile = func() protoreflect.FileDescriptor {
	c := protocompile.Compiler{
		Resolver: protocompile.CompositeResolver{
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{"past/test.proto": thingProto}),
			},
			// `orm.proto` and `payday.proto` as this binary links them, so
			// that an option read off a descriptor is of the linked type.
			protocompile.ResolverFunc(func(p string) (protocompile.SearchResult, error) {
				fd, err := protoregistry.GlobalFiles.FindFileByPath(p)
				if err != nil {
					return protocompile.SearchResult{}, err
				}
				return protocompile.SearchResult{Desc: fd}, nil
			}),
		},
	}

	fs, err := c.Compile(context.Background(), "past/test.proto")
	if err != nil {
		panic(err)
	}

	// Through its wire form, so that the options come back as the extensions
	// this binary has linked rather than as dynamic messages.
	b, err := proto.Marshal(protodesc.ToFileDescriptorProto(fs[0]))
	if err != nil {
		panic(err)
	}
	v := &descriptorpb.FileDescriptorProto{}
	if err := proto.Unmarshal(b, v); err != nil {
		panic(err)
	}
	fd, err := protodesc.NewFile(v, protoregistry.GlobalFiles)
	if err != nil {
		panic(err)
	}

	return fd
}()

func message(name string) protoreflect.MessageDescriptor {
	return testFile.Messages().ByName(protoreflect.Name(name))
}

var (
	thingMd = message("Thing")
	otherMd = message("Other")
	specMd  = thingMd.Messages().ByName("Spec")
)

// field is the field `name` of a Thing.
func field(name string) protoreflect.FieldDescriptor {
	fd := thingMd.Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		panic("no field " + name)
	}

	return fd
}

func newThing() protoreflect.Message { return dynamicpb.NewMessage(thingMd) }

func newOther(id []byte) protoreflect.Message {
	m := dynamicpb.NewMessage(otherMd)
	m.Set(otherMd.Fields().ByName("id"), protoreflect.ValueOfBytes(id))
	return m
}

// described is a Thing as its entity would declare it: `person` and
// `contacts` about a person, and `secret` never answered with.
func described() Description {
	return Description{
		Message:  thingMd,
		Secret:   []protoreflect.FieldDescriptor{field("secret")},
		Personal: []protoreflect.FieldDescriptor{field("person"), field("contacts")},
	}
}
