// Package resourcesql evaluates existing Ent read predicates against an immutable
// resource image. The image replaces only the root row; related-resource checks
// use the database's current policy state. Missing related rows therefore deny
// access. No historical row is inserted into the application's tables.
package resourcesql

import (
	"fmt"
	"time"
	"uuid"

	"github.com/protobuf-orm/ent/dialect/sql"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type Column struct {
	Number   protoreflect.FieldNumber
	Name     string
	Type     ormpb.Type
	Nullable bool
	// Key is the field number of a referenced entity's key, or zero for a scalar.
	Key protoreflect.FieldNumber
}

// Row constructs a derived table with the original table's SQL types. Columns
// containing secrets must be omitted: a scope referring to one then fails closed
// instead of authorizing against an invented zero value. Such policies need an
// application-provided historical projector. Columns that protobuf cannot
// represent losslessly (collections, JSON and nullable scalars without presence)
// must also be omitted.
func Row(dialect, table string, columns []Column, message proto.Message) (*sql.Selector, error) {
	m := message.ProtoReflect()
	names := make([]string, 0, len(columns))
	args := make([]sql.Querier, 0, len(columns))
	for _, c := range columns {
		f := m.Descriptor().Fields().ByNumber(c.Number)
		if f == nil {
			return nil, fmt.Errorf("resource snapshot column %s has no field", c.Name)
		}
		if f.IsMap() || f.IsList() || (c.Key == 0 && (c.Type == ormpb.Type_TYPE_JSON || c.Type == ormpb.Type_TYPE_MESSAGE)) || (c.Nullable && !f.HasPresence()) {
			return nil, fmt.Errorf("resource snapshot column %s requires a historical projector", c.Name)
		}
		var value any
		if !c.Nullable || m.Has(f) {
			v := m.Get(f)
			if c.Key != 0 {
				ref := v.Message()
				v = ref.Get(ref.Descriptor().Fields().ByNumber(c.Key))
			}
			var err error
			value, err = argument(c, v)
			if err != nil {
				return nil, err
			}
		}
		names = append(names, c.Name)
		args = append(args, sql.ExprFunc(func(b *sql.Builder) { b.Arg(value) }))
	}
	// The empty real-table branch supplies PostgreSQL's UUID/jsonb/timestamp types
	// to the parameter branch, and never contributes a current resource value.
	row := sql.Dialect(dialect).Select(names...).From(sql.Table(table)).Where(sql.ExprP("1=0"))
	row.UnionAll(sql.SelectExpr(args...))
	return row.As(table), nil
}

func argument(c Column, v protoreflect.Value) (any, error) {
	switch c.Type {
	case ormpb.Type_TYPE_UUID:
		b := v.Bytes()
		if len(b) != 16 {
			return nil, fmt.Errorf("resource snapshot: invalid UUID")
		}
		return uuid.UUID(b), nil
	case ormpb.Type_TYPE_TIME:
		m := v.Message()
		f := m.Descriptor().Fields()
		return time.Unix(m.Get(f.ByName("seconds")).Int(), m.Get(f.ByName("nanos")).Int()).UTC(), nil
	case ormpb.Type_TYPE_ENUM:
		return int32(v.Enum()), nil
	}
	return v.Interface(), nil
}
