package pdcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The verbs this package knows how to build, in the order they are shown --
// what reads, then what writes.
//
// `Apply` is deliberately absent. It is one of payday's two general writes and
// is closed unless an app opts in, so a command for it would be one that fails
// on every app that took the default -- and the apps that did opt in have a
// reason of their own, which is a command they should write.
//
// `Watch` is here, and it is the only one that does not end on its own. It was
// left out for a while as "a stream, which is not this shape", and that was a
// description of [runner.run] rather than a reason: everything around the call
// -- the connection, the request, `--in`, `-o` -- is the same, and what a
// stream actually needs is an ending to have an opinion about. See [cmdWatch].
var verbs = []struct {
	name   string
	method string

	// stream is the shape the verb was written for, and it is checked rather
	// than assumed: a `Watch` that is not a stream, or a `Get` that is, is not
	// the method this verb means. An app may declare either -- what it gets is
	// no command, rather than one that cannot work.
	stream bool

	build func(e Entity, md protoreflect.MethodDescriptor, r *runner) *xli.Command
}{
	{name: "get", method: "Get", build: cmdGet},
	{name: "ls", method: "List", build: cmdList},
	{name: "watch", method: "Watch", stream: true, build: cmdWatch},
	{name: "add", method: "Add", build: cmdAdd},
	{name: "patch", method: "Patch", build: cmdPatch},
	{name: "erase", method: "Erase", build: cmdErase},
}

// runner is what every built command closes over: where to send the call and
// how to show the answer.
type runner struct {
	conn Connector
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

// watch is [runner.run] for a stream: the same request, the same printer, read
// until it ends.
//
// The printer is resolved twice rather than once. A table shows its header on
// the first answer and not between every event afterwards, which is the whole
// of what a stream changes about a format -- see [runner.printerAs].
func (r *runner) watch(ctx context.Context, cmd *xli.Command, md protoreflect.MethodDescriptor, in proto.Message, retry bool, raw ...string) error {
	if err := fillRaw(cmd, in, raw...); err != nil {
		return err
	}

	head, err := r.printerAs(cmd, true)
	if err != nil {
		return err
	}
	tail, err := r.printerAs(cmd, false)
	if err != nil {
		return err
	}

	p := head
	c := MustConn(ctx)

	// Doubling from a quarter of a second and reset by anything arriving. A
	// server that is down would otherwise be dialled as fast as this loop goes
	// round, and somebody who restarted one wants the first reconnect quick.
	const least, most = 250 * time.Millisecond, 8 * time.Second
	wait := least

	for {
		n, err := func() (int, error) {
			s, err := open(ctx, c, md, in)
			if err != nil {
				return 0, err
			}

			for n := 0; ; n++ {
				out, err := newMessage(md.Output())
				if err != nil {
					return n, err
				}
				if err := s.RecvMsg(out); err != nil {
					return n, err
				}
				if err := p.Print(cmd, out); err != nil {
					return n, err
				}

				p = tail
			}
		}()

		// Stopped rather than ended: a signal or a cancelled context is the one
		// ending somebody asked for, and the only one that is not a gap.
		if ctx.Err() != nil {
			return nil
		}
		if !retry || !worthRetrying(err) {
			return ended(err)
		}

		// The snapshot comes back on for a reconnect, whatever the request
		// asked for the first time. `skip_snapshot` says "I know the current
		// state"; after a gap that is no longer true, and resuming without it
		// would leave somebody holding a row that is wrong until the next write
		// happens to correct it -- which may be never.
		if fd := in.ProtoReflect().Descriptor().Fields().ByName("skip_snapshot"); fd != nil {
			in.ProtoReflect().Clear(fd)
		}

		// Anything arriving means the connection worked, so the next failure
		// starts over rather than carrying on from a backoff that a stream
		// running for a week had wound up to the cap.
		if n > 0 {
			wait = least
		}

		// To stderr, because stdout is the answer: a `-o json` piped into
		// something must not have a reconnection notice in the middle of it.
		fmt.Fprintf(cmd.ErrWriter, "watch: %v; reconnecting in %s\n", ended(err), wait)

		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}

		if wait *= 2; wait > most {
			wait = most
		}
	}
}

// worthRetrying says whether the same request could answer differently later.
//
// `--retry` is about the connection going, and a status the request itself
// caused is not that: a filter naming a row that is not there, a credential that
// may not read it, an entity whose watch this deployment does not serve. None of
// them are fixed by asking again with the same words, and a loop that tried
// would be a command that never stops and never works -- which is worse than
// the failure it was covering, because it looks like it is doing something.
func worthRetrying(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument,
		codes.NotFound,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.FailedPrecondition,
		codes.Unimplemented:
		return false
	}

	return true
}

// ended says what a stream that stopped means, because the transport does not.
//
// A clean [io.EOF] is the server closing the subscription, and it is the case
// worth the sentence: nothing failed, so the error a caller would otherwise see
// is no error at all, and "the command exited" is indistinguishable from "the
// command finished". A watch never finishes.
func ended(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return errors.New("the stream ended, and a watch has no backlog: " +
			"whatever changed after this was not sent to anybody. " +
			"Run it again -- the snapshot is what says what was missed -- or pass --retry")
	}

	return err
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
			ref := arg.MustGet[Ref](cmd, "REF")
			if err := ref.Expect(e.Domain); err != nil {
				return err
			}
			if err := setRef(in, ref); err != nil {
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

// cmdWatch is `ls`, kept current, and the only command here that does not end
// on its own.
//
// The request is a `List`'s in the same words -- what is watched is what that
// list would answer with, now and afterwards -- so the arguments are the same
// trailing protojson. The one addition is the optional reference, taken the way
// `add` takes a name and for the same reason it is optional: `filters` is
// **required** on a watch, since a watch with none is the whole table forever,
// so a command that could not name a row on the line would have no short form
// the server accepts.
//
// # It fails when the stream ends
//
// Any ending is a gap. A watch has no backlog to catch up from -- a
// notification reaches whoever is listening and is then forgotten -- so a
// stream that stopped and a stream where nothing is happening look exactly
// alike, and a command that returned quietly would be the one thing that must
// not happen: somebody reading an empty screen and believing it.
//
// `--retry` is the other answer, and it reconnects **with the snapshot**, which
// is the only thing that says what was missed. Neither half is the default by
// accident: exiting is what a script needs, and reconnecting is what a person
// watching one wants, and nothing here can close the gap without being told
// which of the two it is.
func cmdWatch(e Entity, md protoreflect.MethodDescriptor, r *runner) *xli.Command {
	return &xli.Command{
		Name:  "watch",
		Brief: fmt.Sprintf("watch %s", e.Message.Name()),

		Flags: flg.Flags{
			flgOutput(r.opts.def()),
			flgInput(),
			&flg.Switch{
				Name:  "retry",
				Brief: "reconnect when the stream ends, taking the snapshot again, instead of failing",
			},
		},
		Args: arg.Args{&arg.String{Name: "REF", Optional: true}, argRaw()},

		Handler: xli.Chain(r.withConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			in, err := r.input(md)
			if err != nil {
				return err
			}

			// Read here rather than parsed by the argument itself, for the
			// reason `add` gives about NAME: it is optional and the thing after
			// it is not, so `robot watch '{...}'` has to reach the raw form
			// rather than be refused as a malformed reference.
			raw := []string{}
			if v, ok := arg.Get[string](cmd, "REF"); ok {
				if isRaw(v) {
					raw = append(raw, v)
				} else {
					ref, err := RefParser{}.Parse(v)
					if err != nil {
						return fmt.Errorf("REF: %w", err)
					}
					if err := ref.Expect(e.Domain); err != nil {
						return err
					}
					if err := setFilterRef(in, ref); err != nil {
						return err
					}
				}
			}

			retry, _ := flg.Find[bool](cmd, "retry")
			if err := r.watch(ctx, cmd, md, in, retry, raw...); err != nil {
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
					if err := ref.Expect(e.Domain); err != nil {
						return err
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
			ref := arg.MustGet[Ref](cmd, "REF")
			if err := ref.Expect(e.Domain); err != nil {
				return err
			}
			if err := setRef(in, ref); err != nil {
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
			ref := arg.MustGet[Ref](cmd, "REF")
			if err := ref.Expect(e.Domain); err != nil {
				return err
			}
			if err := setRef(in, ref); err != nil {
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
