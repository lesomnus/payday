package pdcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Unary builds a command for any unary method, named in full.
//
//	c, err := t.Unary("app.RobotService.Transfer")
//	t.Add("robot/transfer", c)
//
// # Why this exists
//
// The verbs [Tree] builds are the ones every entity has. An operation that
// means something is not one of them: payday closes the general writes on
// purpose, so "move this robot to another tenant" is an RPC an app declares in
// an overlay and implements in a layer -- see the schema guide. Nothing can
// generate a command for it, because nothing knows what it means.
//
// What can be shared is everything around it, and that is what this is: the
// reference argument, the trailing protojson, `-o` and `--in`, the call and the
// printing. What is left for the app is the name it goes under.
//
// # It does not care where the method came from
//
// A method declared in an overlay and a method `pd gen` wrote land in the same
// place -- one `ServiceDescriptor`, merged before generation -- so this reaches
// both by name and there is no second path for a hand-written RPC. That is the
// same property [Tree] relies on to know that `Robot` has a `List` and `Cell`
// does not.
//
// # What it works out for itself
//
// Whether to take a reference argument: a request with a `ref` field takes one,
// and a request without takes none. That is not a convention this package
// invented -- it is the shape `pd gen` writes and the shape custody's
// `AssetTransferRequest` follows, because an RPC about a row names it the way
// every other RPC names one.
//
// Everything else about the request is the trailing protojson, for the reason
// on [argRaw]: a flag per field would be a second copy of the schema.
func (t *Tree) Unary(method string) (*xli.Command, error) {
	i := strings.LastIndexByte(method, '.')
	if i < 0 {
		return nil, fmt.Errorf("pdcmd: %s: expected <package>.<Service>.<Method>", method)
	}

	service, name := method[:i], method[i+1:]

	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return nil, fmt.Errorf("pdcmd: %s: %w", service, err)
	}

	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("pdcmd: %s: not a service", service)
	}

	md := sd.Methods().ByName(protoreflect.Name(name))
	if md == nil {
		return nil, fmt.Errorf("pdcmd: %s: %s has no such method", method, service)
	}
	if md.IsStreamingClient() || md.IsStreamingServer() {
		// Refused here rather than built and failing on the call, so that an
		// app wiring a stream up finds out while it is wiring rather than when
		// somebody runs it.
		return nil, fmt.Errorf("pdcmd: %s: is a stream, and this builds one call; "+
			"a server stream is what `watch` is, and what it needed was an opinion "+
			"about ending -- see cmdWatch", method)
	}

	r := t.run

	// A request that names a row takes the argument that names one. `setRef`
	// reads the same field, so the two cannot disagree about which requests
	// have one.
	takesRef := false
	if fd := md.Input().Fields().ByName("ref"); fd != nil && fd.Kind() == protoreflect.MessageKind {
		takesRef = true
	}

	args := arg.Args{}
	if takesRef {
		args = append(args, &ArgRef{Name: "REF"})
	}
	args = append(args, argRaw())

	return &xli.Command{
		Name:  strings.ToLower(name),
		Brief: string(md.Name()),

		Flags: flg.Flags{flgOutput(r.opts.def()), flgInput()},
		Args:  args,

		Handler: xli.Chain(r.withConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			in, err := r.input(md)
			if err != nil {
				return err
			}

			if takesRef {
				if err := setRef(in, arg.MustGet[Ref](cmd, "REF")); err != nil {
					return err
				}
			}

			if err := r.run(ctx, cmd, md, in); err != nil {
				return err
			}

			return next(ctx)
		})),
	}, nil
}
