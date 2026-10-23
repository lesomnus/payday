package past

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"uuid"

	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Encode is the canonical JSON of `v`, a value of the field `fd`: what a fact
// of the field keeps, the same text for the same value whoever writes it and
// whenever.
//
// One way to write each value, so that two facts of one value are equal as
// text -- a read across resources is `value = '"repair"'` in SQL, and a fact
// that changed nothing is one whose text is the text before:
//
//   - a string, an object and a floating-point number as RFC 8785 writes them:
//     the fewest escapes, an object's keys in the order of their UTF-16 code
//     units, a number as ECMAScript writes it -- `0` for -0 as well. NaN and
//     the infinities, which JSON has no number for, are the strings protobuf's
//     JSON writes, `"NaN"`, `"Infinity"` and `"-Infinity"`;
//   - an integer as its exact decimal, a 64-bit one too: past 2^53 a double
//     cannot hold it, and the column is text;
//   - an enum as its number, which a value renamed keeps;
//   - bytes as standard base64, and a uuid -- a column `TYPE_UUID`, and an
//     edge, by the identifier of the row it names -- as its canonical string;
//   - a `google.protobuf.Timestamp` as RFC 3339 in UTC, with 0, 3, 6 or 9
//     digits of a second, as protobuf's JSON writes one;
//   - any other message as an object keyed by field number, which a field
//     renamed keeps, of the fields it holds;
//   - a list as one array.
//
// A map is not one value: a fact is about one entry of it, so a map field is
// refused here, and an entry's value is encoded with `fd.MapValue()`.
func Encode(fd protoreflect.FieldDescriptor, v protoreflect.Value) (string, error) {
	if fd.IsMap() {
		return "", fmt.Errorf("%s: a map is a fact for each entry, so it is not one value; encode an entry's with its MapValue", fd.FullName())
	}
	if !v.IsValid() {
		return "", fmt.Errorf("%s: no value", fd.FullName())
	}

	b, err := appendField(nil, fd, v)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

// Decode is the value [Encode] wrote for the field `fd`, as a value a
// message of `fd` holds -- of its own Go type where one is linked, and a
// dynamic message otherwise. [Null] is no value, and [Withheld] is
// [ErrWithheld]: the value was never written down.
//
// It reads any JSON of the value, the canonical one among them; an object
// keyed by a number the message does not have is read without it, as a row of
// an older schema is.
func Decode(fd protoreflect.FieldDescriptor, s string) (protoreflect.Value, error) {
	switch s {
	case Withheld:
		return protoreflect.Value{}, ErrWithheld
	case Null:
		return protoreflect.Value{}, nil
	}
	if fd.IsMap() {
		return protoreflect.Value{}, fmt.Errorf("%s: a map is a fact for each entry; decode an entry's with its MapValue", fd.FullName())
	}

	raw, err := parse(s)
	if err != nil {
		return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
	}
	if fd.IsList() {
		l := newMessage(fd.ContainingMessage()).NewField(fd).List()
		if err := decodeList(fd, raw, l); err != nil {
			return protoreflect.Value{}, err
		}

		return protoreflect.ValueOfList(l), nil
	}

	return decodeSingular(fd, raw, func() protoreflect.Message { return newMessage(fd.Message()) })
}

// appendField is the JSON of `v` as a value of `fd`, which may be a list or,
// inside a message, a map.
func appendField(b []byte, fd protoreflect.FieldDescriptor, v protoreflect.Value) ([]byte, error) {
	switch {
	case fd.IsList():
		l := v.List()
		b = append(b, '[')
		for i := range l.Len() {
			if i > 0 {
				b = append(b, ',')
			}

			var err error
			if b, err = appendSingular(b, fd, l.Get(i)); err != nil {
				return nil, err
			}
		}

		return append(b, ']'), nil

	case fd.IsMap():
		type entry struct {
			key string
			v   protoreflect.Value
		}
		var es []entry
		var err error
		v.Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			var s string
			if s, err = encodeKey(fd.MapKey(), k); err != nil {
				return false
			}
			es = append(es, entry{s, v})
			return true
		})
		if err != nil {
			return nil, err
		}
		slices.SortFunc(es, func(a, b entry) int { return utf16Compare(a.key, b.key) })

		b = append(b, '{')
		for i, e := range es {
			if i > 0 {
				b = append(b, ',')
			}
			if b, err = appendString(b, e.key); err != nil {
				return nil, err
			}
			b = append(b, ':')
			if b, err = appendSingular(b, fd.MapValue(), e.v); err != nil {
				return nil, err
			}
		}

		return append(b, '}'), nil
	}

	return appendSingular(b, fd, v)
}

// appendSingular is the JSON of one value of `fd`: of the field itself, or of
// one element of a list or one value of a map.
func appendSingular(b []byte, fd protoreflect.FieldDescriptor, v protoreflect.Value) ([]byte, error) {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return strconv.AppendBool(b, v.Bool()), nil

	case protoreflect.EnumKind:
		return strconv.AppendInt(b, int64(v.Enum()), 10), nil

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.AppendInt(b, v.Int(), 10), nil

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.AppendUint(b, v.Uint(), 10), nil

	case protoreflect.FloatKind:
		return appendFloat(b, v.Float(), 32), nil

	case protoreflect.DoubleKind:
		return appendFloat(b, v.Float(), 64), nil

	case protoreflect.StringKind:
		b, err := appendString(b, v.String())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", fd.FullName(), err)
		}

		return b, nil

	case protoreflect.BytesKind:
		if isUuid(fd) {
			return appendUuid(b, fd, v.Bytes())
		}

		b = append(b, '"')
		b = base64.StdEncoding.AppendEncode(b, v.Bytes())
		return append(b, '"'), nil

	case protoreflect.MessageKind, protoreflect.GroupKind:
		m := v.Message()
		switch {
		case isEdge(fd):
			k := keyOf(m.Descriptor())
			if k == nil || k.Kind() != protoreflect.BytesKind {
				return nil, fmt.Errorf("%s: an edge to %s, which has no identifier to name it by", fd.FullName(), m.Descriptor().FullName())
			}

			return appendUuid(b, fd, m.Get(k).Bytes())

		case m.Descriptor().FullName() == "google.protobuf.Timestamp":
			return appendTimestamp(b, fd, m)
		}

		return appendMessage(b, m)
	}

	return nil, fmt.Errorf("%s: a %s, which a fact has no JSON for", fd.FullName(), fd.Kind())
}

// appendMessage is a message as an object keyed by field number, of the
// fields it holds. By number and not by name, because that is what a row of
// an older schema is read back by: a field renamed is the same field.
func appendMessage(b []byte, m protoreflect.Message) ([]byte, error) {
	type field struct {
		key string
		fd  protoreflect.FieldDescriptor
		v   protoreflect.Value
	}
	var fs []field
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		fs = append(fs, field{strconv.Itoa(int(fd.Number())), fd, v})
		return true
	})
	slices.SortFunc(fs, func(a, b field) int { return utf16Compare(a.key, b.key) })

	b = append(b, '{')
	for i, f := range fs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '"')
		b = append(b, f.key...)
		b = append(b, '"', ':')

		var err error
		if b, err = appendField(b, f.fd, f.v); err != nil {
			return nil, err
		}
	}

	return append(b, '}'), nil
}

// appendString is a string as RFC 8785 writes one: a quote, a backslash and
// the control characters escaped -- the five with a short form by it, the rest
// as `\u00xx` in lower case -- and everything else as itself.
func appendString(b []byte, s string) ([]byte, error) {
	if !utf8.ValidString(s) {
		return nil, errors.New("not UTF-8, and a fact's JSON is text")
	}

	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		// Byte by byte: every byte of a character past ASCII is at least 0x80,
		// so it is written as it is, and only these are ever escaped.
		switch c := s[i]; {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c >= 0x20:
			b = append(b, c)
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c == '\t':
			b = append(b, '\\', 't')
		default:
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
		}
	}

	return append(b, '"'), nil
}

// appendFloat is a floating-point number as ECMAScript's Number::toString
// writes it, which is what RFC 8785 says a number is: the fewest digits that
// read back as it, without an exponent from 1e-6 up to 1e21 and with one
// beyond. A float is the fewest that read back as the float, not as the
// double it widens to.
func appendFloat(b []byte, f float64, bits int) []byte {
	switch {
	case math.IsNaN(f):
		return append(b, `"NaN"`...)
	case math.IsInf(f, 1):
		return append(b, `"Infinity"`...)
	case math.IsInf(f, -1):
		return append(b, `"-Infinity"`...)
	case f == 0:
		// -0 as well, as ECMAScript writes it: no JSON number tells the two
		// apart, and a fact is one value for both.
		return append(b, '0')
	}

	if f < 0 {
		b = append(b, '-')
		f = -f
	}

	// The digits, as d.ddd with the power of ten the first is at.
	s := strconv.FormatFloat(f, 'e', -1, bits)
	mant, exp, _ := strings.Cut(s, "e")
	digits := strings.Replace(mant, ".", "", 1)
	e, _ := strconv.Atoi(exp)
	n, k := e+1, len(digits)

	switch {
	case k <= n && n <= 21:
		b = append(b, digits...)
		for range n - k {
			b = append(b, '0')
		}
	case 0 < n && n <= 21:
		b = append(b, digits[:n]...)
		b = append(b, '.')
		b = append(b, digits[n:]...)
	case -6 < n && n <= 0:
		b = append(b, '0', '.')
		for range -n {
			b = append(b, '0')
		}
		b = append(b, digits...)
	default:
		b = append(b, digits[0])
		if k > 1 {
			b = append(b, '.')
			b = append(b, digits[1:]...)
		}
		b = append(b, 'e')
		if n-1 < 0 {
			b = append(b, '-')
			b = strconv.AppendInt(b, int64(1-n), 10)
		} else {
			b = append(b, '+')
			b = strconv.AppendInt(b, int64(n-1), 10)
		}
	}

	return b
}

// appendUuid is an identifier as its canonical string, lower case and
// hyphenated.
func appendUuid(b []byte, fd protoreflect.FieldDescriptor, v []byte) ([]byte, error) {
	if len(v) != len(uuid.UUID{}) {
		return nil, fmt.Errorf("%s: a uuid is 16 bytes, and this is %d", fd.FullName(), len(v))
	}

	b = append(b, '"')
	b, _ = uuid.UUID(v).AppendText(b)
	return append(b, '"'), nil
}

// The moments a Timestamp may say, as protobuf's JSON bounds them: the years
// RFC 3339 has four digits for.
const (
	minSeconds = -62135596800 // 0001-01-01T00:00:00Z
	maxSeconds = 253402300799 // 9999-12-31T23:59:59Z
)

func appendTimestamp(b []byte, fd protoreflect.FieldDescriptor, m protoreflect.Message) ([]byte, error) {
	fs := m.Descriptor().Fields()
	secs, nanos := m.Get(fs.ByNumber(1)).Int(), m.Get(fs.ByNumber(2)).Int()
	if secs < minSeconds || secs > maxSeconds || nanos < 0 || nanos >= 1e9 {
		return nil, fmt.Errorf("%s: %d seconds and %d nanoseconds is no moment RFC 3339 can say", fd.FullName(), secs, nanos)
	}

	x := time.Unix(secs, nanos).UTC().Format("2006-01-02T15:04:05.000000000")
	x = strings.TrimSuffix(x, "000")
	x = strings.TrimSuffix(x, "000")
	x = strings.TrimSuffix(x, ".000")

	b = append(b, '"')
	b = append(b, x...)
	return append(b, 'Z', '"'), nil
}

// encodeKey is a key of a map as a fact's `key` and as an object's: a
// string as it is, a number as its decimal, and a bool as `true` or `false`.
func encodeKey(fd protoreflect.FieldDescriptor, k protoreflect.MapKey) (string, error) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		if !utf8.ValidString(k.String()) {
			return "", fmt.Errorf("%s: a key that is not UTF-8", fd.FullName())
		}
		return k.String(), nil
	case protoreflect.BoolKind:
		return strconv.FormatBool(k.Bool()), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(k.Int(), 10), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(k.Uint(), 10), nil
	}

	return "", fmt.Errorf("%s: a map keyed by %s", fd.FullName(), fd.Kind())
}

// decodeKey is the key [encodeKey] wrote.
func decodeKey(fd protoreflect.FieldDescriptor, s string) (protoreflect.MapKey, error) {
	v, err := scalar(fd, s, true)
	if err != nil {
		return protoreflect.MapKey{}, err
	}

	return v.MapKey(), nil
}

// utf16Compare orders two strings by their UTF-16 code units, which is the
// order RFC 8785 puts an object's keys in. It is not the order of their bytes
// for a character past U+FFFF beside one from U+E000 up.
func utf16Compare(a, b string) int {
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
}

// parse reads one JSON value, keeping every number as the text it is: an
// integer past 2^53 is exact, and a float is read once, by its own width.
func parse(s string) (any, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()

	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("more than one value")
	}

	return v, nil
}

// decodeSingular reads one value of `fd`, a message by `mk`.
func decodeSingular(fd protoreflect.FieldDescriptor, raw any, mk func() protoreflect.Message) (protoreflect.Value, error) {
	if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
		return scalar(fd, raw, false)
	}

	m := mk()
	md := m.Descriptor()
	switch {
	case isEdge(fd):
		s, ok := raw.(string)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("%s: an edge is the identifier of the row it names, and this is %T", fd.FullName(), raw)
		}
		k := keyOf(md)
		if k == nil {
			return protoreflect.Value{}, fmt.Errorf("%s: an edge to %s, which has no identifier", fd.FullName(), md.FullName())
		}
		u, err := uuid.Parse(s)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
		}
		m.Set(k, protoreflect.ValueOfBytes(u[:]))

	case md.FullName() == "google.protobuf.Timestamp":
		s, ok := raw.(string)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("%s: a moment is RFC 3339, and this is %T", fd.FullName(), raw)
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
		}
		if secs := t.Unix(); secs < minSeconds || secs > maxSeconds {
			return protoreflect.Value{}, fmt.Errorf("%s: %s is no moment a Timestamp holds", fd.FullName(), s)
		}
		fs := md.Fields()
		m.Set(fs.ByNumber(1), protoreflect.ValueOfInt64(t.Unix()))
		m.Set(fs.ByNumber(2), protoreflect.ValueOfInt32(int32(t.Nanosecond())))

	default:
		if err := decodeMessage(m, raw); err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
		}
	}

	return protoreflect.ValueOfMessage(m), nil
}

// decodeMessage reads an object keyed by field number into `m`.
func decodeMessage(m protoreflect.Message, raw any) error {
	obj, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("a message is an object, and this is %T", raw)
	}

	fs := m.Descriptor().Fields()
	for k, v := range obj {
		n, err := strconv.ParseInt(k, 10, 32)
		if err != nil {
			return fmt.Errorf("a message is keyed by field number, and %q is not one", k)
		}
		fd := fs.ByNumber(protoreflect.FieldNumber(n))
		if fd == nil {
			// A field this build does not have: a row of an older schema
			// reads without it.
			continue
		}
		if err := decodeInto(m, fd, v); err != nil {
			return err
		}
	}

	return nil
}

// decodeInto sets the field `fd` of `m` to the value `raw` is, through `m`'s
// own types: a message, a list and a map of it are made by it, so what is set
// is what it holds.
func decodeInto(m protoreflect.Message, fd protoreflect.FieldDescriptor, raw any) error {
	switch {
	case fd.IsList():
		l := m.NewField(fd).List()
		if err := decodeList(fd, raw, l); err != nil {
			return err
		}
		m.Set(fd, protoreflect.ValueOfList(l))

	case fd.IsMap():
		obj, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: a map is an object, and this is %T", fd.FullName(), raw)
		}
		mp := m.NewField(fd).Map()
		for k, v := range obj {
			key, err := decodeKey(fd.MapKey(), k)
			if err != nil {
				return err
			}
			e, err := decodeSingular(fd.MapValue(), v, func() protoreflect.Message { return mp.NewValue().Message() })
			if err != nil {
				return err
			}
			mp.Set(key, e)
		}
		m.Set(fd, protoreflect.ValueOfMap(mp))

	default:
		v, err := decodeSingular(fd, raw, func() protoreflect.Message { return m.NewField(fd).Message() })
		if err != nil {
			return err
		}
		m.Set(fd, v)
	}

	return nil
}

func decodeList(fd protoreflect.FieldDescriptor, raw any, l protoreflect.List) error {
	vs, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%s: a list is an array, and this is %T", fd.FullName(), raw)
	}
	for _, v := range vs {
		e, err := decodeSingular(fd, v, func() protoreflect.Message { return l.NewElement().Message() })
		if err != nil {
			return err
		}
		l.Append(e)
	}

	return nil
}

// scalar reads a value of a kind that is not a message: from JSON, or from a
// map's key, which is a string whatever its kind -- `key` says which.
func scalar(fd protoreflect.FieldDescriptor, raw any, key bool) (protoreflect.Value, error) {
	text := func() (string, error) {
		switch v := raw.(type) {
		case json.Number:
			if !key {
				return string(v), nil
			}
		case string:
			if key {
				return v, nil
			}
		}

		return "", fmt.Errorf("%s: a %s, and this is %T", fd.FullName(), fd.Kind(), raw)
	}

	switch fd.Kind() {
	case protoreflect.BoolKind:
		if key {
			s, _ := raw.(string)
			v, err := strconv.ParseBool(s)
			if err != nil {
				return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
			}
			return protoreflect.ValueOfBool(v), nil
		}
		v, ok := raw.(bool)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("%s: a bool, and this is %T", fd.FullName(), raw)
		}
		return protoreflect.ValueOfBool(v), nil

	case protoreflect.EnumKind:
		s, err := text()
		if err != nil {
			return protoreflect.Value{}, err
		}
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: an enum is its number: %w", fd.FullName(), err)
		}
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(n)), nil

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		s, err := text()
		if err != nil {
			return protoreflect.Value{}, err
		}
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
		}
		return protoreflect.ValueOfInt32(int32(n)), nil

	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		s, err := text()
		if err != nil {
			return protoreflect.Value{}, err
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
		}
		return protoreflect.ValueOfInt64(n), nil

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		s, err := text()
		if err != nil {
			return protoreflect.Value{}, err
		}
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil

	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		s, err := text()
		if err != nil {
			return protoreflect.Value{}, err
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
		}
		return protoreflect.ValueOfUint64(n), nil

	case protoreflect.FloatKind, protoreflect.DoubleKind:
		bits := 64
		if fd.Kind() == protoreflect.FloatKind {
			bits = 32
		}

		var f float64
		switch v := raw.(type) {
		case string:
			switch v {
			case "NaN":
				f = math.NaN()
			case "Infinity":
				f = math.Inf(1)
			case "-Infinity":
				f = math.Inf(-1)
			default:
				return protoreflect.Value{}, fmt.Errorf("%s: %q is no number", fd.FullName(), v)
			}
		case json.Number:
			var err error
			if f, err = strconv.ParseFloat(string(v), bits); err != nil {
				return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
			}
		default:
			return protoreflect.Value{}, fmt.Errorf("%s: a number, and this is %T", fd.FullName(), raw)
		}
		if bits == 32 {
			return protoreflect.ValueOfFloat32(float32(f)), nil
		}
		return protoreflect.ValueOfFloat64(f), nil

	case protoreflect.StringKind:
		s, ok := raw.(string)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("%s: a string, and this is %T", fd.FullName(), raw)
		}
		return protoreflect.ValueOfString(s), nil

	case protoreflect.BytesKind:
		s, ok := raw.(string)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("%s: bytes are a string, and this is %T", fd.FullName(), raw)
		}
		if isUuid(fd) {
			u, err := uuid.Parse(s)
			if err != nil {
				return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
			}
			return protoreflect.ValueOfBytes(u[:]), nil
		}
		v, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("%s: %w", fd.FullName(), err)
		}
		return protoreflect.ValueOfBytes(v), nil
	}

	return protoreflect.Value{}, fmt.Errorf("%s: a %s, which a fact has no JSON for", fd.FullName(), fd.Kind())
}

// newMessage is a message of `md`: of its Go type when one is linked, and a
// dynamic one when none is.
func newMessage(md protoreflect.MessageDescriptor) protoreflect.Message {
	if t, err := protoregistry.GlobalTypes.FindMessageByName(md.FullName()); err == nil {
		return t.New()
	}

	return dynamicpb.NewMessage(md)
}

// ormField is the `(orm.field)` of `fd`, and nil for a field that says none.
func ormField(fd protoreflect.FieldDescriptor) *ormpb.FieldOptions {
	v, _ := proto.GetExtension(fd.Options(), ormpb.E_Field).(*ormpb.FieldOptions)
	return v
}

// isUuid says `fd` holds an identifier: bytes the schema typed `TYPE_UUID`.
func isUuid(fd protoreflect.FieldDescriptor) bool {
	return fd.Kind() == protoreflect.BytesKind && ormField(fd).GetType() == ormpb.Type_TYPE_UUID
}

// isEdge says `fd` names another row: a message field the schema declares an
// edge, which a fact holds the identifier of.
func isEdge(fd protoreflect.FieldDescriptor) bool {
	return fd.Kind() == protoreflect.MessageKind && proto.HasExtension(fd.Options(), ormpb.E_Edge)
}

// keyOf is the field a row of `md` is keyed by: the one its schema declares
// the key, and the first field for a message that declares none -- which is
// where payday's key always is.
func keyOf(md protoreflect.MessageDescriptor) protoreflect.FieldDescriptor {
	fs := md.Fields()
	for i := range fs.Len() {
		if ormField(fs.Get(i)).GetKey() {
			return fs.Get(i)
		}
	}

	return fs.ByNumber(1)
}
