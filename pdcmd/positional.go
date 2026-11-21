package pdcmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// reference recognizes the contract the ORM generator writes, not a message
// that merely has a field called id. Free-form JSON and ordinary messages must
// never acquire reference semantics because their values resemble a slug.
func reference(d protoreflect.MessageDescriptor) bool {
	if d == nil || !strings.HasSuffix(string(d.Name()), "Ref") {
		return false
	}
	id := d.Fields().ByName("id")
	return id != nil && id.Kind() == protoreflect.BytesKind && id.ContainingOneof() != nil && id.ContainingOneof().Name() == "key"
}

// The kind of thing the reference names comes from the field's own type:
// the `ref` of a request about a Robot is an `app.RobotRef`, and "the name
// of the entity, then Ref" is the generated rule. An app whose type the
// registry cannot answer for -- a ref declared by hand, outside generation --
// gets no expectation, which [Ref.Expect] reads as nothing to check: the claim
// was still parsed, so a "#word" this app has no domain for is refused either way.
func referenceDomain(d protoreflect.MessageDescriptor) pdid.Domain {
	name := strings.TrimSuffix(string(d.FullName()), "Ref")
	if domain, ok := pdid.Lookup(name); ok {
		return domain
	}
	return pdid.Unknown
}
func compactKey(f protoreflect.FieldDescriptor) bool {
	parent, ok := f.Parent().(protoreflect.MessageDescriptor)
	return ok && reference(parent) && f.ContainingOneof() != nil && f.ContainingOneof().Name() == "key" && f.Message() != nil && f.Message().Fields().Len() == 1 && !jsonMapped(f.Message()) && !reference(f.Message())
}
func decodeJSON(b []byte) (any, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("more than one JSON value")
	}
	return v, nil
}

// positionalValue follows protojson's scalar typing; strings remain literal so
// a part number such as 0042 does not turn into a number and lose its spelling.
func positionalValue(f protoreflect.FieldDescriptor, text string) (any, error) {
	if f.IsList() || f.IsMap() {
		return nil, fmt.Errorf("%s is a collection; use request JSON", f.FullName())
	}
	if compactKey(f) && !strings.HasPrefix(strings.TrimSpace(text), "{") {
		inner := f.Message().Fields().Get(0)
		v, err := positionalValue(inner, text)
		if err != nil {
			return nil, err
		}
		return map[string]any{inner.JSONName(): v}, nil
	}
	switch f.Kind() {
	case protoreflect.StringKind, protoreflect.BytesKind:
		return text, nil
	case protoreflect.EnumKind:
		if v, err := decodeJSON([]byte(text)); err == nil {
			if _, ok := v.(json.Number); ok {
				return v, nil
			}
		}
		return text, nil
	case protoreflect.MessageKind:
		if reference(f.Message()) {
			return text, nil
		}
		switch f.Message().FullName() {
		case "google.protobuf.Timestamp", "google.protobuf.Duration", "google.protobuf.FieldMask", "google.protobuf.StringValue", "google.protobuf.BytesValue":
			return text, nil
		}
	}
	return decodeJSON([]byte(text))
}
