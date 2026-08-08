// Package header is the part of a row that is the same whatever entity it
// belongs to, read from any of them.
//
// # What it is for
//
// Anything that has to say something about a row it does not have a type for.
// The clearest case is already in payday and already half-solved: a trail row
// names what changed by identifier, and the domain byte says what **kind** of
// thing it was -- so "somebody erased a Robot" can be written, and "somebody
// erased **arm-01**" cannot. That is the missing half.
//
// The same shape is what a generic table, a search box, a label selector and an
// audit view all want, and every one of them is otherwise a switch over every
// entity in the app that somebody has to remember to extend.
//
// # It reads rather than being generated
//
// A protobuf descriptor carries every field's name and kind at run time, so
// there is nothing here to generate: this is written once and works on the
// entity somebody adds tomorrow. It is the same discipline the local store is
// held to -- generate the declaration, implement the behaviour once.
//
// What generation does instead is make the read **safe**: a field called `name`
// that is not a string, or `labels` that is not a map of strings, is refused,
// because a reflective read of one would quietly see nothing at all. See
// `pdgen`.
//
// # What it does not do
//
// It does not require an entity to have any of this. payday's own `Audit` and
// `Outbox` have none of it -- their early fields are structural rather than
// descriptive, and forcing them into a shape would be forcing payday to exempt
// itself from its own rule. [Header.Has] is how a reader finds out what was
// there.
package header

import (
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
)

// Field is one of the parts a header can hold.
type Field uint16

const (
	Id Field = 1 << iota
	Alias
	Name
	Desc
	Labels
	Tenant
	Created
	Updated
	Erased
)

// The field names this reads, which are the names payday's own entities use and
// the ones `pd entity add` writes.
//
// They are matched by **name** and not by number. A number is what an overlay
// guard compares and what a wire format is made of; what this needs is only
// that two schemas calling something `name` mean the same thing by it, and
// generation is what makes that true.
const (
	fieldId      = "id"
	fieldAlias   = "alias"
	fieldName    = "name"
	fieldDesc    = "desc"
	fieldLabels  = "labels"
	fieldTenant  = "tenant"
	fieldTenant2 = "tenant_id"
	fieldCreated = "date_created"
	fieldUpdated = "date_updated"
	fieldErased  = "date_erased"
)

// Header is what any row says about itself.
//
// Every part of it may be absent, and [Header.Has] is the only way to tell an
// absent one from an empty one -- an entity with no `name` and an entity whose
// name is "" are different things to a page, and only the first is a reason to
// fall back to something else.
type Header struct {
	// Id is the row, and is the one part almost everything has: every entity
	// payday generates for is keyed by one.
	Id pdid.Id

	// Alias is what a person writes this row as, and Name is what a person
	// reads. They are different on purpose -- `arm-01` and "Arm, front left" --
	// and [Header.Title] is what a page that wants one of them asks for.
	Alias string
	Name  string
	Desc  string

	Labels map[string]string

	// Tenant is who holds the row, and is zero for an entity that is not behind
	// the wall. It is read from an edge or from a column, since payday's own
	// schema has both -- a trail row names its tenant with a column so that it
	// can outlive one.
	Tenant pdid.Id

	Created time.Time
	Updated time.Time
	Erased  time.Time

	has Field
}

// Has reports whether the message this was read from declares `f`.
func (h Header) Has(f Field) bool { return h.has&f != 0 }

// Title is the best thing to show somebody, and never the empty string.
//
// The order is what a person is most likely to recognise: the name they wrote,
// then the name they type, then the identifier -- which is not friendly and is
// at least unambiguous. A page that wants a different order asks the fields
// directly; this is for the many places that only want *something*.
func (h Header) Title() string {
	switch {
	case h.Name != "":
		return h.Name
	case h.Alias != "":
		return h.Alias
	case h.Id != (pdid.Id{}):
		return h.Id.String()
	}

	return ""
}

// Of reads the header of a message.
//
// It answers a zero Header for nil, which is what a caller holding a reference
// that resolved to nothing has, and saves every one of them a check.
func Of(m interface{ ProtoReflect() protoreflect.Message }) Header {
	var h Header
	if m == nil {
		return h
	}

	v := m.ProtoReflect()
	if !v.IsValid() {
		return h
	}

	fs := v.Descriptor().Fields()
	for i := range fs.Len() {
		f := fs.Get(i)
		switch string(f.Name()) {
		case fieldId:
			if k, ok := idOf(v, f); ok {
				h.Id, h.has = k, h.has|Id
			}

		case fieldAlias:
			if f.Kind() == protoreflect.StringKind {
				h.Alias, h.has = v.Get(f).String(), h.has|Alias
			}

		case fieldName:
			if f.Kind() == protoreflect.StringKind {
				h.Name, h.has = v.Get(f).String(), h.has|Name
			}

		case fieldDesc:
			if f.Kind() == protoreflect.StringKind {
				h.Desc, h.has = v.Get(f).String(), h.has|Desc
			}

		case fieldLabels:
			if !f.IsMap() {
				continue
			}

			h.has |= Labels
			v.Get(f).Map().Range(func(k protoreflect.MapKey, w protoreflect.Value) bool {
				if h.Labels == nil {
					h.Labels = map[string]string{}
				}

				h.Labels[k.String()] = w.String()

				return true
			})

		case fieldTenant, fieldTenant2:
			// An edge or a column. The first is a nested message whose own `id`
			// is the answer; the second is the identifier itself, which is what
			// a row that has to outlive its tenant holds.
			if f.Kind() == protoreflect.MessageKind {
				if k := Of(v.Get(f).Message().Interface()); k.Has(Id) {
					h.Tenant, h.has = k.Id, h.has|Tenant
				}

				continue
			}
			if k, ok := idOf(v, f); ok {
				h.Tenant, h.has = k, h.has|Tenant
			}

		case fieldCreated:
			if t, ok := timeOf(v, f); ok {
				h.Created, h.has = t, h.has|Created
			}

		case fieldUpdated:
			if t, ok := timeOf(v, f); ok {
				h.Updated, h.has = t, h.has|Updated
			}

		case fieldErased:
			if t, ok := timeOf(v, f); ok {
				h.Erased, h.has = t, h.has|Erased
			}
		}
	}

	return h
}

// idOf reads a field of sixteen bytes as an identifier.
//
// A field that is bytes and is not sixteen of them is **present and empty**,
// which is what an Add that has not been answered yet looks like: the field is
// declared, so [Header.Has] says so, and the value is the zero identifier.
func idOf(v protoreflect.Message, f protoreflect.FieldDescriptor) (pdid.Id, bool) {
	if f.Kind() != protoreflect.BytesKind {
		return pdid.Id{}, false
	}

	b := v.Get(f).Bytes()
	if len(b) != len(pdid.Id{}) {
		return pdid.Id{}, true
	}

	return pdid.Id(b), true
}

// timeOf reads a `google.protobuf.Timestamp` field.
func timeOf(v protoreflect.Message, f protoreflect.FieldDescriptor) (time.Time, bool) {
	if f.Kind() != protoreflect.MessageKind {
		return time.Time{}, false
	}
	if f.Message().FullName() != "google.protobuf.Timestamp" {
		return time.Time{}, false
	}
	if !v.Has(f) {
		return time.Time{}, true
	}

	t, ok := v.Get(f).Message().Interface().(*timestamppb.Timestamp)
	if !ok {
		return time.Time{}, true
	}

	return t.AsTime(), true
}
