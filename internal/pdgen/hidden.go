package pdgen

import (
	"slices"
	"strings"
	"unicode"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// # What the Secret layer looks into
//
// The layer clears a field declared `secret` from whatever leaves it, and what
// leaves it is not only an entity's own message. A verb answers the entity,
// a list of it, or a stream of changes to it; an RPC an app writes answers
// whatever it declared, and an entity's edges are themselves entities -- a
// Pairing's `lead` is a Robot, read in full where the caller selected it. So
// for each answer the generator asks what the message **can hold**: through
// every field of a message type, singular or repeated, a map's values, a
// `oneof`'s cases, to any depth.
//
// It is asked of the message types and not of the values, so it is decided
// once, here, and what the layer runs is a function per type that reads the
// fields the type can carry a secret through and no other. Reflection on the
// answering path was the other way, a walk of every message answered, and it
// would have cost every answer of every RPC what only the few that can hold a
// secret need.
//
// # Where it stops
//
// At the types the schema can see. A `google.protobuf.Any`, or `bytes` that
// are a message, hold a type that is decided when the answer is made; nothing
// here can know it, so nothing is written for them and the guide says so where
// it makes the promise.
//
// # Why it ends
//
// The question is answered for a finite set: every message of every file the
// generation was handed, which is where any field's type is declared. A
// message can hold itself (a tree), so the walk is not recursive: it starts
// from the messages that declare a secret -- the entities, since a
// `(payday.field)` anywhere else is refused -- and marks, through each field
// read backwards, what holds one of those, until nothing new is marked. Each
// message is marked once.

// hiding is what can hold a secret, of every message the generation saw.
type hiding struct {
	// secrets is the entity each message that declares a secret is.
	secrets map[protoreflect.FullName]*Entity

	// holds is every message that can hold a secret: one that declares one,
	// and one with a field that can.
	holds map[protoreflect.FullName]bool

	// messages is every message of the generation, by its name.
	messages map[protoreflect.FullName]*protogen.Message
}

func readHiding(s *Schema, files []*protogen.File) *hiding {
	h := &hiding{
		secrets:  map[protoreflect.FullName]*Entity{},
		holds:    map[protoreflect.FullName]bool{},
		messages: map[protoreflect.FullName]*protogen.Message{},
	}

	// Every message, by the messages whose fields can hold it.
	heldBy := map[protoreflect.FullName][]protoreflect.FullName{}
	var walk func(ms []*protogen.Message)
	walk = func(ms []*protogen.Message) {
		for _, m := range ms {
			h.messages[m.Desc.FullName()] = m
			for _, f := range m.Fields {
				if v := heldIn(f); v != nil {
					heldBy[v.Desc.FullName()] = append(heldBy[v.Desc.FullName()], m.Desc.FullName())
				}
			}
			walk(m.Messages)
		}
	}
	for _, f := range files {
		walk(f.Messages)
	}

	var next []protoreflect.FullName
	for _, e := range s.Entities {
		if len(e.SecretNumbers) == 0 {
			continue
		}

		h.secrets[e.FullName()] = e
		h.holds[e.FullName()] = true
		next = append(next, e.FullName())
	}
	for len(next) > 0 {
		v := next[0]
		next = next[1:]
		for _, w := range heldBy[v] {
			if !h.holds[w] {
				h.holds[w] = true
				next = append(next, w)
			}
		}
	}

	return h
}

// heldIn is the message a field holds, as itself or as an element of a list
// or a map's value, and nil for a field that holds none.
func heldIn(f *protogen.Field) *protogen.Message {
	if f.Desc.IsMap() {
		return f.Message.Fields[1].Message
	}

	return f.Message
}

// of is the message of entity `e`, and nil for one the generation was not
// handed the file of.
func (h *hiding) of(e *Entity) *protogen.Message {
	if h == nil {
		return nil
	}

	return h.messages[e.FullName()]
}

// canHold answers whether an answer of message `m` can carry a secret.
func (h *hiding) canHold(m *protogen.Message) bool {
	return m != nil && h != nil && h.holds[m.Desc.FullName()]
}

// hideName is the function that clears what a message of `m` can carry.
//
// The entity's is `hide<E>`, which is what it was before the layer looked
// past an entity's own message. Any other message of the app's own package
// is named after its Go name, which is unique there; one of another package
// carries that package's name before it, and an underscore no Go name of the
// app's package begins a word with.
func hideName(m *protogen.Message, root protogen.GoImportPath) string {
	if m.GoIdent.GoImportPath == root {
		return "hide" + m.GoIdent.GoName
	}

	pkg := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, string(m.Desc.ParentFile().Package()))

	return "hide" + pkg + "_" + m.GoIdent.GoName
}

// emitHides writes the function for each message of `ms` and each it can carry
// a secret through, once each, in the order of their names.
func (h *hiding) emitHides(g *protogen.GeneratedFile, root protogen.GoImportPath, ms []*protogen.Message) {
	all := map[protoreflect.FullName]*protogen.Message{}
	var walk func(m *protogen.Message)
	walk = func(m *protogen.Message) {
		if !h.canHold(m) || all[m.Desc.FullName()] != nil {
			return
		}

		all[m.Desc.FullName()] = m
		for _, f := range m.Fields {
			walk(heldIn(f))
		}
	}
	for _, m := range ms {
		walk(m)
	}

	names := make([]protoreflect.FullName, 0, len(all))
	for k := range all {
		names = append(names, k)
	}
	slices.Sort(names)

	for _, k := range names {
		m := all[k]
		if e := h.secrets[k]; e != nil {
			emitHideEntity(g, root, h, e, m)
		} else {
			name := hideName(m, root)
			g.P("// ", name, " clears every secret a ", m.GoIdent.GoName, " can carry, in the")
			g.P("// fields that can carry one.")
			g.P("func ", name, "(v *", m.GoIdent, ") *", m.GoIdent, " {")
			g.P("	if v == nil {")
			g.P("		return nil")
			g.P("	}")
			g.P("")
			h.emitHeld(g, root, m)
			g.P("	return v")
			g.P("}")
			g.P("")
		}
	}
}

// emitHeld writes, in the body of `m`'s function, a call for each field of it
// that can carry a secret.
//
// By the getter, which answers nil for a message that is not there -- an
// unset field, and a `oneof` whose case is another -- and an empty list or map
// for one with nothing in it, so that every shape is one line and none can
// fail.
func (h *hiding) emitHeld(g *protogen.GeneratedFile, root protogen.GoImportPath, m *protogen.Message) {
	for _, f := range m.Fields {
		v := heldIn(f)
		if !h.canHold(v) {
			continue
		}

		switch {
		case f.Desc.IsMap() || f.Desc.IsList():
			g.P("	for _, w := range v.Get", f.GoName, "() {")
			g.P("		", hideName(v, root), "(w)")
			g.P("	}")
		default:
			g.P("	", hideName(v, root), "(v.Get", f.GoName, "())")
		}
	}
	g.P("")
}
