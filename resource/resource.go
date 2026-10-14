// Package resource serves operations across resource types. Watch is a live
// stream of immutable mutation images; it does not replace List or provide replay.
package resource

import (
	"bytes"
	"context"
	"errors"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/interpreter"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/watch"
)

const (
	MaxExpression = 16 << 10
	MaxDepth      = 64
	MaxCost       = 100_000
	MaxEventBytes = 4 << 20
)

// Project authorizes a historical resource under the subscriber's CURRENT read
// policy and returns its public representation. Nil with no error denies access.
// It must not fetch the current row as a substitute for the supplied image.
// A fresh clone is passed in; changes cannot affect another subscription.
type Project func(context.Context, proto.Message) (proto.Message, error)

// Select is the application's publication policy, called once inside the write
// transaction before either live collection or outbox persistence. Nil selects all
// supported resources. Audit recording is independent of this policy.
type Select func(context.Context, *pdpb.WatchEvent) (bool, error)

type Server struct {
	pdpb.UnimplementedResourceServiceServer
	batch   pdpb.BatchServiceServer
	watch   *watch.Watch
	project Project
	env     *cel.Env
}

func New(batch pdpb.BatchServiceServer, w *watch.Watch, project Project, messages ...proto.Message) (*Server, error) {
	if batch == nil || project == nil {
		return nil, errors.New("resource: batch and historical read projector are required")
	}
	descriptors := []any{&pdpb.WatchEvent{}}
	for _, m := range messages {
		descriptors = append(descriptors, m)
	}
	env, err := cel.NewEnv(cel.Types(descriptors...), cel.Variable("event", cel.ObjectType("payday.WatchEvent")),
		cel.ParserExpressionSizeLimit(MaxExpression), cel.ParserRecursionLimit(MaxDepth),
		cel.Constant("payday.CHANGE_KIND_UNSPECIFIED", cel.IntType, types.Int(pdpb.ChangeKind_CHANGE_KIND_UNSPECIFIED)),
		cel.Constant("payday.CHANGE_KIND_CREATED", cel.IntType, types.Int(pdpb.ChangeKind_CHANGE_KIND_CREATED)),
		cel.Constant("payday.CHANGE_KIND_UPDATED", cel.IntType, types.Int(pdpb.ChangeKind_CHANGE_KIND_UPDATED)),
		cel.Constant("payday.CHANGE_KIND_DELETED", cel.IntType, types.Int(pdpb.ChangeKind_CHANGE_KIND_DELETED)))
	if err != nil {
		return nil, err
	}
	return &Server{batch: batch, watch: w, project: project, env: env}, nil
}

func (s *Server) Batch(ctx context.Context, req *pdpb.BatchRequest) (*pdpb.BatchResponse, error) {
	return s.batch.Do(ctx, req)
}

func (s *Server) compile(filter string) (cel.Program, error) {
	if len(filter) > MaxExpression {
		return nil, status.Error(codes.InvalidArgument, "watch filter is too large")
	}
	if filter == "" {
		filter = "true"
	}
	ast, issues := s.env.Compile(filter)
	if issues.Err() != nil {
		return nil, status.Errorf(codes.InvalidArgument, "watch filter: %s", issues.Err())
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, status.Error(codes.InvalidArgument, "watch filter must return bool")
	}
	p, err := s.env.Program(ast, cel.CostLimit(MaxCost), cel.InterruptCheckFrequency(64))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "watch filter cannot be evaluated")
	}
	return p, nil
}

func (s *Server) Watch(req *pdpb.WatchRequest, out grpc.ServerStreamingServer[pdpb.WatchEvent]) error {
	p, err := s.compile(req.GetFilter())
	if err != nil {
		return err
	}
	if s.watch == nil || s.watch.Broker() == nil {
		return status.Error(codes.Unavailable, "watch broker is disabled")
	}
	ch, stop := s.watch.Subscribe()
	defer stop()
	if err := out.SendHeader(metadata.MD{}); err != nil {
		return err
	}
	ctx := out.Context()
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case call, ok := <-ch:
			if !ok {
				return status.Error(codes.ResourceExhausted, "watch subscription fell behind or lost its broker connection; reconcile with List")
			}
			for _, change := range call.Changes {
				if change.Resource == nil {
					continue
				}
				event, err := s.public(ctx, change.Resource)
				if err != nil {
					return err
				}
				if event == nil {
					continue
				}
				v, _, err := p.ContextEval(ctx, map[string]any{"event": event})
				if err != nil {
					if ctx.Err() != nil {
						return status.FromContextError(ctx.Err()).Err()
					}
					var cancelled interpreter.EvalCancelledError
					if errors.As(err, &cancelled) && cancelled.Cause == interpreter.CostLimitExceeded {
						return status.Error(codes.ResourceExhausted, "watch filter exceeded its evaluation budget")
					}
					// The CEL error may embed a field's value. Never return it to the caller.
					return status.Error(codes.InvalidArgument, "watch filter failed on an authorized event; guard dynamic fields with type and presence checks")
				}
				if v == types.True {
					if err := out.Send(event); err != nil {
						return err
					}
				}
			}
		}
	}
}

func (s *Server) public(ctx context.Context, source *pdpb.WatchEvent) (*pdpb.WatchEvent, error) {
	event := proto.Clone(source).(*pdpb.WatchEvent)
	for _, slot := range []struct {
		value *anypb.Any
		set   func(*anypb.Any)
	}{
		{event.GetPrevious(), event.SetPrevious}, {event.GetValue(), event.SetValue},
	} {
		if slot.value == nil {
			continue
		}
		v, err := slot.value.UnmarshalNew()
		if err != nil {
			return nil, status.Error(codes.Internal, "watch resource type is unavailable")
		}
		projected, err := s.project(ctx, v)
		if err != nil {
			return nil, status.Error(codes.Internal, "watch resource authorization failed")
		}
		// Both images must be readable. A move must not disclose the other tenant's
		// data merely because the caller can read one side of the transition.
		if projected == nil {
			return nil, nil
		}
		if projected.ProtoReflect().Descriptor().FullName() != v.ProtoReflect().Descriptor().FullName() {
			return nil, status.Error(codes.Internal, "watch projector changed resource type")
		}
		original := v.ProtoReflect()
		public := projected.ProtoReflect()
		id := original.Descriptor().Fields().ByNumber(1)
		if id == nil || id.Kind() != protoreflect.BytesKind || !bytes.Equal(original.Get(id).Bytes(), public.Get(id).Bytes()) {
			return nil, status.Error(codes.Internal, "watch projector changed resource ID")
		}
		packed, err := anypb.New(projected)
		if err != nil {
			return nil, status.Error(codes.Internal, "watch public resource cannot be encoded")
		}
		slot.set(packed)
	}
	return event, nil
}
