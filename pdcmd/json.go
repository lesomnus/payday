package pdcmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdid"
)

// JSON is protojson with the identifiers written the way a person writes them.
//
// # Why there are two JSONs
//
// protojson encodes `bytes` as base64, and every payday identifier is a `bytes`
// field, so [ProtoJSON] answers:
//
//	"id":  "AaABD/0ejxumAkRCTSq6vQ=="
//
// which is exactly right and nearly useless. It is the identifier, and it is not
// the identifier anybody can paste into the next command or match against a log
// line. A `jq` over a page of those is a page of values that have to be decoded
// before they mean anything.
//
// Rewriting them in place would have been the easy answer and the wrong one:
// what `-o protojson` produces has to be what protojson produces, or a script
// that feeds it back somewhere stops being able to. So this is a second format
// rather than a change to the first, and which one somebody gets is which one
// they asked for.
//
//	-o protojson   "id":  "AaABD/0ejxumAkRCTSq6vQ=="
//	-o json        "id": "01a0010f-fd1e-8f1b-a602-44424d2ababd"
//
// # What it changes and what it does not
//
// Only fields the schema marks `type: TYPE_UUID`. A `bytes` field payday did
// not put there stays base64, because base64 is what it is -- see [isId].
// Everything else is protojson's: the timestamps, the enum names, the
// number-as-string rules for 64-bit fields.
//
// Fields come out in the order the schema declares them rather than
// alphabetically, which is why this walks the descriptor rather than handing a
// decoded map back to the encoder. `id` first and the timestamps last is the
// order the message is written in and the order somebody reads it in.
var JSON Printer = PrinterFunc(func(w io.Writer, m proto.Message) error {
	b, err := jsonOf(m.ProtoReflect(), "")
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
})

// jsonOf renders one message, in schema order, with uuids for identifiers.
//
// It marshals with protojson first and re-emits, rather than encoding from
// scratch. protojson holds every rule about how a protobuf becomes JSON --
// 64-bit integers are strings, a `Timestamp` is RFC 3339, an enum is its name,
// a `FieldMask` is a comma-joined path list -- and an encoder written here would
// be a second, worse copy of all of it that drifts on the next well-known type
// somebody uses.
func jsonOf(m protoreflect.Message, indent string) ([]byte, error) {
	raw, err := protojson.Marshal(m.Interface())
	if err != nil {
		return nil, err
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}

	b := &bytes.Buffer{}
	if err := emit(b, decoded, m.Descriptor(), indent); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}

// emit writes `v` as a JSON object, taking its key order from `d`.
func emit(b *bytes.Buffer, v map[string]any, d protoreflect.MessageDescriptor, indent string) error {
	inner := indent + "  "

	fs := d.Fields()
	first := true

	b.WriteString("{")
	for i := range fs.Len() {
		fd := fs.Get(i)

		// protojson writes the JSON name; the proto name is accepted as well,
		// so both are looked for. A field that is not in the map was not set.
		x, ok := v[fd.JSONName()]
		if !ok {
			x, ok = v[string(fd.Name())]
			if !ok {
				continue
			}
		}

		if !first {
			b.WriteString(",")
		}
		first = false

		fmt.Fprintf(b, "\n%s%q: ", inner, fd.JSONName())
		if err := emitValue(b, x, fd, inner); err != nil {
			return err
		}
	}

	if first {
		b.WriteString("}")
		return nil
	}

	fmt.Fprintf(b, "\n%s}", indent)
	return nil
}

func emitValue(b *bytes.Buffer, x any, fd protoreflect.FieldDescriptor, indent string) error {
	switch v := x.(type) {
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return nil
		}

		inner := indent + "  "
		b.WriteString("[")
		for i, e := range v {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, "\n%s", inner)
			if err := emitOne(b, e, fd, inner); err != nil {
				return err
			}
		}
		fmt.Fprintf(b, "\n%s]", indent)

		return nil

	default:
		return emitOne(b, x, fd, indent)
	}
}

func emitOne(b *bytes.Buffer, x any, fd protoreflect.FieldDescriptor, indent string) error {
	// A nested message, and only when it really came out as an object: a
	// well-known type is a message to the descriptor and a string to protojson,
	// and recursing into one with its descriptor would look for fields in a
	// string.
	if m, ok := x.(map[string]any); ok && fd.Kind() == protoreflect.MessageKind {
		return emit(b, m, fd.Message(), indent)
	}

	// The one substitution this format exists for.
	if s, ok := x.(string); ok && fd.Kind() == protoreflect.BytesKind && isId(fd) {
		if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
			if id, err := pdid.From(raw); err == nil {
				fmt.Fprintf(b, "%q", id.String())
				return nil
			}
		}
	}

	out, err := json.Marshal(x)
	if err != nil {
		return err
	}

	b.Write(out)
	return nil
}

// unmarshalJSON reads a request, accepting an identifier written either way.
//
// # Why this is not a second parser
//
// protojson reads `bytes` as base64, so the uuid a person just read out of
// `-o json` -- or off a screen, or out of a log -- cannot be typed back into a
// request. That is the round trip broken in the middle: this app prints an
// identifier one way and refuses to be told it that way.
//
// So the JSON is walked before protojson sees it, and a string standing where
// the schema declared `type: TYPE_UUID` is re-encoded as base64. Only there:
// nothing else in the document is touched, and a `bytes` field payday did not
// put there still means base64.
//
// # Why protojson cannot be left to work it out
//
// It does not refuse a uuid, which was the surprise. protojson accepts URL-safe
// base64 as well as standard, and `-` is in that alphabet, so
// `01a0010f-fd1e-8f1b-a602-44424d2ababd` decodes -- to 27 bytes of nothing. What
// comes back is `id: invalid UUID (got 27 bytes)` from the server, which is a
// true statement about a value nobody wrote.
//
// So the substitution has to happen before protojson sees the document, which
// is what this does.
//
// # It does not guess
//
// The test is [pdid.Parse], so a string is converted only when it is exactly a
// uuid. Base64 of sixteen bytes is 24 characters ending in `==` and is not one,
// so nothing a caller meant as base64 is reinterpreted -- the ambiguity runs
// one way only, and that way is the one protojson gets wrong.
//
// That is why the lenient reading is the default. `--in protojson` turns it off
// for a caller that wants what protojson accepts and nothing more; it is a
// stricter contract rather than a different one.
func unmarshalJSON(b []byte, m proto.Message, strict bool) error {
	if strict {
		return protojson.Unmarshal(b, m)
	}

	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}

	v = toWire(v, m.ProtoReflect().Descriptor())

	out, err := json.Marshal(v)
	if err != nil {
		return err
	}

	return protojson.Unmarshal(out, m)
}

// toWire rewrites the uuid strings in a decoded document into base64.
func toWire(x any, d protoreflect.MessageDescriptor) any {
	obj, ok := x.(map[string]any)
	if !ok || d == nil {
		return x
	}

	fs := d.Fields()
	for k, v := range obj {
		fd := fs.ByJSONName(k)
		if fd == nil {
			fd = fs.ByName(protoreflect.Name(k))
		}
		if fd == nil {
			// Not a field of this message. Left alone so that protojson is the
			// one to refuse it, with the name and the position it knows.
			continue
		}

		switch vs := v.(type) {
		case []any:
			for i, e := range vs {
				vs[i] = oneToWire(e, fd)
			}

		default:
			obj[k] = oneToWire(v, fd)
		}
	}

	return obj
}

func oneToWire(x any, fd protoreflect.FieldDescriptor) any {
	if fd.Kind() == protoreflect.MessageKind {
		return toWire(x, fd.Message())
	}

	s, ok := x.(string)
	if !ok || fd.Kind() != protoreflect.BytesKind || !isId(fd) {
		return x
	}

	id, err := pdid.Parse(s)
	if err != nil {
		// Not a uuid, so it is whatever it already was -- base64, most likely,
		// and protojson's to read.
		return x
	}

	return base64.StdEncoding.EncodeToString(id.Bytes())
}
