package resource

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Redact copies an image and removes declared secrets recursively, including
// nested messages and unknown fields. Run it before storage as well as delivery.
func Redact(v proto.Message, secrets map[protoreflect.FullName][]protoreflect.FieldNumber) proto.Message {
	if v == nil || !v.ProtoReflect().IsValid() {
		return nil
	}
	out := proto.Clone(v)
	var clear func(protoreflect.Message)
	clear = func(m protoreflect.Message) {
		m.SetUnknown(nil)
		for _, n := range secrets[m.Descriptor().FullName()] {
			if f := m.Descriptor().Fields().ByNumber(n); f != nil {
				m.Clear(f)
			}
		}
		m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			switch {
			case f.IsMap() && f.MapValue().Message() != nil:
				v.Map().Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool { clear(v.Message()); return true })
			case f.IsList() && f.Message() != nil:
				for i := 0; i < v.List().Len(); i++ {
					clear(v.List().Get(i).Message())
				}
			case !f.IsMap() && !f.IsList() && f.Message() != nil:
				clear(v.Message())
			}
			return true
		})
	}
	clear(out.ProtoReflect())
	return out
}
