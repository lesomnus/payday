// Package resource serves operations across resource types. Watch is a live
// stream of immutable mutation images; it does not replace List or provide replay.
//
// # The filter language is not here
//
// A subscription's filter is CEL, and CEL is
// [github.com/lesomnus/payday/resource/resourcecel], which a process links by
// importing it. Everything an app generates imports this package, and so does
// the app's sandbox; that package says why that keeps CEL out of this one.
//
// A [Server] whose binary links no language serves Batch, and a Watch with no
// filter. A Watch with one is refused, `Unimplemented`, rather than served as
// though it had none: a filter says what the subscriber is not to be sent, and
// a stream that ignored it would send exactly that.
package resource

import (
	"bytes"
	"context"
	"errors"
	"sync"

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
	// MaxExpression is the longest filter a subscription may send, in bytes,
	// whatever it is written in: it is refused before a language reads it.
	MaxExpression = 16 << 10
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

// RecorderOptions controls the generated ResourceRecorder's capture cost.
type RecorderOptions struct {
	// Previous captures the preimage of updates using a row lock. By default,
	// updates include only Value. Deletions always capture Previous because it
	// is their only payload for authorization and filtering; creations have no
	// Previous.
	Previous bool
}

// Filter is one subscription's filter, compiled.
type Filter interface {
	// Match is asked of each event the subscriber may read, after its images
	// were projected: it sees what the stream would send and nothing it would
	// not. An error ends the subscription and is what the subscriber is told,
	// so it is a status, and it never says a value the event held -- the event
	// is somebody's data, and the error is read by whoever wrote the filter.
	Match(ctx context.Context, event *pdpb.WatchEvent) (bool, error)
}

// Filters compiles the filters of one [Server].
type Filters interface {
	// Compile answers the filter a subscription sent, and never a nil Filter
	// with no error. One that does not compile is InvalidArgument, saying why:
	// the reason is about the caller's own text and nobody's data.
	Compile(filter string) (Filter, error)
}

// Language makes the [Filters] of a [Server] as it is made, told the types a
// filter may name: the event first, then every resource the server serves.
type Language func(types []protoreflect.MessageDescriptor) (Filters, error)

var language struct {
	sync.Mutex
	l Language
}

// RegisterLanguage makes a Watch filter mean something: every [Server] made
// after it compiles its filters in `l`.
//
// [github.com/lesomnus/payday/resource/resourcecel] calls it as it is
// imported, and nothing else should. A filter is CEL, which is what the field
// says to whoever writes one, and a second language for it would be a second
// meaning -- so a second registration panics, as a second driver of one name
// does.
func RegisterLanguage(l Language) {
	if l == nil {
		panic("resource: RegisterLanguage with a nil language")
	}

	language.Lock()
	defer language.Unlock()
	if language.l != nil {
		panic("resource: a filter language is registered already")
	}
	language.l = l
}

func registered() Language {
	language.Lock()
	defer language.Unlock()
	return language.l
}

type Server struct {
	pdpb.UnimplementedResourceServiceServer
	batch   pdpb.BatchServiceServer
	watch   *watch.Watch
	project Project

	// filters is nil in a binary that links no language: see the package
	// comment.
	filters Filters
}

func New(batch pdpb.BatchServiceServer, w *watch.Watch, project Project, messages ...proto.Message) (*Server, error) {
	if batch == nil || project == nil {
		return nil, errors.New("resource: batch and historical read projector are required")
	}
	descriptors := []protoreflect.MessageDescriptor{(&pdpb.WatchEvent{}).ProtoReflect().Descriptor()}
	for _, m := range messages {
		if m == nil {
			return nil, errors.New("resource: nil message type")
		}
		descriptors = append(descriptors, m.ProtoReflect().Descriptor())
	}
	s := &Server{batch: batch, watch: w, project: project}
	if l := registered(); l != nil {
		filters, err := l(descriptors)
		if err != nil {
			return nil, err
		}
		s.filters = filters
	}
	return s, nil
}

func (s *Server) Batch(ctx context.Context, req *pdpb.BatchRequest) (*pdpb.BatchResponse, error) {
	return s.batch.Do(ctx, req)
}

// compile is the filter a subscription sent, as what its events are matched
// by. Nil, with no error, is every event the subscriber may read: an empty
// filter needs no language to mean that.
func (s *Server) compile(filter string) (Filter, error) {
	if len(filter) > MaxExpression {
		return nil, status.Error(codes.InvalidArgument, "watch filter is too large")
	}
	if filter == "" {
		return nil, nil
	}
	if s.filters == nil {
		return nil, status.Error(codes.Unimplemented, "watch filter: this server links no filter language (payday's resource/resourcecel), so it serves only a watch with no filter")
	}
	f, err := s.filters.Compile(filter)
	if err == nil && f == nil {
		// Nil is what serves the stream unfiltered, which is the one answer a
		// filter must not get.
		return nil, status.Error(codes.Internal, "watch filter compiled to nothing")
	}
	return f, err
}

func (s *Server) Watch(req *pdpb.WatchRequest, out grpc.ServerStreamingServer[pdpb.WatchEvent]) error {
	f, err := s.compile(req.GetFilter())
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
				// Project first: the filter must only see this subscriber's
				// public image.
				ok, err := match(ctx, f, event)
				if err != nil {
					return err
				}
				if ok {
					if err := out.Send(event); err != nil {
						return err
					}
				}
			}
		}
	}
}

// match asks a subscription's filter about one event it may be sent.
func match(ctx context.Context, f Filter, event *pdpb.WatchEvent) (bool, error) {
	if f == nil {
		return true, nil
	}
	ok, err := f.Match(ctx, event)
	if err == nil {
		return ok, nil
	}
	if ctx.Err() != nil {
		return false, status.FromContextError(ctx.Err()).Err()
	}
	if _, isStatus := status.FromError(err); !isStatus {
		// What a language answered is sent to the subscriber, and an error
		// that is not a status is one nobody shaped for that: it may say a
		// value the event held.
		return false, status.Error(codes.Internal, "watch filter failed")
	}
	return false, err
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
		// Every included image must be readable. When update preimages are enabled,
		// a move must not disclose the other tenant's side of the transition.
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
