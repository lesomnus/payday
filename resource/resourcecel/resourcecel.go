// Package resourcecel is the language a Watch filter is written in: a boolean
// CEL expression over `event`, the [pdpb.WatchEvent] the subscriber would be
// sent (payday#74). Importing it is what makes a process evaluate one:
//
//	import _ "github.com/lesomnus/payday/resource/resourcecel"
//
// # Why importing it, and where
//
// Because the linker follows imports, and everything an app generates imports
// [resource] -- so the app's sandbox does too, since it builds on the `Build`
// the process builds on. With CEL in [resource], every page payday's apps serve
// carried cel-go, ANTLR under its parser and `text/template` for a prompt
// nothing renders: 10 MB of payday's own sandbox, for a Watch no page serves
// (payday#163).
//
// So it is said once, where the database driver is said: in what only the
// process imports -- `cli` in an app `pd new` wrote -- and never in what the
// sandbox does. A server in a binary that does not link it refuses a filter
// rather than ignoring one; see [resource].
//
// Nothing is excluded by a build tag. A page that does want filtered watches
// imports this, and pays for it knowingly.
package resourcecel

import (
	"context"
	"errors"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/interpreter"
	pwrcel "github.com/lesomnus/pwr/cel"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/resource"
)

const (
	// MaxDepth is how deeply a filter may nest.
	MaxDepth = 64

	// MaxCost is what evaluating a filter against one event may spend, in
	// CEL's cost units. Past it the subscription ends ResourceExhausted.
	MaxCost = 100_000
)

func init() {
	resource.RegisterLanguage(language)
}

// language is CEL over the event and the types a server serves, which is what
// lets a filter say `type(event.value) == app.Robot` and read the fields.
func language(descriptors []protoreflect.MessageDescriptor) (resource.Filters, error) {
	adapter, err := pwrcel.New(descriptors...)
	if err != nil {
		return nil, err
	}
	options := append(adapter.EnvOptions(), cel.Variable("event", cel.ObjectType("payday.WatchEvent")),
		cel.ParserExpressionSizeLimit(resource.MaxExpression), cel.ParserRecursionLimit(MaxDepth),
		cel.Constant("payday.CHANGE_KIND_UNSPECIFIED", cel.IntType, types.Int(pdpb.ChangeKind_CHANGE_KIND_UNSPECIFIED)),
		cel.Constant("payday.CHANGE_KIND_CREATED", cel.IntType, types.Int(pdpb.ChangeKind_CHANGE_KIND_CREATED)),
		cel.Constant("payday.CHANGE_KIND_UPDATED", cel.IntType, types.Int(pdpb.ChangeKind_CHANGE_KIND_UPDATED)),
		cel.Constant("payday.CHANGE_KIND_DELETED", cel.IntType, types.Int(pdpb.ChangeKind_CHANGE_KIND_DELETED)))
	env, err := cel.NewEnv(options...)
	if err != nil {
		return nil, err
	}
	return filters{env}, nil
}

type filters struct{ env *cel.Env }

func (f filters) Compile(filter string) (resource.Filter, error) {
	ast, issues := f.env.Compile(filter)
	if issues.Err() != nil {
		return nil, status.Errorf(codes.InvalidArgument, "watch filter: %s", issues.Err())
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, status.Error(codes.InvalidArgument, "watch filter must return bool")
	}
	p, err := f.env.Program(ast, cel.CostLimit(MaxCost), cel.InterruptCheckFrequency(64))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "watch filter cannot be evaluated")
	}
	return program{p}, nil
}

type program struct{ cel.Program }

func (p program) Match(ctx context.Context, event *pdpb.WatchEvent) (bool, error) {
	// The adapter reads its Any bytes through pwr, avoiding a second full
	// payload decode without serializing the event envelope.
	v, _, err := p.ContextEval(ctx, map[string]any{"event": event})
	if err != nil {
		var cancelled interpreter.EvalCancelledError
		if errors.As(err, &cancelled) && cancelled.Cause == interpreter.CostLimitExceeded {
			return false, status.Error(codes.ResourceExhausted, "watch filter exceeded its evaluation budget")
		}
		// The CEL error may embed a field's value. Never return it to the caller.
		return false, status.Error(codes.InvalidArgument, "watch filter failed on an authorized event; guard dynamic fields with type and presence checks")
	}
	return v == types.True, nil
}
