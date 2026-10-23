package past

import (
	"math"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

var anId = uuid.MustParse("0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a")

// spec is a Thing's Spec holding `set`.
func spec(set func(m protoreflect.Message)) protoreflect.Value {
	m := dynamicpb.NewMessage(specMd)
	set(m)
	return protoreflect.ValueOfMessage(m)
}

func timestamp(at time.Time) protoreflect.Value {
	m := newThing().NewField(field("at")).Message()
	m.Set(m.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfInt64(at.Unix()))
	m.Set(m.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfInt32(int32(at.Nanosecond())))
	return protoreflect.ValueOfMessage(m)
}

func list(fd protoreflect.FieldDescriptor, vs ...protoreflect.Value) protoreflect.Value {
	l := newThing().NewField(fd).List()
	for _, v := range vs {
		l.Append(v)
	}
	return protoreflect.ValueOfList(l)
}

// TestAValueIsWrittenOneWay: the canonical JSON of each kind of value a fact
// holds, as RFC 8785 writes strings, numbers and objects, with integers
// exact, enums by number, identifiers as their strings, a moment as RFC 3339
// and a message keyed by field number -- and each read back to the value it
// was, and written again to the same text.
func TestAValueIsWrittenOneWay(t *testing.T) {
	str := protoreflect.ValueOfString
	f64 := protoreflect.ValueOfFloat64

	for _, c := range []struct {
		name  string
		field string
		v     protoreflect.Value
		want  string
	}{
		// Strings: a quote, a backslash and the control characters escaped,
		// the five with a short form by it and the rest as \u00xx in lower
		// case; everything else as itself -- U+2028, which JavaScript's own
		// JSON once escaped, and a character past U+FFFF included.
		{"a word", "name", str("arm"), `"arm"`},
		{"the empty string, which is not withheld", "name", str(""), `""`},
		{"escapes", "name", str("q\"b\\n\nt\tf\fr\rb\bc\x01\x1f"), `"q\"b\\n\nt\tf\fr\rb\bc\u0001\u001f"`},
		{"unicode as itself", "name", str("é  \U0001F600<>&\x7f"), "\"é  \U0001F600<>&\x7f\""},

		// Integers exact, past what a double holds.
		{"2^53 + 1", "count", protoreflect.ValueOfInt64(1<<53 + 1), `9007199254740993`},
		{"the least int64", "count", protoreflect.ValueOfInt64(math.MinInt64), `-9223372036854775808`},
		{"the most uint64", "big", protoreflect.ValueOfUint64(math.MaxUint64), `18446744073709551615`},
		{"an int32", "small", protoreflect.ValueOfInt32(-7), `-7`},
		{"a sint64", "low", protoreflect.ValueOfInt64(-1 << 60), `-1152921504606846976`},

		// Numbers as ECMAScript writes them.
		{"-0", "load", f64(math.Copysign(0, -1)), `0`},
		{"0", "load", f64(0), `0`},
		{"NaN", "load", f64(math.NaN()), `"NaN"`},
		{"+Inf", "load", f64(math.Inf(1)), `"Infinity"`},
		{"-Inf", "load", f64(math.Inf(-1)), `"-Infinity"`},
		{"a fraction", "load", f64(123.456), `123.456`},
		{"one tenth", "load", f64(0.1), `0.1`},
		{"negative", "load", f64(-1.5), `-1.5`},
		{"1e20, the last without an exponent", "load", f64(1e20), `100000000000000000000`},
		{"1e21, the first with one", "load", f64(1e21), `1e+21`},
		{"1e-6, the last without one", "load", f64(0.000001), `0.000001`},
		{"1e-7, the first with one", "load", f64(1e-7), `1e-7`},
		{"the least", "load", f64(5e-324), `5e-324`},
		{"the most", "load", f64(math.MaxFloat64), `1.7976931348623157e+308`},
		{"2^53 + 1, as a double", "load", f64(1<<53 + 1), `9007199254740992`},
		{"a float, by its own digits", "ratio", protoreflect.ValueOfFloat32(0.1), `0.1`},

		{"a bool", "on", protoreflect.ValueOfBool(true), `true`},
		{"bytes, as standard base64", "blob", protoreflect.ValueOfBytes([]byte{1, 2, 3, 0xff}), `"AQID/w=="`},
		{"an identifier", "maker_id", protoreflect.ValueOfBytes(anId[:]), `"0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a"`},
		{"an edge, by the row it names", "near", protoreflect.ValueOfMessage(newOther(anId[:])), `"0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a"`},
		{"an enum, by its number", "mode", protoreflect.ValueOfEnum(2), `2`},

		// RFC 3339 in UTC, with 0, 3, 6 or 9 digits of a second.
		{"a moment", "at", timestamp(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)), `"2026-10-01T09:00:00Z"`},
		{"in another zone", "at", timestamp(time.Date(2026, 10, 1, 18, 0, 0, 0, time.FixedZone("KST", 9*3600))), `"2026-10-01T09:00:00Z"`},
		{"a millisecond", "at", timestamp(time.Date(2026, 10, 1, 9, 0, 1, 500_000_000, time.UTC)), `"2026-10-01T09:00:01.500Z"`},
		{"a microsecond", "at", timestamp(time.Date(2026, 10, 1, 9, 0, 0, 1000, time.UTC)), `"2026-10-01T09:00:00.000001Z"`},
		{"a nanosecond", "at", timestamp(time.Date(2026, 10, 1, 9, 0, 0, 1, time.UTC)), `"2026-10-01T09:00:00.000000001Z"`},

		// A message by field number, the keys in the order of their code
		// units -- "10" before "2" -- and what it holds by the same rules.
		{"a message", "spec", spec(func(m protoreflect.Message) {
			fs := m.Descriptor().Fields()
			m.Set(fs.ByName("tenth"), protoreflect.ValueOfUint32(10))
			m.Set(fs.ByName("reach"), protoreflect.ValueOfFloat64(1.5))
			m.Set(fs.ByName("axes"), protoreflect.ValueOfUint32(3))
		}), `{"1":3,"10":10,"2":1.5}`},
		{"an empty one", "spec", spec(func(protoreflect.Message) {}), `{}`},
		{"one holding a list, a map and a moment", "spec", spec(func(m protoreflect.Message) {
			fs := m.Descriptor().Fields()
			notes := m.Mutable(fs.ByName("notes")).List()
			notes.Append(str("b"))
			notes.Append(str("a"))

			// Keyed by UTF-16 code units: U+1F600 is D83D DE00, before
			// U+FF61, where its UTF-8 is after.
			parts := m.Mutable(fs.ByName("parts")).Map()
			parts.Set(protoreflect.ValueOfString("｡").MapKey(), protoreflect.ValueOfInt64(1))
			parts.Set(protoreflect.ValueOfString("\U0001F600").MapKey(), protoreflect.ValueOfInt64(2))
			parts.Set(protoreflect.ValueOfString("a").MapKey(), protoreflect.ValueOfInt64(1<<62))

			due := m.Mutable(fs.ByName("due")).Message()
			due.Set(due.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfInt64(0))
		}), "{\"3\":[\"b\",\"a\"],\"4\":{\"a\":4611686018427387904,\"\U0001F600\":2,\"｡\":1},\"5\":\"1970-01-01T00:00:00Z\"}"},

		{"a list, as one value", "tags", list(field("tags"), str("b"), str("a")), `["b","a"]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)
			fd := field(c.field)

			got, err := Encode(fd, c.v)
			x.NoError(err)
			x.Equal(c.want, got)

			v, err := Decode(fd, got)
			x.NoError(err)
			again, err := Encode(fd, v)
			x.NoError(err)
			x.Equal(c.want, again, "read back and written again")
		})
	}

	t.Run("a map's entry, by its value's descriptor", func(t *testing.T) {
		x := require.New(t)
		fd := field("slots")

		got, err := Encode(fd.MapValue(), spec(func(m protoreflect.Message) {
			m.Set(m.Descriptor().Fields().ByName("axes"), protoreflect.ValueOfUint32(1))
		}))
		x.NoError(err)
		x.Equal(`{"1":1}`, got)

		k, err := encodeKey(fd.MapKey(), protoreflect.ValueOfInt32(-7).MapKey())
		x.NoError(err)
		x.Equal("-7", k)

		_, err = Encode(fd, protoreflect.ValueOfMap(newThing().NewField(fd).Map()))
		x.ErrorContains(err, "a fact for each entry", "a map is no one value")
	})

	t.Run("what has no JSON is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := Encode(field("name"), str("\xff"))
		x.Error(err, "a string that is not UTF-8")
		_, err = Encode(field("maker_id"), protoreflect.ValueOfBytes([]byte{1, 2, 3}))
		x.ErrorContains(err, "16 bytes")
		_, err = Encode(field("at"), timestamp(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)))
		x.ErrorContains(err, "RFC 3339")
	})

	t.Run("null is no value, and withheld is none to read", func(t *testing.T) {
		x := require.New(t)

		v, err := Decode(field("name"), Null)
		x.NoError(err)
		x.False(v.IsValid())

		_, err = Decode(field("name"), Withheld)
		x.ErrorIs(err, ErrWithheld)
	})

	t.Run("a number past what a double holds is read exactly", func(t *testing.T) {
		x := require.New(t)

		v, err := Decode(field("count"), `9007199254740993`)
		x.NoError(err)
		x.Equal(int64(1<<53+1), v.Int())

		v, err = Decode(field("big"), `18446744073709551615`)
		x.NoError(err)
		x.Equal(uint64(math.MaxUint64), v.Uint())

		_, err = Decode(field("count"), `1.5`)
		x.Error(err, "an integer is an integer")
	})
}
