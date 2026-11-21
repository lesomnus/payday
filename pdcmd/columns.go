package pdcmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lesomnus/payday/internal/pdcolumn"
	"github.com/lesomnus/payday/pdpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func entityMessage(d protoreflect.MessageDescriptor) bool {
	return proto.HasExtension(d.Options(), pdpb.E_Entity)
}

func declaredColumns(d protoreflect.MessageDescriptor, wide bool, opts ...PrintOptions) ([]column, bool, error) {
	entity, _ := proto.GetExtension(d.Options(), pdpb.E_Entity).(*pdpb.Entity)
	if len(entity.GetColumns()) == 0 {
		return nil, false, nil
	}
	specs, err := pdcolumn.Resolve(d, entity.GetColumns())
	if err != nil {
		return nil, true, err
	}
	cols := []column{}
	for _, spec := range specs {
		if !spec.Wide || wide {
			cols = append(cols, pathColumn(spec.Title, spec.Path, opts...))
		}
	}
	return cols, true, nil
}

func scalarColumns(d protoreflect.MessageDescriptor, opts ...PrintOptions) []column {
	var out []column
	for i := 0; i < d.Fields().Len(); i++ {
		f := d.Fields().Get(i)
		if pdcolumn.Scalar(f) && !pdcolumn.Secret(f) {
			out = append(out, pathColumn(strings.ToUpper(string(f.Name())), []protoreflect.FieldDescriptor{f}, opts...))
		}
	}
	return out
}

func pathColumn(title string, path []protoreflect.FieldDescriptor, opts ...PrintOptions) column {
	return column{key: len(path) == 1 && path[0].Name() == "id", head: title, read: func(m protoreflect.Message) string {
		for i, f := range path {
			if !m.IsValid() || (f.HasPresence() && !m.Has(f)) {
				return "-"
			}
			v := m.Get(f)
			if i == len(path)-1 {
				return tableValue(v, f, opts...)
			}
			m = v.Message()
		}
		return "-"
	}}
}

func tableValue(v protoreflect.Value, f protoreflect.FieldDescriptor, opts ...PrintOptions) string {
	switch f.Kind() {
	case protoreflect.BytesKind:
		if isId(f) {
			return uuidOf(v.Bytes())
		}
		return base64.StdEncoding.EncodeToString(v.Bytes())
	case protoreflect.EnumKind:
		if e := f.Enum().Values().ByNumber(v.Enum()); e != nil {
			return string(e.Name())
		}
		return fmt.Sprint(v.Enum())
	case protoreflect.MessageKind:
		b, err := humanJSON(v.Message(), "", printLocation(opts))
		if err != nil {
			return "<invalid>"
		}
		var s string
		if json.Unmarshal(b, &s) == nil {
			return cellText(s)
		}
		return string(b)
	default:
		return cellText(fmt.Sprint(v.Interface()))
	}
}

// A field value is one cell even if the stored text contains a tab or newline.
func cellText(s string) string {
	return strings.NewReplacer("\t", "\\t", "\n", "\\n", "\r", "\\r").Replace(s)
}

func tableRows(m proto.Message) []protoreflect.Message {
	d := m.ProtoReflect().Descriptor()
	if f := d.Fields().ByName("items"); f != nil && f.IsList() && f.Message() != nil {
		return Rows(m)
	}
	if entityMessage(d) {
		return Rows(m)
	}
	var rows protoreflect.FieldDescriptor
	for i := 0; i < d.Fields().Len(); i++ {
		f := d.Fields().Get(i)
		if f.IsList() && f.Message() != nil {
			if rows != nil {
				return Rows(m)
			}
			rows = f
		}
	}
	if rows == nil {
		return Rows(m)
	}
	values := m.ProtoReflect().Get(rows).List()
	out := make([]protoreflect.Message, values.Len())
	for i := range values.Len() {
		out[i] = values.Get(i).Message()
	}
	return out
}
