package pdcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The verbs this package knows how to build, in the order they are shown.
//
// `Apply` is deliberately absent. It is one of payday's two general writes and
// is closed unless an app opts in, so a command for it would be one that fails
// on every app that took the default -- and the apps that did opt in have a
// reason of their own, which is a command they should write. `Watch` is absent
// because it is a stream, and a stream is not this shape.
var verbs = []struct {
	name   string
	method string
	build  func(e Entity, md protoreflect.MethodDescriptor, r *runner) *xli.Command
}{
	{"get", "Get", cmdGet},
	{"ls", "List", cmdList},
	{"add", "Add", cmdAdd},
	{"patch", "Patch", cmdPatch},
	{"erase", "Erase", cmdErase},
}

// runner is what every built command closes over: where to send the call and
// how to show the answer.
type runner struct {
	open Opener
	opts Options
}

// run is the last three lines of every command, kept in one place because they
// are the three that must not differ between them: merge the raw request, call,
// print what came back.
func (r *runner) run(ctx context.Context, cmd *xli.Command, md protoreflect.MethodDescriptor, in proto.Message, raw ...string) error {
	if err := fillRaw(cmd, in, raw...); err != nil {
		return err
	}

	// From the context rather than from here, because [runner.withConn] put it
	// there -- which is what lets a command an app wrote itself reach the same
	// connection; see [Tree.WithConn].
	out, err := call(ctx, MustConn(ctx), md, in)
	if err != nil {
		return err
	}

	p, err := r.printer(cmd)
	if err != nil {
		return err
	}

	return p.Print(cmd, out)
}

func (r *runner) input(md protoreflect.MethodDescriptor) (proto.Message, error) {
	return newMessage(md.Input())
}

func flgOutput(def string) *flg.String {
	return &flg.String{
		Name:  "output",
		Alias: 'o',
		Brief: fmt.Sprintf("one of: pretty, json, prototext, protojson, name, table, wide, template=... (default %s)", def),
	}
}

// flgInput is how the trailing request is read.
//
// Two names and one of them is the default, because the lenient reading cannot
// misread the strict one: a uuid and base64 of sixteen bytes have no string in
// common. So this is not a mode to pick before writing a request -- it is there
// for a caller that wants the contract to be exactly protojson and nothing
// more.
func flgInput() *flg.String {
	return &flg.String{
		Name:  "in",
		Brief: "how REQ is read: json (identifiers may be uuids) or protojson (default json)",
	}
}

func cmdGet(e Entity, md protoreflect.MethodDescriptor, r *runner) *xli.Command {
	return &xli.Command{
		Name:  "get",
		Brief: fmt.Sprintf("get one %s", e.Message.Name()),

		Flags: flg.Flags{flgOutput(r.opts.def()), flgInput()},
		Args:  arg.Args{&ArgRef{Name: "REF"}, argRaw()},

		Handler: xli.Chain(r.withConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			in, err := r.input(md)
			if err != nil {
				return err
			}
			if err := setRef(in, arg.MustGet[Ref](cmd, "REF")); err != nil {
				return err
			}
			if err := r.run(ctx, cmd, md, in); err != nil {
				return err
			}

			return next(ctx)
		})),
	}
}

// cmdList is `ls`, and the two flags are the whole of paging.
//
// `--size` and `--next` rather than a page number, because that is what the
// schema offers: a list carries on from the last row of the answer before, so
// that a row added ahead of a page does not shift it. Anything else the request
// can hold -- filters, ordering -- is the trailing protojson, for the reason
// given on [argRaw]: a flag per filter would be a second copy of the schema.
func cmdList(e Entity, md protoreflect.MethodDescriptor, r *runner) *xli.Command {
	return &xli.Command{
		Name:  "ls",
		Brief: fmt.Sprintf("list %s", e.Message.Name()),

		Flags: flg.Flags{
			flgOutput(r.opts.def()),
			flgInput(),
			&flg.Int{Name: "size", Alias: 'n', Brief: "how many to answer with"},
			&flg.String{Name: "next", Brief: `carry on from the "next" of an earlier answer`},
		},
		Args: arg.Args{argRaw()},

		Handler: xli.Chain(r.withConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			in, err := r.input(md)
			if err != nil {
				return err
			}

			m := in.ProtoReflect()
			fs := m.Descriptor().Fields()
			if v, ok := flg.Find[int](cmd, "size"); ok {
				if fd := fs.ByName("size"); fd != nil {
					m.Set(fd, protoreflect.ValueOfInt32(int32(v)))
				}
			}
			if v, ok := flg.Find[string](cmd, "next"); ok && v != "" {
				if fd := fs.ByName("next"); fd != nil {
					m.Set(fd, protoreflect.ValueOfString(v))
				}
			}

			if err := r.run(ctx, cmd, md, in); err != nil {
				return err
			}

			return next(ctx)
		})),
	}
}

// cmdAdd takes the name the new row is to have, and everything else as
// protojson.
//
// The alias is an argument rather than a flag because it is the one field
// nearly every `add` sets, and it is optional because an entity may have no
// alias at all -- `Reading` in payday's own test app has none.
func cmdAdd(e Entity, md protoreflect.MethodDescriptor, r *runner) *xli.Command {
	return &xli.Command{
		Name:  "add",
		Brief: fmt.Sprintf("add a %s", e.Message.Name()),

		Flags: flg.Flags{flgOutput(r.opts.def()), flgInput()},
		Args:  arg.Args{&arg.String{Name: "NAME", Optional: true}, argRaw()},

		Handler: xli.Chain(r.withConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			in, err := r.input(md)
			if err != nil {
				return err
			}

			// NAME is taken as a string and read here rather than parsed as a
			// [Ref] by the argument itself, because it is optional and the
			// thing after it is not: `holder add -` and `holder add '{...}'`
			// are both requests with no name, and an argument that insisted on
			// parsing the first word as a name would refuse them before this
			// command ever ran.
			raw := []string{}
			if v, ok := arg.Get[string](cmd, "NAME"); ok {
				if isRaw(v) {
					raw = append(raw, v)
				} else {
					ref, err := RefParser{}.Parse(v)
					if err != nil {
						return fmt.Errorf("NAME: %w", err)
					}
					if err := setNew(in, ref); err != nil {
						return err
					}
				}
			}

			if err := r.run(ctx, cmd, md, in, raw...); err != nil {
				return err
			}

			return next(ctx)
		})),
	}
}

func cmdPatch(e Entity, md protoreflect.MethodDescriptor, r *runner) *xli.Command {
	return &xli.Command{
		Name:  "patch",
		Brief: fmt.Sprintf("change one %s", e.Message.Name()),

		Flags: flg.Flags{flgOutput(r.opts.def()), flgInput()},
		Args:  arg.Args{&ArgRef{Name: "REF"}, argRaw()},

		Handler: xli.Chain(r.withConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			in, err := r.input(md)
			if err != nil {
				return err
			}
			if err := setRef(in, arg.MustGet[Ref](cmd, "REF")); err != nil {
				return err
			}
			if err := r.run(ctx, cmd, md, in); err != nil {
				return err
			}

			return next(ctx)
		})),
	}
}

// cmdErase takes a reference and nothing else, which is what the RPC takes.
//
// It still accepts the trailing protojson, so that the shape of these commands
// is one shape. There is nothing useful to put in it here, and a command that
// refused it would be a special case to remember.
func cmdErase(e Entity, md protoreflect.MethodDescriptor, r *runner) *xli.Command {
	return &xli.Command{
		Name:  "erase",
		Brief: fmt.Sprintf("erase one %s", e.Message.Name()),

		Flags: flg.Flags{flgOutput(r.opts.def()), flgInput()},
		Args:  arg.Args{&ArgRef{Name: "REF"}, argRaw()},

		Handler: xli.Chain(r.withConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			in, err := r.input(md)
			if err != nil {
				return err
			}
			if err := setRef(in, arg.MustGet[Ref](cmd, "REF")); err != nil {
				return err
			}
			if err := r.run(ctx, cmd, md, in); err != nil {
				return err
			}

			return next(ctx)
		})),
	}
}

// isRaw says the word is the request rather than a name for it.
//
// Two forms, and both are unambiguous: a name is an identifier or begins with
// `@`, so neither a `-` nor an opening brace can be one.
func isRaw(s string) bool {
	return s == "-" || strings.HasPrefix(s, "{")
}

// setNew writes a name onto an `XxxAddRequest`.
//
// Not a `Ref`: an `Add` has no row to point at, so it carries the fields
// themselves -- `id`, `tenant`, `alias`. The same argument syntax is used for
// both because it is the same thing being said, and `@acme/arm-01` on an `add`
// meaning "in acme, called arm-01" is the reading somebody already has.
func setNew(in proto.Message, r Ref) error {
	m := in.ProtoReflect()
	fs := m.Descriptor().Fields()

	if !r.Id.IsZero() {
		fd := fs.ByName("id")
		if fd == nil {
			return fmt.Errorf("%s: takes no identifier", m.Descriptor().FullName())
		}

		m.Set(fd, protoreflect.ValueOfBytes(r.Id.Bytes()))
	}

	if r.Alias != "" {
		fd := fs.ByName("alias")
		if fd == nil {
			return fmt.Errorf("%s: has no alias", m.Descriptor().FullName())
		}

		m.Set(fd, protoreflect.ValueOfString(r.Alias))
	}

	if r.Tenant != "" {
		fd := fs.ByName("tenant")
		if fd == nil || fd.Kind() != protoreflect.MessageKind {
			return fmt.Errorf("%s: is not inside a tenant", m.Descriptor().FullName())
		}

		tenant := m.NewField(fd).Message()
		afd := tenant.Descriptor().Fields().ByName("alias")
		if afd == nil {
			return fmt.Errorf("%s: cannot be named by alias", tenant.Descriptor().FullName())
		}

		tenant.Set(afd, protoreflect.ValueOfString(r.Tenant))
		m.Set(fd, protoreflect.ValueOfMessage(tenant))
	}

	return nil
}
