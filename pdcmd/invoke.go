package pdcmd

import (
	"context"
	"fmt"
	"io"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// call invokes one unary method and answers with the reply.
//
// The concrete generated type is used for the reply when it is linked in, and
// `dynamicpb` when it is not. Concrete matters: it is what a per-message
// renderer keys on, and what a `-o json` of a well-known type formats properly.
func call(ctx context.Context, c Conn, md protoreflect.MethodDescriptor, in proto.Message) (proto.Message, error) {
	out, err := newMessage(md.Output())
	if err != nil {
		return nil, err
	}

	name := fmt.Sprintf("/%s/%s", md.Parent().FullName(), md.Name())
	if err := c.Invoke(ctx, name, in, out); err != nil {
		return nil, err
	}

	return out, nil
}

// open starts one server-streaming method and answers with the stream, the
// request sent and nothing more to send.
//
// The counterpart of [call], and the difference is only that the reply is read
// more than once: the same connection, the same name, the same message. Which
// is why `pdcmd` builds a `Watch` command at all -- what a stream needed was an
// opinion about ending, not a second way of making a call.
func open(ctx context.Context, c Conn, md protoreflect.MethodDescriptor, in proto.Message) (grpc.ClientStream, error) {
	name := fmt.Sprintf("/%s/%s", md.Parent().FullName(), md.Name())

	s, err := c.NewStream(ctx, &grpc.StreamDesc{StreamName: string(md.Name()), ServerStreams: true}, name)
	if err != nil {
		return nil, err
	}
	if err := s.SendMsg(in); err != nil {
		return nil, err
	}

	// Said once and never again: a payday Watch takes its filters in the
	// request and the stream is the answer, so a client that held the send half
	// open would be holding it open for nothing.
	if err := s.CloseSend(); err != nil {
		return nil, err
	}

	return s, nil
}

func newMessage(d protoreflect.MessageDescriptor) (proto.Message, error) {
	if t, err := protoregistry.GlobalTypes.FindMessageByName(d.FullName()); err == nil {
		return t.New().Interface(), nil
	}

	return dynamicpb.NewMessage(d), nil
}

// argRaw is the trailing protojson, and the reason these commands are not a
// smaller version of the API.
//
// A generated `Add` request has every field of the entity on it, and a command
// with a flag per field would be a second schema to keep in step -- one that
// silently loses a field the day somebody adds one. So the typed arguments
// cover what is written constantly (which row, what it is called) and anything
// else is the request itself:
//
//	app robot add @arm-01 '{"cell":{"id":"01a0010f-fd1e-8f1b-a60a-44424d2ababd"}}'
//	app robot patch @acme/arm-01 '{"alias":"arm-02"}'
//	app robot add - < robot.json
//
// The same thing was found writing these commands by hand before anything
// generated them: the flexible half has to be the message, because the message
// is what the server takes.
func argRaw() *arg.RestStrings {
	return &arg.RestStrings{
		Name:  "REQ",
		Brief: `the request as protojson, merged over the arguments; "-" reads stdin`,
	}
}

// fillRaw merges the trailing protojson over what the typed arguments set.
//
// Merged **over**, so the JSON wins where they overlap. That order is the one
// that can be explained in a sentence -- what you spelled out beats what was
// inferred -- and it is what makes the raw form a complete escape hatch: any
// field the command sets can be overridden without the command growing a flag
// to unset it.
//
// Each argument is unmarshalled into the same message rather than concatenated,
// so several are merged in the order they were written.
func fillRaw(cmd *xli.Command, in proto.Message, first ...string) error {
	vs, _ := arg.Get[[]string](cmd, "REQ")
	vs = append(append([]string{}, first...), vs...)
	if len(vs) == 0 {
		return nil
	}

	// `--in` decides how strictly the request is read; both readings are
	// protojson and the lenient one only adds identifiers written as uuids. See
	// [unmarshalJSON].
	strict := false
	if v, ok := flg.Find[string](cmd, "in"); ok && v != "" {
		switch v {
		case "json":
		case "protojson":
			strict = true
		default:
			return fmt.Errorf("--in %s: not an input format; one of json, protojson", v)
		}
	}

	raw := in.ProtoReflect().New().Interface()
	for _, v := range vs {
		var b []byte
		if v == "-" {
			// Bounded, because this is a request and not a file transfer. A
			// megabyte of protojson is already far past anything a person types
			// or a script composes.
			var err error
			b, err = io.ReadAll(io.LimitReader(cmd, 1<<20))
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
		} else {
			b = []byte(v)
		}

		if err := unmarshalJSON(b, raw, strict); err != nil {
			return fmt.Errorf("REQ: %w", err)
		}
	}

	proto.Merge(in, raw)
	return nil
}

// setRef puts a [Ref] where the request keeps one.
//
// Two shapes, because the generated contract has two: most methods take a
// request with a `ref` field, and `Erase` takes the `XxxRef` itself -- it has
// nothing else to say. Rather than special-casing the verb, this asks the
// message which it is.
func setRef(in proto.Message, r Ref) error {
	m := in.ProtoReflect()

	if fd := m.Descriptor().Fields().ByName("ref"); fd != nil && fd.Kind() == protoreflect.MessageKind {
		ref := m.NewField(fd).Message()
		if err := r.Fill(ref); err != nil {
			return err
		}

		m.Set(fd, protoreflect.ValueOfMessage(ref))
		return nil
	}

	return r.Fill(m)
}

// setFilterRef names one row among the filters a watch takes.
//
// A watch takes `filters` and not a `ref`, because what it watches is what a
// list would answer with -- so naming a single row is that list with one filter
// in it, which is the same request written shorter rather than a second shape.
//
// Appended rather than assigned, so that a reference and a trailing protojson
// carrying filters of its own are a union and not a contradiction. That is what
// `proto.Merge` does to a repeated field anyway, and [fillRaw] runs after this.
func setFilterRef(in proto.Message, r Ref) error {
	m := in.ProtoReflect()

	fd := m.Descriptor().Fields().ByName("filters")
	if fd == nil || !fd.IsList() || fd.Kind() != protoreflect.MessageKind {
		return fmt.Errorf("REF: %s takes no filters, so there is nowhere to name a row", m.Descriptor().FullName())
	}

	fs := m.Mutable(fd).List()
	v := fs.NewElement().Message()

	rd := v.Descriptor().Fields().ByName("ref")
	if rd == nil || rd.Kind() != protoreflect.MessageKind {
		return fmt.Errorf("REF: %s has no ref to name a row by", v.Descriptor().FullName())
	}

	ref := v.NewField(rd).Message()
	if err := r.Fill(ref); err != nil {
		return err
	}
	v.Set(rd, protoreflect.ValueOfMessage(ref))

	fs.Append(protoreflect.ValueOfMessage(v))
	return nil
}
