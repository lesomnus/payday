package pdcli

import (
	"fmt"
	"strings"

	"github.com/protobuf-orm/protobuf-orm/graph"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/internal/pdgen"
)

// A field that may hold a person, and that the schema says nothing about.
//
// `personal` keeps a person's values off the trail (#92), and a field nobody
// declared is copied onto it as a value -- at `patch` and at `value`, in the one
// table nothing rewrites, signed and archived. Whether a string is about
// somebody is the schema's to say, and payday cannot tell from a type. What it
// can tell is when the schema has not said, which is what this asks about, in
// two places:
//
//   - An entity with a personal field holds people, so every field of free text
//     on it is one somebody could have meant to declare. Each says which it is:
//     `personal`, or `impersonal` for a role, a state, the name of a provider --
//     looked at, and about no one.
//   - A field anywhere whose name is about somebody -- an address, a phone, a
//     birthday -- is asked about the same way, so that an entity that holds
//     people and declares nothing at all is not the one that gets through.
//
// Advice and not a refusal: generation does what it always did, and the
// decision is the schema's to make. What it is not is quiet.
func doctorPersonal(s *pdgen.Schema) []Finding {
	var vs []Finding
	for _, e := range s.Sorted() {
		decided := map[string]bool{}
		for _, names := range [][]string{e.Personal, e.Impersonal, e.Secrets} {
			for _, v := range names {
				decided[v] = true
			}
		}

		people := len(e.Personal) > 0
		var asked []graph.Prop
		for p := range e.Props() {
			if decided[p.Name()] || p.Number() == e.Key().Number() {
				continue
			}
			// An identifier, which is what the trail is made of and which
			// generation refuses to call personal; what the row it names
			// holds is asked about there.
			if _, ok := p.(graph.Edge); ok {
				continue
			}
			if (people && freeText(p)) || namedForSomebody(p.Name()) {
				asked = append(asked, p)
			}
		}
		if len(asked) == 0 {
			continue
		}

		vs = append(vs, personalFinding(e, people, asked))
	}

	return vs
}

func personalFinding(e *pdgen.Entity, people bool, asked []graph.Prop) Finding {
	names := make([]string, len(asked))
	for i, p := range asked {
		names[i] = p.Name()
	}

	what := fmt.Sprintf("%s holds people and says nothing of %s", e.FullName(), strings.Join(names, ", "))
	if !people {
		what = fmt.Sprintf("%s says nothing of %s, which %s named for something about a person", e.FullName(), strings.Join(names, ", "), are(len(names)))
	}

	fix := &strings.Builder{}
	fix.WriteString("A field nobody declared is kept on the trail as a value. Declare each beside it:\n\n")
	fix.WriteString("    [(payday.field) = {personal: true}]    about a person: never on the trail as a value\n")
	fix.WriteString("    [(payday.field) = {impersonal: true}]  about no one: kept, and now on purpose\n")
	for _, p := range asked {
		fmt.Fprintf(fix, "\n%s  %s", where(p), p.Name())
	}

	return Finding{What: what, Fix: fix.String()}
}

func are(n int) string {
	if n == 1 {
		return "is"
	}

	return "are"
}

// freeText is whether a field holds what a person writes, which is where a
// person ends up: a string, bytes, a document, or a list or a map of them. A
// number, a time, a state out of an enum, a flag and an identifier do not.
func freeText(p graph.Prop) bool {
	switch p.Type() {
	case ormpb.Type_TYPE_UUID, ormpb.Type_TYPE_TIME, ormpb.Type_TYPE_BOOL, ormpb.Type_TYPE_ENUM,
		ormpb.Type_TYPE_INT, ormpb.Type_TYPE_UINT, ormpb.Type_TYPE_FLOAT, ormpb.Type_TYPE_DOUBLE:
		return false
	case ormpb.Type_TYPE_JSON:
		return true
	}

	d := p.Descriptor()
	if d.IsMap() {
		d = d.MapValue()
	}

	switch d.Kind() {
	case protoreflect.StringKind, protoreflect.BytesKind:
		return true
	}

	return false
}

// aboutSomebody are the words a field's name is made of when it is about a
// person: how they are reached, who they are, what they look like. Whole words
// of the name, so that `hotel` is not `tel` -- and not `name`, which is every
// entity's, people's or not; an entity of people has it declared already, and
// is asked about the rest of its fields for that reason.
var aboutSomebody = map[string]bool{
	"email": true, "phone": true, "mobile": true, "tel": true, "telephone": true, "fax": true,
	"address": true,
	"birth":   true, "birthday": true, "birthdate": true, "dob": true,
	"gender": true, "sex": true, "nationality": true,
	"nickname": true, "surname": true, "forename": true,
	"passport": true, "ssn": true, "resident": true,
	"portrait": true, "avatar": true, "photo": true,
}

// aboutSomebodyTwice are the names that are about a person only as a pair:
// `first_name`, where neither word is.
var aboutSomebodyTwice = map[[2]string]bool{
	{"first", "name"}: true, {"last", "name"}: true, {"given", "name"}: true,
	{"family", "name"}: true, {"full", "name"}: true, {"middle", "name"}: true,
	{"real", "name"}: true, {"legal", "name"}: true,
	{"national", "id"}: true,
}

func namedForSomebody(name string) bool {
	words := strings.Split(strings.ToLower(name), "_")
	for i, w := range words {
		if aboutSomebody[w] {
			return true
		}
		if i > 0 && aboutSomebodyTwice[[2]string{words[i-1], w}] {
			return true
		}
	}

	return false
}

// where is the line a field is declared on, as a path from the app's root, or
// the file alone when the schema was built without its source.
func where(p graph.Prop) string {
	d := p.Descriptor()
	f := d.ParentFile()
	at := DirProto + "/" + f.Path()

	loc := f.SourceLocations().ByDescriptor(d)
	if loc.Path == nil {
		return at
	}

	return fmt.Sprintf("%s:%d", at, loc.StartLine+1)
}
