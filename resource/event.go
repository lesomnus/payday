package resource

import (
	"bytes"
	"errors"
	"time"

	"github.com/lesomnus/payday/pdpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Event builds one envelope before the live and outbox paths diverge. Inputs must
// already have secrets removed. The packed values no longer alias the caller.
func Event(id []byte, at time.Time, method, call string, kind pdpb.ChangeKind, previous, value proto.Message) (*pdpb.WatchEvent, error) {
	valid := func(m proto.Message) bool { return m != nil && m.ProtoReflect().IsValid() }
	if len(id) != 16 {
		return nil, errors.New("resource: event ID must be sixteen bytes")
	}
	stamp := timestamppb.New(at)
	if err := stamp.CheckValid(); err != nil {
		return nil, errors.New("resource: invalid event timestamp")
	}
	old, new := valid(previous), valid(value)
	// Update preimages are optional, so payload presence cannot determine kind.
	validImages := false
	switch kind {
	case pdpb.ChangeKind_CHANGE_KIND_CREATED:
		validImages = !old && new
	case pdpb.ChangeKind_CHANGE_KIND_UPDATED:
		validImages = new
	case pdpb.ChangeKind_CHANGE_KIND_DELETED:
		validImages = old && !new
	}
	if !validImages {
		return nil, errors.New("resource: images do not match change kind")
	}
	key := func(m proto.Message) []byte {
		r := m.ProtoReflect()
		f := r.Descriptor().Fields().ByNumber(1)
		if f == nil || string(f.Name()) != "id" {
			return nil
		}
		b, _ := r.Get(f).Interface().([]byte)
		return b
	}
	for _, m := range []proto.Message{previous, value} {
		if valid(m) && len(key(m)) != 16 {
			return nil, errors.New("resource: image has no resource ID")
		}
	}
	if old && new && (previous.ProtoReflect().Descriptor().FullName() != value.ProtoReflect().Descriptor().FullName() || !bytes.Equal(key(previous), key(value))) {
		return nil, errors.New("resource: images identify different resources")
	}
	event := pdpb.WatchEvent_builder{Id: bytes.Clone(id), DateCreated: stamp, Method: method, CallMethod: call, Kind: kind}.Build()
	if old {
		v, err := anypb.New(previous)
		if err != nil {
			return nil, err
		}
		event.SetPrevious(v)
	}
	if new {
		v, err := anypb.New(value)
		if err != nil {
			return nil, err
		}
		event.SetValue(v)
	}
	if proto.Size(event) > MaxEventBytes {
		return nil, errors.New("resource: event exceeds payload limit")
	}
	return event, nil
}
