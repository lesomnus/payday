package pdcmd

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// registerApp puts one payday app into the process the way a generated
// `.pb.go` does at init: a file in the global registry whose message carries
// `(payday.entity)`. Discovery reads descriptors and never Go types -- see
// [Entities] -- so a descriptor built by hand is indistinguishable from a
// linked app, and this binary gets its second app without a second generated
// module to link.
//
// The path is `<pkg>/payday/holder.proto` because that is the shape `pd gen`
// writes and the reason it writes it: every app copies `payday/holder.proto`,
// and it is the app's package on the front that keeps two copies from being
// one path.
func registerApp(pkg, path string, domain uint32) error {
	opts := &descriptorpb.MessageOptions{}
	proto.SetExtension(opts, pdpb.E_Entity, pdpb.Entity_builder{Domain: domain}.Build())

	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String(path),
		Package: proto.String(pkg),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:    proto.String("Holder"),
			Options: opts,
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		return err
	}

	return protoregistry.GlobalFiles.RegisterFile(fd)
}

// The global registry is append-only, so what this file adds cannot be taken
// back out -- and half of what is under test is the state *between* the two
// additions. So everything order-dependent runs once and is remembered, and
// the tests assert over the record: a second run in the same process
// (`-count=2`) reads it instead of registering the same paths again, and the
// tests do not care which of them ran first.
var twoApps struct {
	once sync.Once

	// The registry before this file touched it, and New's answer then.
	before  []protoreflect.FullName
	nothing error

	// What RegisterFile said, in the order the apps arrived: fleet first,
	// so that the order Packages answers in is provably the sorted one and
	// not the arrival one.
	directory, fleet error

	// New's answer while fleet was the only app in the process.
	guess    *Tree
	guessErr error

	// And its answer once directory joined.
	refusal error
}

func loadTwoApps() {
	twoApps.once.Do(func() {
		twoApps.before = Packages()
		_, twoApps.nothing = New(Static(nil))

		twoApps.fleet = registerApp("pdcmdtest.fleet", "pdcmdtest.fleet/payday/holder.proto", 212)
		twoApps.guess, twoApps.guessErr = New(Static(nil))

		twoApps.directory = registerApp("pdcmdtest.directory", "pdcmdtest.directory/payday/holder.proto", 211)
		_, twoApps.refusal = New(Static(nil))
	})
}

// TestASecondAppsCopiesLandBesideTheFirsts.
//
// MIGRATING's rule -- two payday apps share a process when their proto
// packages differ -- rests on two non-collisions, and this is both of them
// run rather than claimed. The registry keys files by path and the fixture
// paths carry the package, so the second `payday/holder.proto` registers
// instead of panicking before `main`; and the messages are
// `pdcmdtest.directory.Holder` and `pdcmdtest.fleet.Holder`, so each package
// reads back its own entity and not one row seen twice.
func TestASecondAppsCopiesLandBesideTheFirsts(t *testing.T) {
	loadTwoApps()
	x := require.New(t)

	x.Empty(twoApps.before,
		"this binary links no generated app, which is what lets the fixture be the whole registry")
	x.NoError(twoApps.directory)
	x.NoError(twoApps.fleet, "the second holder.proto is a new path, not a duplicate")

	x.Equal([]protoreflect.FullName{"pdcmdtest.directory", "pdcmdtest.fleet"}, Packages())

	for pkg, domain := range map[protoreflect.FullName]pdid.Domain{
		"pdcmdtest.directory": 211,
		"pdcmdtest.fleet":     212,
	} {
		es := Entities(pkg)
		x.Len(es, 1)
		x.Equal("holder", es[0].Name)
		x.Equal(domain, es[0].Domain, "each package's Holder is its own")
	}
}

// TestNewRefusesToGuessBetweenTwoApps.
//
// Neither fixture file declares `(payday.app)` -- which is what a process of
// apps that predate the option looks like -- so [New] is down to counting,
// and these are the rows apptest cannot reach: its registry always holds a
// declared package, so its `New` never counts. All three counts are here.
// Nothing linked is its own refusal; one app is not a guess at all; and two
// is the refusal that names them, because a connection cannot say which app
// it speaks to and picking one would build a tree that runs against the
// other.
func TestNewRefusesToGuessBetweenTwoApps(t *testing.T) {
	loadTwoApps()
	x := require.New(t)

	x.ErrorContains(twoApps.nothing, "no payday entities")

	x.NoError(twoApps.guessErr, "one app alone leaves nothing to guess between")
	x.Equal(protoreflect.FullName("pdcmdtest.fleet"), twoApps.guess.pkg)

	x.ErrorContains(twoApps.refusal, "2 payday apps")
	// Both names, and in sorted order rather than the order they arrived --
	// the registry walk is a map walk, and a refusal that named the apps in a
	// different order each run could not be read twice the same way.
	x.ErrorContains(twoApps.refusal, "(pdcmdtest.directory, pdcmdtest.fleet)")
	// And it says what to do instead, because there is a right thing to do:
	// a process with two apps wants two trees.
	x.ErrorContains(twoApps.refusal, "use NewIn")
}
