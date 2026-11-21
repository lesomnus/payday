package gate

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdpb"
)

// Direct enforces (payday.method).direct on the method being served, including
// in-process calls through the generated gate. The transport gate also calls
// it for hand-written services. A permissive app policy cannot widen it.
func Direct(ctx context.Context, method string) error {
	f, ok := frame.From(ctx)
	if !ok || f.Caller == nil {
		return nil
	}
	service, name, ok := strings.Cut(strings.TrimPrefix(method, "/"), "/")
	if !ok {
		return nil
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service + "." + name))
	if err != nil {
		return nil
	}
	m, ok := d.(protoreflect.MethodDescriptor)
	if !ok {
		return nil
	}
	o, _ := proto.GetExtension(m.Options(), pdpb.E_Method).(*pdpb.Method)
	if o.HasDirect() {
		return status.Errorf(codes.PermissionDenied, "%s: the actor must call this directly", method)
	}
	return nil
}
