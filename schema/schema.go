// Package schema holds payday's own entities and what they are allowed to
// become in an app.
//
// The `.proto` files beside this are sources an app generates from rather than
// a module it imports; see README.md. What this package adds is the ability to
// check what an app did to them, which needs the originals to compare against
// -- so they are embedded here, and the check reads the same bytes that were
// copied.
package schema

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/lesomnus/payday/pdpb"
)

//go:embed payday/*.proto
var files embed.FS

// FS is payday's entities as they ship, for whatever copies them.
func FS() fs.FS { return files }

// Field is what payday declared at one number.
type Field struct {
	Name string
	Kind protoreflect.Kind

	// Message is the full name of the message this field holds, empty for a
	// scalar. It is compared as well because a number can keep its name and
	// change what it points at.
	Message string
}

var (
	once  sync.Once
	owned map[pdpb.Own]map[int32]Field
	fail  error
)

// Owned answers with the fields payday declared, by the marker each entity
// carries and then by number.
//
// The numbers are the whole of payday's half of the contract. An app adds its
// own outside them; what it must not do is redeclare one of these, and
// [CheckOverlay] is what makes that something other than a promise.
// Keyed on [pdpb.Own] and not on the full name, so that an app may put these
// entities in its own proto package. The name was the thing that made the
// package payday's rather than the app's; the marker travels with the message.
func Owned() (map[pdpb.Own]map[int32]Field, error) {
	once.Do(func() { owned, fail = read() })
	return owned, fail
}

func read() (map[pdpb.Own]map[int32]Field, error) {
	names := []string{}
	es, err := files.ReadDir("payday")
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		names = append(names, path.Join("payday", e.Name()))
	}

	c := protocompile.Compiler{
		Resolver: protocompile.CompositeResolver{
			&protocompile.SourceResolver{
				Accessor: func(p string) (io.ReadCloser, error) { return files.Open(p) },
			},
			protocompile.ResolverFunc(func(p string) (protocompile.SearchResult, error) {
				fd, err := protoregistry.GlobalFiles.FindFileByPath(p)
				if err != nil {
					return protocompile.SearchResult{}, err
				}
				return protocompile.SearchResult{Desc: fd}, nil
			}),
		},
	}

	fds, err := c.Compile(context.Background(), names...)
	if err != nil {
		return nil, fmt.Errorf("read payday's own entities: %w", err)
	}

	vs := map[pdpb.Own]map[int32]Field{}
	for _, fd := range fds {
		// The compiler resolves `payday.proto` out of the global registry and
		// makes a *dynamicpb.Message of an option written against it, which
		// `proto.GetExtension` panics on when handed the concrete type. A
		// round-trip through the wire form brings it back as the linked one.
		own, err := ownOf(fd)
		if err != nil {
			return nil, err
		}

		ms := fd.Messages()
		for i := range ms.Len() {
			m := ms.Get(i)
			fs := map[int32]Field{}
			for j := range m.Fields().Len() {
				f := m.Fields().Get(j)
				v := Field{Name: string(f.Name()), Kind: f.Kind()}
				if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
					v.Message = Relative(string(f.Message().FullName()), string(fd.Package()))
				}
				fs[int32(f.Number())] = v
			}

			k, ok := own[string(m.Name())]
			if !ok {
				// A message payday ships that is not one of its entities.
				continue
			}

			vs[k] = fs
		}
	}

	return vs, nil
}

// ownOf is the marker each top-level message of a file declares, by message
// name, leaving out the ones that declare none.
//
// It reads the descriptor back through its wire form because the options on the
// compiled one are dynamic; see the note at the call site.
func ownOf(fd protoreflect.FileDescriptor) (map[string]pdpb.Own, error) {
	b, err := proto.Marshal(protodesc.ToFileDescriptorProto(fd))
	if err != nil {
		return nil, fmt.Errorf("hold %s: %w", fd.Path(), err)
	}

	// The default resolver is the global type registry, which is where the
	// linked extensions are.
	v := &descriptorpb.FileDescriptorProto{}
	if err := proto.Unmarshal(b, v); err != nil {
		return nil, fmt.Errorf("read %s back: %w", fd.Path(), err)
	}

	vs := map[string]pdpb.Own{}
	for _, m := range v.GetMessageType() {
		e, _ := proto.GetExtension(m.GetOptions(), pdpb.E_Entity).(*pdpb.Entity)
		if k := e.GetOwn(); k != pdpb.Own_OWN_UNSPECIFIED {
			vs[m.GetName()] = k
		}
	}

	return vs, nil
}

// Relative is a message's name with `pkg` taken off the front, and the whole
// name when it is not in that package.
//
// It is what makes a field's type comparable across a rename. payday's Holder
// points at `payday.Tenant` and an app that put these entities in its own
// package points at `hday.Tenant` -- the same field, and by full name a
// redeclaration of it. A map field is the sharper case, since its entry type is
// synthesized from the message it is in: `payday.Tenant.LabelsEntry` against
// `hday.Tenant.LabelsEntry`.
//
// What is left alone is everything outside the package, `google.protobuf.Timestamp`
// most of all -- those are the same name on both sides and have to stay
// comparable as one.
func Relative(name string, pkg string) string {
	return strings.TrimPrefix(name, pkg+".")
}
