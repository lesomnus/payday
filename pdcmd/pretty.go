package pdcmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Pretty is one message, field by field, for a person.
//
// # Why it is the default rather than prototext
//
// prototext is exact and unreadable here, and the reason is payday's own
// schema. Every identifier is a `bytes` field, and prototext prints bytes as an
// escaped string:
//
//	id:  "\x01\xa0\x01\x03ͤ\x8d\xed\xbd\x02\xb4\xbc_\x1e\xa7\x9e"
//
// That is the identifier a person then has to type back, and there is no way to
// get from that line to the uuid it is. Timestamps are the other half: a
// `google.protobuf.Timestamp` prints as a `seconds`/`nanos` pair, and an unset
// one prints as `seconds: -62135596800`, which is year one.
//
// So the default format was the one a person sees most and the one they could
// use least. It is worth being blunt about why that happened: prototext is what
// a protobuf library hands you, and taking it means the format nobody chose is
// the format everybody gets. `kubectl get` has the same shape of problem and
// answered it the same way -- the human format is the one with work in it, and
// the exact serialisations are behind `-o`.
//
// `-o text` is still prototext, unchanged. When the question is what is
// actually on the wire, that is the answer, and this is not it.
//
// # What it knows
//
// Only what the schema already says. A `bytes` field named `id` or ending in
// `_id` is an identifier, because that is payday's field-number rule written in
// names; a `Timestamp` is a time. Nothing here is a guess about a particular
// app's entity, which is why it needs nothing declared to be useful.
var Pretty Printer = PrinterFunc(func(w io.Writer, m proto.Message) error {
	rows := Rows(m)
	for i, row := range rows {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := pretty(w, row, ""); err != nil {
			return err
		}
	}

	// What a list did not say: `next` is the only field of a list response that
	// is not a row, and a person paging through needs it. Printed only when
	// there is more, because empty means there was not.
	v := m.ProtoReflect()
	if fd := v.Descriptor().Fields().ByName("next"); fd != nil && fd.Kind() == protoreflect.StringKind {
		if s := v.Get(fd).String(); s != "" {
			fmt.Fprintf(w, "\nnext: %s\n", s)
		}
	}

	return nil
})

func pretty(w io.Writer, m protoreflect.Message, indent string) error {
	fs := m.Descriptor().Fields()

	// Widest name first, so the values line up. Over the fields that will
	// actually be printed rather than all of them, or one long name nothing
	// sets pushes every value across the screen.
	width := 0
	shown := make([]protoreflect.FieldDescriptor, 0, fs.Len())
	for i := range fs.Len() {
		fd := fs.Get(i)
		if !m.Has(fd) {
			continue
		}
		if isZeroTime(m, fd) {
			continue
		}

		shown = append(shown, fd)
		if n := len(fd.Name()); n > width {
			width = n
		}
	}

	for _, fd := range shown {
		name := fmt.Sprintf("%s%-*s ", indent, width, fd.Name())
		v := m.Get(fd)

		switch {
		case fd.IsList():
			l := v.List()
			fmt.Fprintf(w, "%s(%d)\n", name, l.Len())
			for i := range l.Len() {
				if fd.Kind() == protoreflect.MessageKind {
					if err := pretty(w, l.Get(i).Message(), indent+"  "); err != nil {
						return err
					}
					continue
				}

				fmt.Fprintf(w, "%s  %s\n", indent, l.Get(i).String())
			}

		case fd.IsMap():
			fmt.Fprintf(w, "%s\n", name)
			v.Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
				fmt.Fprintf(w, "%s  %s: %s\n", indent, k.String(), v.String())
				return true
			})

		case isTimestamp(fd):
			fmt.Fprintf(w, "%s%s\n", name, timeOf(v))

		case fd.Kind() == protoreflect.MessageKind:
			// A nested entity is shown on one line when it can be -- its
			// identifier and its alias are what somebody is looking for, and
			// the rest of it is a `get` of its own.
			if s, ok := oneLine(v.Message()); ok {
				fmt.Fprintf(w, "%s%s\n", name, s)
				continue
			}

			fmt.Fprintf(w, "%s\n", strings.TrimRight(name, " "))
			if err := pretty(w, v.Message(), indent+"  "); err != nil {
				return err
			}

		case fd.Kind() == protoreflect.BytesKind && isId(fd):
			fmt.Fprintf(w, "%s%s\n", name, uuidOf(v.Bytes()))

		case fd.Kind() == protoreflect.BytesKind:
			// Not an identifier, so there is nothing better to say than how
			// much of it there is. Printing the bytes themselves is what makes
			// a `secret` field land in a terminal's scrollback.
			fmt.Fprintf(w, "%s(%d bytes)\n", name, len(v.Bytes()))

		case fd.Kind() == protoreflect.StringKind:
			fmt.Fprintf(w, "%s%s\n", name, v.String())

		default:
			fmt.Fprintf(w, "%s%v\n", name, v.Interface())
		}
	}

	return nil
}

// oneLine is a nested message worth a line rather than a block: an entity,
// named the way a person names one.
func oneLine(m protoreflect.Message) (string, bool) {
	fs := m.Descriptor().Fields()

	idf := fs.ByName("id")
	if idf == nil || idf.Kind() != protoreflect.BytesKind || !m.Has(idf) {
		return "", false
	}

	s := uuidOf(m.Get(idf).Bytes())
	if af := fs.ByName("alias"); af != nil && af.Kind() == protoreflect.StringKind && m.Has(af) {
		s = fmt.Sprintf("%s (%s)", m.Get(af).String(), s)
	}

	return s, true
}

// isId says a `bytes` field holds an identifier.
//
// By name, because that is what payday's field-number rule is written in: `id`
// is the key, and everything that points at one is `<something>_id` -- there is
// no `bytes` field in payday's own entities that is named that way and is not
// an identifier.
func isId(fd protoreflect.FieldDescriptor) bool {
	n := string(fd.Name())
	return n == "id" || strings.HasSuffix(n, "_id")
}

func isTimestamp(fd protoreflect.FieldDescriptor) bool {
	return fd.Kind() == protoreflect.MessageKind &&
		fd.Message().FullName() == "google.protobuf.Timestamp"
}

// isZeroTime is a timestamp that is set and means nothing.
//
// It happens: a `Select` that asked for a nested entity answers with one whose
// timestamps were never read, and prototext prints those as year one -- three
// lines of `seconds: -62135596800` in the middle of what somebody is reading.
func isZeroTime(m protoreflect.Message, fd protoreflect.FieldDescriptor) bool {
	if !isTimestamp(fd) || fd.IsList() {
		return false
	}

	return asTime(m.Get(fd)).IsZero()
}

func timeOf(v protoreflect.Value) string {
	t := asTime(v)
	if t.IsZero() {
		return "-"
	}

	return t.UTC().Format(time.RFC3339)
}

func asTime(v protoreflect.Value) time.Time {
	ts := &timestamppb.Timestamp{}
	b, err := proto.Marshal(v.Message().Interface())
	if err != nil || proto.Unmarshal(b, ts) != nil {
		return time.Time{}
	}

	return ts.AsTime()
}
