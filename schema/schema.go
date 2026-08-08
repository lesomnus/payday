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
	"sync"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
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
	owned map[string]map[int32]Field
	fail  error
)

// Owned answers with the fields payday declared, by entity full name and then
// by number.
//
// The numbers are the whole of payday's half of the contract. An app adds its
// own outside them; what it must not do is redeclare one of these, and
// [CheckOverlay] is what makes that something other than a promise.
func Owned() (map[string]map[int32]Field, error) {
	once.Do(func() { owned, fail = read() })
	return owned, fail
}

func read() (map[string]map[int32]Field, error) {
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

	vs := map[string]map[int32]Field{}
	for _, fd := range fds {
		ms := fd.Messages()
		for i := range ms.Len() {
			m := ms.Get(i)
			fs := map[int32]Field{}
			for j := range m.Fields().Len() {
				f := m.Fields().Get(j)
				v := Field{Name: string(f.Name()), Kind: f.Kind()}
				if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
					v.Message = string(f.Message().FullName())
				}
				fs[int32(f.Number())] = v
			}

			vs[string(m.FullName())] = fs
		}
	}

	return vs, nil
}
