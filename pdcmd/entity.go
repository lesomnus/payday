package pdcmd

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// Entity is one thing this app stores, and the service that answers about it.
//
// It is read out of the process rather than generated, because everything it
// holds is already there: an app's `.pb.go` files register their descriptors at
// init, and the `(payday.entity)` option travels with them. What generation
// would add is a second copy of a list the binary already has.
type Entity struct {
	// Name is what a person types: "robot" for `app.Robot`, and whatever the
	// schema declared where it declared one -- an entity that says
	// `name: "key"` is `key` on the command line and `#key` in a slug, which
	// is the point of saying it once. See [pdid.Name].
	Name string

	Domain  pdid.Domain
	Message protoreflect.MessageDescriptor

	// Service is the generated contract, or nil when the entity has none --
	// which happens: an entity can be stored and not served.
	Service protoreflect.ServiceDescriptor
}

// Method answers with a method of this entity's service, or nil.
//
// The nil is the point of this whole type. Not every entity has every verb:
// `List` is generated only where the schema declared `list:`, and in payday's
// own test app four entities of eleven have it. A command tree that assumed the
// verb would build `robot ls` for a service with no `List` and fail at the
// call, naming a method the server is right to say it does not have.
func (e Entity) Method(name string) protoreflect.MethodDescriptor {
	if e.Service == nil {
		return nil
	}

	return e.Service.Methods().ByName(protoreflect.Name(name))
}

// Entities lists what is linked into this binary, in the proto package given.
//
// # Why a package has to be named
//
// Two payday apps can share a process -- that is the whole reason an app may
// choose its own proto package -- and when they do, this registry holds both.
// An app that embeds another is such a process: `directory.Holder` and
// `fleet.Holder` are both here, and a tree built from everything would have two
// `holder` commands pointed at two different servers.
//
// The connection cannot answer which is wanted: it is a `grpc.ClientConnInterface`
// and knows nothing about schemas. So the caller says, and [New] guesses only
// when there is nothing to guess between.
func Entities(pkg protoreflect.FullName) []Entity {
	vs := []Entity{}

	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != pkg {
			return true
		}

		ms := fd.Messages()
		for i := range ms.Len() {
			m := ms.Get(i)

			opts, _ := proto.GetExtension(m.Options(), pdpb.E_Entity).(*pdpb.Entity)
			if opts == nil {
				continue
			}

			e := Entity{
				// What the schema said this is called, which is the same
				// question the `#word` of a slug asks -- so it is the same
				// answer, from [pdid.Name], rather than a second derivation
				// that happens to agree on the entities anybody has tried.
				Name:    pdid.Name(opts.GetName(), string(m.Name())),
				Domain:  pdid.Domain(opts.GetDomain()),
				Message: m,
			}

			// The contract is named after the entity -- `RobotService` for
			// `Robot` -- while the *file* it lands in is named after the file
			// the entity was declared in. So this looks the service up by name
			// and does not go looking through files.
			if d, err := protoregistry.GlobalFiles.FindDescriptorByName(m.FullName() + "Service"); err == nil {
				if sd, ok := d.(protoreflect.ServiceDescriptor); ok {
					e.Service = sd
				}
			}

			vs = append(vs, e)
		}

		return true
	})

	slices.SortFunc(vs, func(a, b Entity) int { return strings.Compare(a.Name, b.Name) })

	return vs
}

// Packages lists the proto packages in this process that hold payday entities.
//
// For [New] to guess with, and for its refusal to name what it found.
func Packages() []protoreflect.FullName {
	seen := map[protoreflect.FullName]bool{}

	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		ms := fd.Messages()
		for i := range ms.Len() {
			if opts, _ := proto.GetExtension(ms.Get(i).Options(), pdpb.E_Entity).(*pdpb.Entity); opts != nil {
				seen[fd.Package()] = true
				break
			}
		}

		return true
	})

	vs := make([]protoreflect.FullName, 0, len(seen))
	for k := range seen {
		vs = append(vs, k)
	}
	slices.SortFunc(vs, func(a, b protoreflect.FullName) int { return strings.Compare(string(a), string(b)) })

	return vs
}

// ownPackage is the package a file declared as the app's own, with
// `option (payday.app)`.
//
// An app may hold entities in more than one package -- a shared name for the
// thing several services are about, beside what one service keeps to itself --
// and then the packages are not apps and counting them says nothing. The file
// that says which is the app's is the same one `pd gen` reads at build time, so
// the two cannot drift.
//
// Two of them is two apps genuinely linked into one process, which is what the
// refusal below is for.
func ownPackage() (protoreflect.FullName, bool) {
	seen := map[protoreflect.FullName]bool{}

	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if proto.HasExtension(fd.Options(), pdpb.E_App) {
			seen[fd.Package()] = true
		}

		return true
	})
	if len(seen) != 1 {
		return "", false
	}

	for k := range seen {
		return k, true
	}

	return "", false
}

// solePackage is the one package with entities, or a refusal naming them all.
func solePackage() (protoreflect.FullName, error) {
	if v, ok := ownPackage(); ok {
		return v, nil
	}

	vs := Packages()
	switch len(vs) {
	case 1:
		return vs[0], nil

	case 0:
		return "", fmt.Errorf("pdcmd: no payday entities are linked into this binary")

	default:
		names := make([]string, len(vs))
		for i, v := range vs {
			names[i] = string(v)
		}

		return "", fmt.Errorf(
			"pdcmd: %d payday apps are in this process (%s), so which one this connection speaks to has to be said: use NewIn",
			len(vs), strings.Join(names, ", "))
	}
}
