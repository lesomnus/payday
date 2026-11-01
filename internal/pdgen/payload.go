package pdgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdpb"
)

// Payload is one message the app declared to be what its notifications carry
// -- `option (payday.notification) = {}` (payday#193).
//
// It is not an entity and has no table: a notification keeps it packed in its
// `payload`, as bytes nothing on the trail, the access record or an erasure
// looks inside. That is what every rule [readPayloads] refuses with is about.
type Payload struct {
	// Desc is the message.
	Desc protoreflect.MessageDescriptor
}

// FullName is the payload message's.
func (p *Payload) FullName() protoreflect.FullName { return p.Desc.FullName() }

// readPayloads gathers the notification payloads of the app's own files,
// nested ones included, and refuses one that would hold a person: a
// notification carries identifiers, and the reader says the people they name
// (payday#193).
//
// It answers too with every message a payload holds, itself included: a field
// of one may say `(payday.field) = {personal: false}`, which [checkFieldOptions]
// refuses of any other message that is not an entity's own.
//
// Before [checkFieldOptions] for that reason, as [readEvents] is.
func readPayloads(g *graph.Graph, files []*protogen.File) ([]*Payload, map[protoreflect.FullName]bool, error) {
	var vs []*Payload
	held := map[protoreflect.FullName]bool{}

	var walk func(ms []*protogen.Message) error
	walk = func(ms []*protogen.Message) error {
		for _, m := range ms {
			if err := walk(m.Messages); err != nil {
				return err
			}

			opts := m.Desc.Options()
			if opts == nil || !proto.HasExtension(opts, pdpb.E_Notification) {
				continue
			}

			if _, ok := g.Entities[m.Desc.FullName()]; ok {
				return fmt.Errorf(
					"%s: says it is a notification payload, and is an entity. A notification names a row by its "+
						"identifier -- `bytes %s = 1` -- and the page that shows it reads the row as its reader may",
					m.Desc.FullName(), snake(string(m.Desc.Name())))
			}

			v := &Payload{Desc: m.Desc}
			if err := checkPayloadFields(g, v, m.Desc, held); err != nil {
				return err
			}
			vs = append(vs, v)
		}

		return nil
	}

	for _, f := range files {
		if !f.Generate {
			continue
		}
		if err := walk(f.Messages); err != nil {
			return nil, nil, err
		}
	}

	sort.Slice(vs, func(i, j int) bool { return vs[i].FullName() < vs[j].FullName() })

	return vs, held, nil
}

// checkPayloadFields refuses what in a payload, or a message it holds, would
// keep a person: a `(payday.field)` that says anything but `personal: false`,
// an entity, a message of a generated contract, and an Any.
func checkPayloadFields(g *graph.Graph, p *Payload, m protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) error {
	if seen[m.FullName()] {
		return nil
	}
	seen[m.FullName()] = true

	in := ""
	if m.FullName() != p.FullName() {
		in = fmt.Sprintf(", which notification payload %s holds", p.FullName())
	}

	fs := m.Fields()
	for i := range fs.Len() {
		f := fs.Get(i)

		if opts := f.Options(); opts != nil && proto.HasExtension(opts, pdpb.E_Field) {
			v, _ := proto.GetExtension(opts, pdpb.E_Field).(*pdpb.Field)
			if !v.HasPersonal() || v.GetPersonal() || v.GetSecret() || v.GetStamped() || v.GetEvent() {
				return fmt.Errorf(
					"%s: (payday.field) in a notification payload%s says more than `personal: false`. A "+
						"notification names people by identifier and holds nothing of theirs: it is shown to every "+
						"reader of its topic for as long as it is kept, and neither the access record nor an "+
						"erasure looks inside one. Name whoever it is about by identifier, and say a field about no "+
						"one is so: `[(payday.field) = {personal: false}]`",
					f.FullName(), in)
			}
		}

		// What a map holds is its value; its key is a scalar.
		held := f
		if f.IsMap() {
			held = f.MapValue()
		}
		if held.Kind() != protoreflect.MessageKind && held.Kind() != protoreflect.GroupKind {
			continue
		}

		t := held.Message()
		switch {
		case t.FullName() == "google.protobuf.Any":
			return fmt.Errorf(
				"%s: holds a google.protobuf.Any, in a notification payload%s. A payload is the one Any of its "+
					"notification already, and an Any in it holds a message nothing declared, which no rule here "+
					"can be held to. Declare the message it holds",
				f.FullName(), in)

		case g.Entities[t.FullName()] != nil:
			return fmt.Errorf(
				"%s: holds %s, an entity, in a notification payload%s. A notification names a row by its "+
					"identifier -- `bytes %s = %d` -- and one holding the row would show every field of it, the "+
					"personal ones included, to every reader of the topic for as long as it is kept",
				f.FullName(), t.FullName(), in, f.Name(), f.Number())

		case strings.HasSuffix(t.ParentFile().Path(), "_svc.g.proto"):
			return fmt.Errorf(
				"%s: holds %s, a message of a generated contract (%s), in a notification payload%s. A "+
					"contract's messages are the API's and move with the entity: a Ref carries a slug, which is "+
					"somebody's name for the row, and a request carries the entity's fields. Name the row by its "+
					"identifier -- `bytes %s = %d`",
				f.FullName(), t.FullName(), t.ParentFile().Path(), in, f.Name(), f.Number())

		case wellKnown(t):
			continue
		}

		if err := checkPayloadFields(g, p, t, seen); err != nil {
			return err
		}
	}

	return nil
}

// checkPayloads refuses a payload declared in an app that declares no
// notifications to carry it.
func (s *Schema) checkPayloads() error {
	if len(s.Payloads) == 0 || s.Notify != nil {
		return nil
	}

	return fmt.Errorf(
		"%s: says it is a notification payload, and this app declares no notifications to carry it; declare them "+
			"on the file that says the package is the app's:\n\n"+
			"    option (payday.app) = {notify: {notification: <domain>, receipt: <domain>, keep: {days: <days>}}};",
		s.Payloads[0].FullName())
}
