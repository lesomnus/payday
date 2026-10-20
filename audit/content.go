package audit

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/lesomnus/protobuf-patch/patch"
	"github.com/lesomnus/protobuf-patch/patchpb"
	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpatch"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdid"
)

// Content is how much of a write a row of the trail holds, beyond who made it,
// what they asked for, which row it was and when (#92).
//
// # Why it is a choice, and why the patch is the default
//
// The trail used to hold the whole row after every write. That is the one
// table nothing rewrites, so every value in it outlives every correction made
// to the row it came from: a write that changed a person's team copied their
// address in again, and taking a person out of the trail meant blanking every
// copy of them, in the database and in the archive. What a write said is what
// a trail is for -- who changed what, and to what -- and the row it left is in
// its own table, where it is the person's to correct.
//
// So the default is the document the write was compiled from, and the row is
// what a deployment asks for, by kind, when it has a reason to keep one. A
// field declared `personal` or `secret` is on the trail at no level: what it
// was set to is taken out, and that it was set is in `Audit.fields`.
type Content uint8

const (
	// ContentPatch keeps the fields a write set, and the document it was
	// compiled from; for an Add, the row it made as a patch from the empty row
	// ([Created]). The zero value, and the default.
	ContentPatch Content = iota

	// ContentFields keeps the numbers of the fields a write set, and nothing
	// they were set to.
	ContentFields

	// ContentValue keeps what [ContentPatch] keeps, and the row as the write
	// left it.
	ContentValue
)

var contentNames = map[Content]string{
	ContentFields: "fields",
	ContentPatch:  "patch",
	ContentValue:  "value",
}

func (c Content) String() string {
	if v, ok := contentNames[c]; ok {
		return v
	}

	return fmt.Sprintf("Content(%d)", uint8(c))
}

// ParseContent reads a content level as configuration writes one: `fields`,
// `patch` or `value`, and empty for the default.
//
// Refused rather than ignored: a deployment that meant `value` and wrote
// `values` has a trail that silently keeps less than it was told to, which is
// the failure a trail exists to make loud.
func ParseContent(v string) (Content, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ContentPatch, nil
	}
	for c, n := range contentNames {
		if n == v {
			return c, nil
		}
	}

	return ContentPatch, fmt.Errorf("%q is not what a row of the trail can hold; it is one of fields, patch or value", v)
}

// Contents is the content of each kind of thing's rows of the trail: Default
// for every kind By does not name. The zero value is [ContentPatch] for all of
// them.
//
// Digest is the key a row's digest of the row its write left is made under,
// and nil for none (payday#102). It is here because it is the other thing a
// row of the trail holds of a write, and every recorder is handed this.
type Contents struct {
	Default Content
	By      map[pdid.Domain]Content
	Digest  *Digest
}

// Of is the content of the rows about a thing of domain `d`.
func (c Contents) Of(d pdid.Domain) Content {
	if v, ok := c.By[d]; ok {
		return v
	}

	return c.Default
}

// Withhold is `p` without the entries that set any of `fds`, or nil when there
// is nothing left of it.
//
// An entry names the field it sets by its first segment: the first of its
// path, or the keys of its selectors when it has none. A document the servers
// compile names a field by its number and its name both, and one an `Apply`
// was handed may name it by either alone, or by its JSON name -- so a field is
// matched by any of the three. Matching the number alone, which is what this
// did before #92, let a hand-written document set a `secret:` field by its name
// and carry the value onto the trail.
//
// The first segment and no other, because that is the entity's own field and
// what the declaration is on: anything deeper is inside it.
func Withhold(p *patchpb.Patch, fds []protoreflect.FieldDescriptor) *patchpb.Patch {
	if p == nil {
		return nil
	}
	if len(fds) == 0 {
		return p
	}

	kept := []*patchpb.Entry{}
	for _, e := range p.GetDelta().GetEntries() {
		if slices.ContainsFunc(named(e), func(f *patchpb.Field) bool { return is(f, fds) }) {
			continue
		}

		kept = append(kept, e)
	}
	if len(kept) == len(p.GetDelta().GetEntries()) {
		return p
	}
	if len(kept) == 0 {
		// A document of nothing at all is not one: a delta carries at least
		// one entry, so a write whose every entry was withheld leaves the
		// column empty, as a write with no document does. `Audit.fields` is
		// what still says it set something.
		return nil
	}

	// A copy, because the document is the caller's and the write it was
	// handed with is still going to apply it.
	out := proto.CloneOf(p)
	out.GetDelta().SetEntries(kept)

	return out
}

// FieldsOf is the numbers of the fields of `md` that `p` sets, each once and in
// order -- read off the first segment of each of its entries, by number, by
// name or by JSON name. A segment that names no field of `md` is left out: a
// document that names one is not one the servers compiled.
func FieldsOf(p *patchpb.Patch, md protoreflect.MessageDescriptor) []uint32 {
	out := []uint32{}
	for _, e := range p.GetDelta().GetEntries() {
		for _, f := range named(e) {
			if fd := resolve(f, md); fd != nil {
				out = append(out, uint32(fd.Number()))
			}
		}
	}
	slices.Sort(out)

	return slices.Compact(out)
}

// named is the fields an entry names with its first segment.
func named(e *patchpb.Entry) []*patchpb.Field {
	if ss := e.GetPath().GetSegments(); len(ss) > 0 {
		if !ss[0].HasField() {
			return nil
		}

		return []*patchpb.Field{ss[0].GetField()}
	}

	out := []*patchpb.Field{}
	for _, s := range e.GetTargets().GetSelectors() {
		if k := s.GetKey(); k.HasField() {
			out = append(out, k.GetField())
		}
	}

	return out
}

// is reports whether `f` names any of `fds`.
func is(f *patchpb.Field, fds []protoreflect.FieldDescriptor) bool {
	return slices.ContainsFunc(fds, func(fd protoreflect.FieldDescriptor) bool {
		switch {
		case f.HasNumber() && f.GetNumber() == uint32(fd.Number()):
			return true
		case f.HasName() && f.GetName() == string(fd.Name()):
			return true
		case f.HasJsonName() && f.GetJsonName() == fd.JSONName():
			return true
		}

		return false
	})
}

// resolve is the field of `md` that `f` names.
func resolve(f *patchpb.Field, md protoreflect.MessageDescriptor) protoreflect.FieldDescriptor {
	fs := md.Fields()
	switch {
	case f.HasNumber():
		return fs.ByNumber(protoreflect.FieldNumber(f.GetNumber()))
	case f.HasName():
		return fs.ByName(protoreflect.Name(f.GetName()))
	case f.HasJsonName():
		return fs.ByJSONName(f.GetJsonName())
	}

	return nil
}

// Created is the document an Add is recorded with: the row it made, as a patch
// from the empty row -- an entry for every field the row holds, and an edge by
// the key of the row it names, which is how a Patch of the same fields says
// them (`ormpatch.FromPatchRequest`). Nil for a row that holds nothing.
//
// An Add is compiled from no document, so this is the record of what it made.
// It is the row and not the request, because the row is what the recorder is
// handed: the timestamps and the defaults the server settled on are in it,
// which a document of the request would not have said. The key is left out --
// it is `object_id` -- and so is an edge to many, whose rows are the other
// table's.
func Created(row proto.Message) (*patchpb.Patch, error) {
	m := row.ProtoReflect()
	e, err := entityOf(m.Descriptor())
	if err != nil {
		return nil, err
	}

	entries := []*patchpb.Entry{}
	for p := range e.Props() {
		fd := p.Descriptor()
		at := patch.At(string(fd.Name()))
		switch p := p.(type) {
		case graph.Edge:
			if p.IsList() || !m.Has(fd) {
				continue
			}

			key := p.Target().Key()
			kfd := key.Descriptor()
			v, err := patch.ValueOf(m.Get(fd).Message().Get(kfd), kfd, patch.SiteField, at)
			if err != nil {
				return nil, fmt.Errorf("the row %s made: %w", e.FullName(), err)
			}
			// Creating the edge's message where it is absent, which is what
			// a patch from the empty row has to say: applied to a row with
			// no such message, a path into it reaches nothing, and the
			// document is meant to be one a reader can apply to nothing and
			// get the row back.
			entries = append(entries, patchpb.Entry_builder{
				Path:         patchpb.Path_builder{Segments: []*patchpb.Key{keyOf(p)}}.Build(),
				Targets:      targetOf(keyOf(key)),
				OnAbsentPath: patchpb.OnAbsentPath_ON_ABSENT_PATH_CREATE.Enum(),
				Assign:       patchpb.Assign_builder{Value: v}.Build(),
			}.Build())

		case graph.Field:
			if p.Number() == e.Key().Number() || !holds(m, fd) {
				continue
			}

			v, err := patch.ValueOf(m.Get(fd), fd, patch.SiteField, at)
			if err != nil {
				return nil, fmt.Errorf("the row %s made: %w", e.FullName(), err)
			}
			entries = append(entries, patchpb.Entry_builder{
				Targets: targetOf(keyOf(p)),
				Assign:  patchpb.Assign_builder{Value: v}.Build(),
			}.Build())
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}

	return patchpb.Patch_builder{
		MessageType: proto.String(string(e.FullName())),
		Delta:       patchpb.Delta_builder{Entries: entries}.Build(),
	}.Build(), nil
}

// holds reports whether a row holds a value of `fd`: a collection that is not
// empty, or a field the message has.
func holds(m protoreflect.Message, fd protoreflect.FieldDescriptor) bool {
	switch {
	case fd.IsMap():
		return m.Get(fd).Map().Len() > 0
	case fd.IsList():
		return m.Get(fd).List().Len() > 0
	}

	return m.Has(fd)
}

// keyOf names a prop by its name and its number both, as the servers' own
// documents do, so that one read against a renumbered entity is a conflict
// rather than another column.
func keyOf(p graph.Prop) *patchpb.Key {
	return patchpb.Key_builder{Field: patchpb.Field_builder{
		Name:   proto.String(p.Name()),
		Number: proto.Uint32(uint32(p.Number())),
	}.Build()}.Build()
}

func targetOf(k *patchpb.Key) *patchpb.Targets {
	return patchpb.Targets_builder{Selectors: []*patchpb.Selector{
		patchpb.Selector_builder{Key: k}.Build(),
	}}.Build()
}

// entities is every entity [Created] has been asked about, by message: what the
// schema says of one does not change while the process runs, and reading it is
// a walk of its file.
var entities sync.Map

func entityOf(md protoreflect.MessageDescriptor) (graph.Entity, error) {
	if v, ok := entities.Load(md.FullName()); ok {
		return v.(graph.Entity), nil
	}

	e, err := ormpatch.EntityOf(md.ParentFile(), md.Name())
	if err != nil {
		return nil, fmt.Errorf("the row %s made is not an entity's: %w", md.FullName(), err)
	}
	entities.Store(md.FullName(), e)

	return e, nil
}

// Clear clears `fds` out of `m`, in place: a row as the trail keeps one, without
// what its entity declared the trail holds no value of.
func Clear(m proto.Message, fds []protoreflect.FieldDescriptor) {
	r := m.ProtoReflect()
	for _, fd := range fds {
		r.Clear(fd)
	}
}
