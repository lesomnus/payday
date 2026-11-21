// Package pdcolumn resolves CLI columns once by the same rules in generation
// and at runtime, so an invalid declaration fails before an app is shipped.
package pdcolumn

import (
	"fmt"
	"strings"

	"github.com/lesomnus/payday/pdpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type Column struct {
	Title string
	Wide  bool
	Path  []protoreflect.FieldDescriptor
}

func Scalar(f protoreflect.FieldDescriptor) bool {
	if f.IsList() || f.IsMap() {
		return false
	}
	if f.Message() == nil {
		return true
	}
	switch f.Message().FullName() {
	case "google.protobuf.Timestamp", "google.protobuf.Duration",
		"google.protobuf.StringValue", "google.protobuf.BytesValue", "google.protobuf.BoolValue",
		"google.protobuf.Int32Value", "google.protobuf.Int64Value", "google.protobuf.UInt32Value", "google.protobuf.UInt64Value",
		"google.protobuf.FloatValue", "google.protobuf.DoubleValue":
		return true
	}
	return false
}

func Secret(f protoreflect.FieldDescriptor) bool {
	opts, _ := proto.GetExtension(f.Options(), pdpb.E_Field).(*pdpb.Field)
	return opts.GetSecret()
}

func Resolve(d protoreflect.MessageDescriptor, specs []*pdpb.Entity_Column) ([]Column, error) {
	out := make([]Column, 0, len(specs))
	visible := false
	seen := map[string]bool{}
	for _, spec := range specs {
		name := spec.GetField()
		fail := func(why string) ([]Column, error) {
			return nil, fmt.Errorf("%s: columns.%s: %s", d.FullName(), name, why)
		}
		if seen[name] {
			return fail("duplicate field")
		}
		seen[name] = true
		c := Column{Title: spec.GetTitle(), Wide: spec.GetWide()}
		if c.Title == "" {
			c.Title = strings.ToUpper(name)
		}
		if strings.ContainsAny(c.Title, "\t\r\n") {
			return fail("title must fit one table cell")
		}
		current := d
		parts := strings.Split(name, ".")
		for i, part := range parts {
			if current == nil {
				return fail("path crosses a scalar field")
			}
			f := current.Fields().ByName(protoreflect.Name(part))
			if f == nil {
				return fail("unknown field path")
			}
			if f.IsList() || f.IsMap() {
				return fail("path crosses a list or map")
			}
			if Secret(f) {
				return fail("secret fields cannot be table columns")
			}
			if i == len(parts)-1 && !Scalar(f) {
				return fail("column must end in a scalar field")
			}
			c.Path = append(c.Path, f)
			current = f.Message()
		}
		visible = visible || !c.Wide
		out = append(out, c)
	}
	if len(out) > 0 && !visible {
		return nil, fmt.Errorf("%s: columns: at least one column must appear without wide", d.FullName())
	}
	return out, nil
}
