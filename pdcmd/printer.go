package pdcmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/template"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
)

// Printer turns one answer into what the person asked to see.
//
// One method, because that is the whole of what a format is. It is the shape
// kubectl settled on -- `ResourcePrinter.PrintObj(runtime.Object, io.Writer)`
// -- and it is worth copying for the reason it worked there: every format,
// including the ones that wrap another format, is the same type, so a caller
// that accepts a Printer accepts all of them and one written here is not a
// lesser kind of format than the built-in ones.
//
// The message is the RPC's answer and not a row: a `Get` hands over the entity,
// a `List` hands over the response that holds them. A printer that wants rows
// asks [Rows] for them, which is what [Table] does.
type Printer interface {
	Print(w io.Writer, m proto.Message) error
}

// PrinterFunc is a Printer written as a function.
type PrinterFunc func(w io.Writer, m proto.Message) error

func (f PrinterFunc) Print(w io.Writer, m proto.Message) error { return f(w, m) }

// ProtoText is protobuf's own text format, exactly as the library emits it.
//
// `-o prototext` rather than `-o text` or `-o raw`, and the name is the point:
// it says which encoding this is, so there is nothing to guess about what a
// person gets. `raw` was considered and dropped -- in these commands `raw`
// already names the **input**, the trailing protojson of a request, and one
// word meaning both halves of a call is a word that has to be explained every
// time.
//
// What it is for is the question "what is actually on the wire". It cannot
// mislead: every field that is set is shown, with the name the schema gave it,
// and nothing is derived. That is also why it is not the default -- see
// [Pretty].
//
// With one addition, which is not derived but said: a list that holds nothing
// is `items: []`, the field the answer has when it holds something. The
// library writes a repeated field that is empty as nothing at all, so an empty
// list was an empty document, which reads as no answer and not as a list with
// nothing in it; see [emptyItems]. `items: []` is text format the library reads
// back as the same message.
var ProtoText Printer = PrinterFunc(func(w io.Writer, m proto.Message) error {
	b, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(m)
	if err != nil {
		return err
	}
	if fd := emptyItems(m.ProtoReflect()); fd != nil {
		b = append([]byte(fmt.Sprintf("%s: []\n", fd.TextName())), b...)
	}

	_, err = w.Write(b)
	return err
})

// ProtoJson is protojson, indented.
//
// It costs nothing to support -- every message this app has is a protobuf, so
// the encoder is already linked in -- and it is the format anything downstream
// of a shell can read. `EmitUnpopulated` is off: a field that was not set reads
// as absent rather than as a zero somebody might act on, which matters most for
// the `bytes` identifiers, where an empty one is not a valid id.
//
// A list that holds nothing is `{"items": []}`, the key the answer has when it
// holds something, where protojson writes `{}`: see [emptyItems]. Turning
// `EmitUnpopulated` on for it instead would have answered every other empty
// field of the response too.
var ProtoJson Printer = PrinterFunc(func(w io.Writer, m proto.Message) error {
	var (
		b   []byte
		err error
	)
	if fd := emptyItems(m.ProtoReflect()); fd != nil {
		b, err = withEmptyItems(m, fd)
	} else {
		b, err = protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(m)
	}
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
})

// withEmptyItems is protojson of a list answer with no rows, with the key of
// its rows said, indented as [ProtoJson] indents.
//
// The key goes first, where the generated list responses declare it. Indented
// by encoding/json and not by protojson's own Multiline, which varies its
// whitespace from build to build on purpose; Indent reads past what it was
// given and writes its own.
func withEmptyItems(m proto.Message, fd protoreflect.FieldDescriptor) ([]byte, error) {
	raw, err := protojson.Marshal(m)
	if err != nil {
		return nil, err
	}

	rest := bytes.TrimSpace(raw)
	rest = bytes.TrimSpace(rest[1:]) // past the '{'
	head := fmt.Sprintf("{%q:[]", fd.JSONName())
	if !bytes.HasPrefix(rest, []byte("}")) {
		head += ","
	}

	b := &bytes.Buffer{}
	if err := json.Indent(b, append([]byte(head), rest...), "", "  "); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}

// emptyItems is the field of a list answer's rows when it holds none, and nil
// for anything else: a list answer that has rows, a `Get`'s entity, a message
// with no `items`.
//
// An empty list is a list. Every encoder here writes a repeated field that is
// empty as nothing, so `-o json` of a list with no rows was `{}`: a script
// reading `.items` read null where it reads an array, and an answer with
// nothing in it looked like no answer. So the printers that write the answer's
// structure -- json, protojson, prototext, and a template, which reads json's
// document -- say the key with nothing in it. Recognised as [Rows] recognises a
// list, by `items`.
func emptyItems(m protoreflect.Message) protoreflect.FieldDescriptor {
	fd := m.Descriptor().Fields().ByName("items")
	if fd == nil || !fd.IsList() || fd.Kind() != protoreflect.MessageKind {
		return nil
	}
	if m.Get(fd).List().Len() > 0 {
		return nil
	}

	return fd
}

// Name prints the identifier of each row and nothing else.
//
// For the shell: `app robot list -o name | xargs -n1 app robot get`. kubectl's
// `-o name` is the same idea and the same use.
var Name Printer = PrinterFunc(func(w io.Writer, m proto.Message) error {
	for _, row := range Rows(m) {
		fd := row.Descriptor().Fields().ByName("id")
		if fd == nil {
			continue
		}
		if _, err := fmt.Fprintln(w, uuidOf(row.Get(fd).Bytes())); err != nil {
			return err
		}
	}

	return nil
})

// Template renders with `text/template` over the message decoded as Json.
//
// Over the decoded JSON rather than over the protobuf message, so that the
// names in a template are the names in `-o json` -- `{{.alias}}`, not
// `{{.Alias}}` or a `protoreflect` call. A person writing a template has the
// JSON in front of them, and a template that does not agree with it would be a
// second naming to learn.
//
// A text that does not parse is refused with what is wrong with it and where,
// `line 1: unclosed action`, and nothing else: whoever says it to a person
// says which flag and value it is about, as the command line's refusal does --
// `-o template={{: line 1: unclosed action`. text/template's own words start
// `template: :1:`, and wrapped once more they said `template:` twice and named
// no flag.
func Template(text string) (Printer, error) {
	t, err := template.New("").Parse(text)
	if err != nil {
		if rest, ok := strings.CutPrefix(err.Error(), "template: :"); ok {
			return nil, errors.New("line " + rest)
		}
		return nil, err
	}

	return PrinterFunc(func(w io.Writer, m proto.Message) error {
		b, err := jsonOf(m.ProtoReflect(), "")
		if err != nil {
			return err
		}

		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}

		if err := t.Execute(w, v); err != nil {
			return err
		}

		_, err = fmt.Fprintln(w)
		return err
	}), nil
}

// Table is columns, and the format a person reading a list actually wants.
//
// # Where the columns come from
//
// From the message, for now, and this is the half worth explaining because it
// is the half kubectl got right the second time. A CustomResourceDefinition
// that declares no `additionalPrinterColumns` prints NAME and AGE and nothing
// else -- the fallback is available and it is not useful, so everybody declares
// columns or uses `-o json`.
//
// payday can do better without being told anything, because its schema already
// says more than a CRD's does: which field is the identifier, which is the
// alias, which are the timestamps. So the default columns below are not a
// guess at what might be there -- they are fields payday's own field-number
// rule reserves by name. An entity that has them gets a useful table with
// nothing declared.
//
// Columns declared in `(payday.entity).columns` replace the defaults.
// Ordinary RPC responses show their scalar fields; an unambiguous repeated
// message field supplies rows for a response containing a list.
//
// # wide
//
// `wide` is kubectl's `priority`: the same table with the columns that are
// worth having and not worth the width. Splitting them is what keeps the
// default narrow enough to read in a terminal.
func Table(wide bool, opts ...PrintOptions) Printer { return table(wide, true, opts...) }

// table is [Table] told whether to show its header.
//
// Not a field on Table and not state it keeps, because a Printer is a value an
// app may hold and reuse -- one that showed its header the first time it was
// ever used would be a table with no header for every command after the first.
// What knows this is the caller, which is the only thing that knows whether
// this answer is one of several; see [runner.printerAs].
func table(wide, header bool, opts ...PrintOptions) Printer {
	return PrinterFunc(func(w io.Writer, m proto.Message) error {
		rows := tableRows(m)
		if len(rows) == 0 {
			_, err := fmt.Fprintln(w, "(none)")
			return err
		}

		cols, err := columnsOf(rows[0].Descriptor(), wide, opts...)
		if err != nil {
			return err
		}
		if len(cols) == 0 {
			return fmt.Errorf("%s has no scalar table columns; use -o json", rows[0].Descriptor().FullName())
		}

		cells := make([][]string, 0, len(rows)+1)
		if header {
			hs := make([]string, len(cols))
			for i, c := range cols {
				hs[i] = c.head
			}
			cells = append(cells, hs)
		}

		for _, row := range rows {
			vs := make([]string, len(cols))
			for i, c := range cols {
				vs[i] = cellText(c.read(row))
			}
			cells = append(cells, vs)
		}

		return printCells(w, cells)
	})
}

// Rows answers with what a printer should show one line per.
//
// A `Get` answers with the entity itself and a `List` answers with a response
// that holds them, so this looks for the repeated message field a list response
// has -- `items` -- and falls back to the message itself. Both are one code
// path afterwards, which is why `-o name` and `-o table` work on a `get`
// without saying anything about it.
func Rows(m proto.Message) []protoreflect.Message {
	v := m.ProtoReflect()
	if fd := v.Descriptor().Fields().ByName("items"); fd != nil && fd.IsList() && fd.Kind() == protoreflect.MessageKind {
		l := v.Get(fd).List()
		vs := make([]protoreflect.Message, 0, l.Len())
		for i := range l.Len() {
			vs = append(vs, l.Get(i).Message())
		}

		return vs
	}

	return []protoreflect.Message{v}
}

type column struct {
	key  bool
	head string
	read func(protoreflect.Message) string
}

// columnsOf is the default table for a message that declared none.
//
// The order is what a person scans for: what it is called, then what it is,
// then how old. The identifier is not first -- an alias is what somebody
// recognises, and a uuid at the left edge pushes everything readable off the
// screen. It is a column of its own so that `-o wide` still answers "which
// row exactly", and `-o name` answers it without a table at all.
func columnsOf(d protoreflect.MessageDescriptor, wide bool, opts ...PrintOptions) ([]column, error) {
	if vs, err := itemColumns(d, wide, opts...); vs != nil || err != nil {
		return vs, err
	}
	if vs, declared, err := declaredColumns(d, wide, opts...); declared || err != nil {
		return vs, err
	}
	if !entityMessage(d) {
		return scalarColumns(d, opts...), nil
	}

	fs := d.Fields()
	vs := []column{}

	add := func(head string, name protoreflect.Name, f func(protoreflect.Value) string) {
		fd := fs.ByName(name)
		if fd == nil {
			return
		}

		vs = append(vs, column{key: name == "id", head: head, read: func(m protoreflect.Message) string {
			if !m.Has(fd) {
				return "-"
			}

			return f(m.Get(fd))
		}})
	}

	add("ALIAS", "alias", func(v protoreflect.Value) string { return v.String() })
	add("NAME", "name", func(v protoreflect.Value) string { return v.String() })
	if wide {
		add("Id", "id", func(v protoreflect.Value) string { return uuidOf(v.Bytes()) })
		add("TENANT", "tenant_id", func(v protoreflect.Value) string { return uuidOf(v.Bytes()) })
	}
	add("AGE", "date_created", func(v protoreflect.Value) string { return age(v) })

	if len(vs) == 0 {
		// Nothing the field-number rule reserves, which is a legitimate entity
		// -- `Reading` in apptest has no alias and no name. The identifier is
		// the one thing every entity has, so a table of it is still a table.
		add("Id", "id", func(v protoreflect.Value) string { return uuidOf(v.Bytes()) })
	}

	return vs, nil
}

// itemColumns is the table for what a watch sends, which is not a row.
//
// It is a row, what happened to it, and -- for one that is gone -- no row at
// all. So the columns are the row's own, read through `value`, with the RPC
// that changed it in front and the identifier at the end.
//
// **The identifier is not behind `-o wide` here**, which is the one place this
// departs from [columnsOf], and the reason is the absent `value`: a row that
// was erased or that moved out of the filters arrives with nothing but its id,
// every column of it reads `-`, and a line with no id would be a line saying
// only that something happened to something. The `-` itself is not written
// here -- [columnsOf] already answers it for a field that is not set, and an
// absent `value` makes every field of it absent.
//
// Recognised by its fields and not by its name, the way [Rows] recognises a
// list response by `items`: what makes this shape is `id`, `value` and
// `action` together, which is what `pd gen` writes and what a hand-written
// stream of the same thing would have to write to mean the same.
func itemColumns(d protoreflect.MessageDescriptor, wide bool, opts ...PrintOptions) ([]column, error) {
	fs := d.Fields()

	id := fs.ByName("id")
	vd := fs.ByName("value")
	ad := fs.ByName("action")
	if id == nil || vd == nil || ad == nil {
		return nil, nil
	}
	if id.Kind() != protoreflect.BytesKind || id.IsList() {
		return nil, nil
	}
	if vd.Kind() != protoreflect.MessageKind || vd.IsList() {
		return nil, nil
	}
	if ad.Kind() != protoreflect.StringKind || ad.IsList() {
		return nil, nil
	}

	vs := []column{{head: "ACTION", read: func(m protoreflect.Message) string {
		if !m.Has(ad) {
			// The first message, which is not something anybody asked for --
			// it is what was already there.
			return "-"
		}

		return m.Get(ad).String()
	}}}

	cols, err := columnsOf(vd.Message(), wide, opts...)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		if c.key {
			// The item names the row itself, and says so for one that is gone
			// as well. Two Id columns would be the same uuid twice.
			continue
		}

		read := c.read
		vs = append(vs, column{head: c.head, read: func(m protoreflect.Message) string {
			if !m.Has(vd) {
				return "-"
			}

			return read(m.Get(vd).Message())
		}})
	}

	return append(vs, column{head: "Id", read: func(m protoreflect.Message) string {
		if !m.Has(id) {
			return "-"
		}

		return uuidOf(m.Get(id).Bytes())
	}}), nil
}

// uuidOf prints an identifier the way a person writes one back.
//
// Through [pdid] rather than by formatting bytes, so that anything it refuses
// is shown as what it is instead of as a uuid-shaped string that names nothing.
func uuidOf(b []byte) string {
	id, err := pdid.From(b)
	if err != nil {
		return fmt.Sprintf("<%x>", b)
	}

	return id.String()
}

// age is how long ago, which is what a person reads a timestamp for in a table.
//
// kubectl's AGE column, and for its reason: an absolute time in a table is
// eleven columns of which two are being read. The exact time is in `-o json`,
// which is where somebody who needs it is already looking.
func age(v protoreflect.Value) string {
	ts := &timestamppb.Timestamp{}
	b, err := proto.Marshal(v.Message().Interface())
	if err != nil || proto.Unmarshal(b, ts) != nil {
		return "-"
	}

	d := time.Since(ts.AsTime())
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
