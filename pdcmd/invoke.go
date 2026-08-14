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

// Conn is what the app hands over, and the whole of what these commands need.
//
// A `grpc.ClientConnInterface` and not a generated client, for the reason this
// package exists at all: an app has more than one way in -- a dialed socket, an
// in-process server over `bufconn`, an admin port with a different policy in
// front of it -- and which one a command should use is the deployment's
// decision, not payday's. Nothing here dials, authenticates, or reads a
// configuration file. The caller has already decided all three by the time it
// has one of these.
//
// It is also what makes an embedded server work unchanged: `bufconn` hands back
// a `*grpc.ClientConn` like any other.
type Conn = grpc.ClientConnInterface

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
//	app robot add @arm-01 '{"cell":{"alias":"floor-2"}}'
//	app robot patch @acme/arm-01 '{"alias":"arm-02"}'
//	app robot add - < robot.json
//
// Taken from oasys, which found the same thing writing the same commands by
// hand: the flexible half has to be the message, because the message is what
// the server takes.
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
