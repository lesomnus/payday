package pdcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/pdid"
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
// invented -- it is the shape `pd gen` writes and the shape a hand-written
// `RobotMoveRequest` follows, because an RPC about a row names it the way every
// other RPC names one.
//
// [Arg] chooses a different singular positional field. [OptionalArg] permits
// omission when the handler accepts it. Everything else is trailing JSON:
// a flag per field would be a second copy of the schema.
func (t *Tree) Unary(method string, options ...UnaryOption) (*xli.Command, error) {
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

	config := unaryOptions{}
	for _, opt := range options {
		if opt == nil {
			return nil, fmt.Errorf("pdcmd: nil Unary option")
		}
		if err := opt(&config); err != nil {
			return nil, err
		}
	}
	field := config.field
	if !config.arg && md.Input().Fields().ByName("ref") != nil {
		field = "ref"
	}
	var fd protoreflect.FieldDescriptor
	argName := ""
	args := arg.Args{}
	if field != "" {
		fd = md.Input().Fields().ByName(protoreflect.Name(field))
		if fd == nil {
			fd = md.Input().Fields().ByJSONName(field)
		}
		if fd == nil || fd.IsList() || fd.IsMap() {
			return nil, fmt.Errorf("pdcmd: %s: %s must name a singular request field", method, field)
		}
		argName = strings.ToUpper(string(fd.Name()))
		args = append(args, &arg.String{Name: argName, Optional: config.optional})
	} else if config.optional {
		return nil, fmt.Errorf("pdcmd: %s: OptionalArg needs a positional field", method)
	}
	args = append(args, argRaw())

	return &xli.Command{
		// The RPC's name, as a command is typed: `AddMany` is `add-many`, the
		// way an entity's name is its message's -- [pdid.Name] -- and not
		// `addmany`, which is a word nobody would guess at. [Tree.Add] names
		// it again by its path, so this is what it is called anywhere else.
		Name:  pdid.Name("", name),
		Brief: string(md.Name()),

		Flags: flg.Flags{flgOutput(r.opts.def()), flgInput()},
		Args:  args,

		Handler: xli.Chain(r.withConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			in, err := r.input(md)
			if err != nil {
				return err
			}

			raw := []string{}
			if fd != nil {
				if text, ok := arg.Get[string](cmd, argName); ok {
					if config.optional && isRaw(text) {
						raw = append(raw, text)
					} else {
						v, err := positionalValue(fd, text)
						if err != nil {
							return fmt.Errorf("%s: %w", argName, err)
						}
						b, err := json.Marshal(map[string]any{fd.JSONName(): v})
						if err != nil {
							return err
						}
						if err := unmarshalJson(b, in, false); err != nil {
							return fmt.Errorf("%s: %w", argName, err)
						}
					}
				}
			}
			if err := r.run(ctx, cmd, md, in, raw...); err != nil {
				return err
			}

			return next(ctx)
		})),
	}, nil
}

// UnaryOption selects how a custom command takes its positional argument.
type UnaryOption func(*unaryOptions) error

type unaryOptions struct {
	field         string
	arg, optional bool
}

// Arg names the singular request field filled by the positional argument.
// Without it, Unary uses ref when present. JSON still overrides the argument.
func Arg(field string) UnaryOption {
	return func(o *unaryOptions) error {
		if o.arg || field == "" {
			return fmt.Errorf("pdcmd: Arg must name one request field, once")
		}
		o.field, o.arg = field, true
		return nil
	}
}

// OptionalArg permits an omitted positional argument. Whether a server accepts
// an absent reference is application behavior; protobuf presence cannot tell us.
// A JSON object or '-' in the argument's place is read as the request body.
func OptionalArg() UnaryOption {
	return func(o *unaryOptions) error {
		if o.optional {
			return fmt.Errorf("pdcmd: OptionalArg declared twice")
		}
		o.optional = true
		return nil
	}
}
