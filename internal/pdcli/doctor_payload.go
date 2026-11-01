package pdcli

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
)

// What a notification payload can hold and still generate (payday#193).
//
// `pd gen` refuses a payload that declares a person in it, holds a row or a
// contract's message, or an Any. What is left is what it cannot tell, because
// it is a question of what a string means: whether `outcome` is a code or a
// sentence, and whether a sentence has a name in it. A notification is shown
// to every reader of its topic for as long as it is kept, and nothing that
// keeps a person's data in step -- the access record, forgetting somebody --
// looks inside its payload. So the doctor asks, of each payload and every
// message it holds:
//
//   - of every string, or list or map of them, that says nothing about itself:
//     free text is where a name ends up, and it belongs in the sentence the
//     reader makes, from identifiers, not in what is kept. One that is a code,
//     a version or another system's identifier says `personal: false`.
//   - of a field of any type whose name is about somebody, with personal.go's
//     words, as the past's event messages are asked.
//
// A `bytes` field is taken for an identifier, which is what one holds here.
//
// And of an app that declares notifications and marks no message at all, that
// every payload it publishes is refused ([unmarkedPayloads]).
//
// Advice and not a refusal, as `personal` is: generation does what it always
// did, and what a field holds is the schema's to say.
func doctorPayloads(s *pdgen.Schema) []Finding {
	// A message two payloads hold is asked about once, under the first.
	seen := map[protoreflect.FullName]bool{}

	var vs []Finding
	if s.Notify != nil && len(s.Payloads) == 0 {
		vs = append(vs, unmarkedPayloads(s.Notify))
	}
	for _, p := range s.Payloads {
		var asked []protoreflect.FieldDescriptor
		var walk func(m protoreflect.MessageDescriptor)
		walk = func(m protoreflect.MessageDescriptor) {
			if seen[m.FullName()] {
				return
			}
			seen[m.FullName()] = true

			fs := m.Fields()
			for i := range fs.Len() {
				f := fs.Get(i)
				if !aboutNoOne(f) && (payloadText(f) || namedForSomebody(string(f.Name()))) {
					asked = append(asked, f)
				}

				held := f
				if f.IsMap() {
					held = f.MapValue()
				}
				if held.Kind() == protoreflect.MessageKind || held.Kind() == protoreflect.GroupKind {
					if t := held.Message(); !strings.HasPrefix(t.ParentFile().Path(), "google/protobuf/") {
						walk(t)
					}
				}
			}
		}
		walk(p.Desc)
		if len(asked) == 0 {
			continue
		}

		names := make([]string, len(asked))
		for i, f := range asked {
			names[i] = heldName(p.FullName(), f)
		}

		fix := &strings.Builder{}
		fix.WriteString("A notification is shown to every reader of its topic for as long as it is kept, and nothing " +
			"that keeps a person's data in step -- the access record, forgetting somebody -- looks inside its payload. " +
			"Name whoever it is about by identifier, and the page that shows it says them as they are called then:\n\n")
		fix.WriteString("    bytes holder = 2;   // their identifier, and nothing of theirs\n\n")
		fix.WriteString("A field about no one -- a code, a version, another system's identifier -- says so:\n\n")
		fix.WriteString("    [(payday.field) = {personal: false}]\n")
		for _, f := range asked {
			fmt.Fprintf(fix, "\n%s  %s", whereField(f), heldName(p.FullName(), f))
		}

		vs = append(vs, Finding{
			What: fmt.Sprintf("notification payload %s holds %s, which %s free text that says nothing about itself or "+
				"named for something about a person, and a notification names people by identifier only",
				p.FullName(), strings.Join(names, ", "), are(len(names))),
			Fix: fix.String(),
		})
	}

	return vs
}

// payloadText is whether a field of a payload holds text: a string, or a list
// or a map of them. `bytes` is an identifier's, here.
func payloadText(f protoreflect.FieldDescriptor) bool {
	if f.IsMap() {
		f = f.MapValue()
	}

	return f.Kind() == protoreflect.StringKind
}

// aboutNoOne is whether a field says `(payday.field) = {personal: false}`.
func aboutNoOne(f protoreflect.FieldDescriptor) bool {
	opts := f.Options()
	if opts == nil || !proto.HasExtension(opts, pdpb.E_Field) {
		return false
	}
	v, _ := proto.GetExtension(opts, pdpb.E_Field).(*pdpb.Field)

	return v.HasPersonal() && !v.GetPersonal()
}

// unmarkedPayloads is the finding of an app that declares notifications and no
// message one may carry.
//
// `NotificationService.Add` takes a payload only of a message the schema marks
// (payday#193), so in such an app every notification that carries one is
// refused -- when it is published, by `InvalidArgument`, and not before. That
// is the shape an app upgraded across #193 is in until it marks what it
// publishes, and nothing else says so. Advice and not a refusal: a
// notification with no payload is a notification, and an app that publishes
// only those marks nothing.
func unmarkedPayloads(n *pdgen.Notify) Finding {
	return Finding{
		What: fmt.Sprintf("%s declares notifications and no message one may carry, so NotificationService.Add "+
			"refuses every notification this app publishes with a payload: a payload's type has to say "+
			"`option (payday.notification) = {}` (payday#193)", n.At),
		Fix: `Mark each message your app publishes as a notification's payload:

    message JobDone {
      option (payday.notification) = {};

      bytes job = 1;   // what it is about, by identifier
    }

A notification with no payload needs nothing. docs/migrating.md, "A notification's payload is a message the schema declares", has the rest.`,
	}
}
