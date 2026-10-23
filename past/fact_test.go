package past

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdid"
)

// changed is what Changes said, as the slots and the values they hold --
// "null" for one cleared or gone.
func changed(t *testing.T, vs []Value) map[Slot]string {
	t.Helper()

	out := map[Slot]string{}
	for _, v := range vs {
		s := Slot{Field: v.Field.Number()}
		if v.Key.IsValid() {
			k, err := encodeKey(v.Field.MapKey(), v.Key)
			require.NoError(t, err)
			s.Key = k
		}

		switch {
		case !v.Value.IsValid():
			out[s] = Null
		case v.Key.IsValid():
			e, err := Encode(v.Field.MapValue(), v.Value)
			require.NoError(t, err)
			out[s] = e
		default:
			e, err := Encode(v.Field, v.Value)
			require.NoError(t, err)
			out[s] = e
		}
	}

	return out
}

func set(m protoreflect.Message, name string, v protoreflect.Value) {
	m.Set(field(name), v)
}

func entry(m protoreflect.Message, name, k, v string) {
	m.Mutable(field(name)).Map().Set(protoreflect.ValueOfString(k).MapKey(), protoreflect.ValueOfString(v))
}

// TestChangesAreWhatAWriteChanged: the fields one message holds otherwise
// than another, each set, cleared, or -- of a map -- an entry; and never the
// key, the version or a field that cannot change.
func TestChangesAreWhatAWriteChanged(t *testing.T) {
	other := pdid.New(7)
	home := pdid.New(8)

	before := newThing()
	set(before, "id", protoreflect.ValueOfBytes(pdid.New(9).Bytes()))
	set(before, "home", protoreflect.ValueOfMessage(newOther(home.Bytes())))
	set(before, "name", protoreflect.ValueOfString("arm"))
	set(before, "count", protoreflect.ValueOfInt64(5))
	set(before, "load", protoreflect.ValueOfFloat64(math.Copysign(0, -1)))
	set(before, "ratio", protoreflect.ValueOfFloat32(float32(math.NaN())))
	set(before, "explicit", protoreflect.ValueOfInt32(1))
	set(before, "tags", list(field("tags"), protoreflect.ValueOfString("a")))
	set(before, "date_updated", timestamp(time.Unix(1, 0)))
	set(before, "date_created", timestamp(time.Unix(1, 0)))
	entry(before, "settings", "keep", "1")
	entry(before, "settings", "gone", "2")
	entry(before, "settings", "moved", "3")

	// The edge as a read that loaded the row would hold it: more than its
	// identifier, which is all a write answers with.
	near := newOther(other.Bytes())
	near.Set(otherMd.Fields().ByName("alias"), protoreflect.ValueOfString("loaded"))
	set(before, "near", protoreflect.ValueOfMessage(near))

	after := newThing()
	set(after, "id", protoreflect.ValueOfBytes(pdid.New(9).Bytes()))
	set(after, "home", protoreflect.ValueOfMessage(newOther(pdid.New(8).Bytes())))
	set(after, "name", protoreflect.ValueOfString("arm"))
	set(after, "count", protoreflect.ValueOfInt64(0))
	set(after, "load", protoreflect.ValueOfFloat64(0))
	set(after, "ratio", protoreflect.ValueOfFloat32(float32(math.NaN())))
	set(after, "date_updated", timestamp(time.Unix(2, 0)))
	set(after, "date_created", timestamp(time.Unix(2, 0)))
	set(after, "near", protoreflect.ValueOfMessage(newOther(other.Bytes())))
	set(after, "mode", protoreflect.ValueOfEnum(1))
	entry(after, "settings", "keep", "1")
	entry(after, "settings", "moved", "4")
	entry(after, "settings", "new", "5")

	x := require.New(t)
	x.Equal(map[Slot]string{
		// A count of nothing is a count: its field has no presence, so 0 is
		// what it holds.
		{Field: 8}: `0`,
		// One that has presence and is not set holds nothing, as does an
		// empty list.
		{Field: 28}: Null,
		{Field: 22}: Null,
		{Field: 19}: `1`,
		// A map by entry: one gone, one changed, one new.
		{Field: 23, Key: "gone"}:  Null,
		{Field: 23, Key: "moved"}: `"4"`,
		{Field: 23, Key: "new"}:   `"5"`,
	}, changed(t, Changes(before.Interface(), after.Interface())),
		"not the key, the version, date_created or the immutable edge; "+
			"not -0 for 0, nor NaN for NaN; not an edge to the same row, however much of it was read")

	t.Run("a row made is every field it holds", func(t *testing.T) {
		x := require.New(t)

		x.Equal(map[Slot]string{
			{Field: 5}:                `"arm"`,
			{Field: 11}:               `"NaN"`,
			{Field: 18}:               `"` + other.String() + `"`,
			{Field: 19}:               `1`,
			{Field: 23, Key: "keep"}:  `"1"`,
			{Field: 23, Key: "moved"}: `"4"`,
			{Field: 23, Key: "new"}:   `"5"`,
		}, changed(t, Changes(nil, after.Interface())),
			"a count of 0 and a load of 0 are what a row with neither holds, so they are no change from nothing")
	})

	t.Run("and a row gone, every field cleared", func(t *testing.T) {
		x := require.New(t)

		got := changed(t, Changes(after.Interface(), nil))
		x.Equal(Null, got[Slot{Field: 18}])
		x.Equal(Null, got[Slot{Field: 23, Key: "keep"}])
		x.Equal(`""`, got[Slot{Field: 5}], "a string with no presence is cleared to the empty string")
	})

	t.Run("and two of one message are nothing", func(t *testing.T) {
		require.Empty(t, Changes(after.Interface(), after.Interface()))
	})
}

// TestAPersonalValueIsWithheld: a fact of a field the subject's entity
// declares personal or secret is written as having changed, and never as what
// to -- nor, of a map, which entry, since a key can be a person's as much as
// a value. Cleared, it says so.
func TestAPersonalValueIsWithheld(t *testing.T) {
	d := described()
	row := func(t *testing.T, vs ...Value) []said {
		t.Helper()

		v, err := compile(Fact{Subject: pdid.New(9), Values: vs}, d)
		require.NoError(t, err)
		return v
	}

	t.Run("set and cleared", func(t *testing.T) {
		x := require.New(t)

		x.Equal([]said{
			{Slot{Field: 5}, `"Kim"`},
			{Slot{Field: 24}, Withheld},
			{Slot{Field: 30}, Withheld},
		}, row(t,
			To(field("person"), protoreflect.ValueOfString("Kim")),
			To(field("secret"), protoreflect.ValueOfBytes([]byte("hunter2"))),
			To(field("name"), protoreflect.ValueOfString("Kim")),
		), "the name is not declared about anyone, so it is written; the person and the secret are not")

		x.Equal([]said{{Slot{Field: 24}, Null}}, row(t, Cleared(field("person"))))
		x.Equal([]said{{Slot{Field: 24}, Withheld}}, row(t, To(field("person"), protoreflect.ValueOfString(""))),
			"an empty string is a value too, and withheld")
	})

	t.Run("a map, as one fact of the field", func(t *testing.T) {
		x := require.New(t)
		k := func(v string) protoreflect.MapKey { return protoreflect.ValueOfString(v).MapKey() }

		x.Equal([]said{{Slot{Field: 25}, Withheld}}, row(t,
			Entry(field("contacts"), k("Kim's mother"), protoreflect.ValueOfString("010-0000-0000"))),
			"no key: it is somebody's")

		before, after := newThing(), newThing()
		entry(before, "contacts", "Kim", "1")
		entry(after, "contacts", "Lee", "2")
		entry(after, "contacts", "Park", "3")
		x.Equal([]said{{Slot{Field: 25}, Withheld}}, row(t, Changes(before.Interface(), after.Interface())...),
			"three entries changed, and one fact of the field says so")

		x.Equal([]said{{Slot{Field: 25}, Null}}, row(t, Changes(after.Interface(), newThing().Interface())...),
			"and the map left empty is the field cleared")

		settings := newThing()
		entry(settings, "settings", "Kim", "1")
		x.Equal([]said{{Slot{Field: 23, Key: "Kim"}, Null}}, row(t, Changes(settings.Interface(), newThing().Interface())...),
			"a map nothing declares about anyone is a fact for each entry")
	})

	t.Run("and what is no fact is refused", func(t *testing.T) {
		for name, vs := range map[string][]Value{
			"a map set whole":            {To(field("settings"), protoreflect.ValueOfMap(newThing().NewField(field("settings")).Map()))},
			"a key of a field no map":    {Entry(field("name"), protoreflect.ValueOfString("k").MapKey(), protoreflect.ValueOfString("v"))},
			"a field of another message": {To(otherMd.Fields().ByName("alias"), protoreflect.ValueOfString("v"))},
			"one field twice":            {To(field("name"), protoreflect.ValueOfString("a")), To(field("name"), protoreflect.ValueOfString("b"))},
			"one withheld field twice":   {To(field("person"), protoreflect.ValueOfString("a")), To(field("person"), protoreflect.ValueOfString("b"))},
			"no field":                   {{}},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := compile(Fact{Subject: pdid.New(9), Values: vs}, d)
				require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
			})
		}
	})
}

// TestAStateIsWhatWasKnown is the example rove's design gives (§3.3), of a
// fact: where a robot was, recorded on 10/1, moved on 10/5, and the move
// corrected on 10/9. What it was at a moment is the fact of it that holds from latest by
// then, of the ones standing now -- or of the ones that stood at another
// moment, by what had been written down by then: what the system said.
func TestAStateIsWhatWasKnown(t *testing.T) {
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	at := func(v time.Time) *time.Time { return &v }
	cell := Slot{Field: 3}

	// 10/1: from 9/1 in A. 10/5: to B on 9/15. 10/9: the move was on 9/12,
	// so #2 is superseded and #3 written in its place.
	f1 := FactRow{Id: pdid.New(18), Field: 3, Value: `"A"`, From: day(9, 1), Recorded: day(10, 1)}
	f2 := FactRow{Id: pdid.New(18), Field: 3, Value: `"B"`, From: day(9, 15), Recorded: day(10, 5), Superseded: at(day(10, 9))}
	f3 := FactRow{Id: pdid.New(18), Field: 3, Value: `"B"`, From: day(9, 12), Recorded: day(10, 9)}
	rows := []FactRow{f3, f1, f2}

	for _, c := range []struct {
		name  string
		at    time.Time
		known *time.Time
		want  *FactRow
	}{
		{"9/13, as known now", day(9, 13), nil, &f3},
		{"9/13, as known on 10/6", day(9, 13), at(day(10, 6)), &f1},
		{"9/20, as known on 10/2", day(9, 20), at(day(10, 2)), &f1},
		{"9/20, as known on 10/6", day(9, 20), at(day(10, 6)), &f2},
		{"9/20, as known now", day(9, 20), nil, &f3},
		{"9/13, as known the moment it was corrected", day(9, 13), at(day(10, 9)), &f3},
		{"9/20, as known the moment it was corrected: #2 had stopped being", day(9, 20), at(day(10, 9)), &f3},
		{"9/13, as known an instant before", day(9, 13), at(day(10, 9).Add(-time.Microsecond)), &f1},
		{"9/11, as known now", day(9, 11), nil, &f1},
		{"8/31, before anything was", day(8, 31), nil, nil},
		{"9/20, as known on 9/30, before anything was written", day(9, 20), at(day(9, 30)), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)

			k := Known{At: c.known}
			got, ok := fold(rows, c.at, k)[cell]
			if c.want == nil {
				x.False(ok, "%v", got)
				return
			}
			x.True(ok)
			x.Equal(c.want.Id, got.Id)
		})
	}

	t.Run("a fact written later of an earlier moment holds from that moment", func(t *testing.T) {
		x := require.New(t)

		// 10/12: it was in C from 9/5 -- recorded after the others, and
		// holding from before two of them.
		f4 := FactRow{Id: pdid.New(18), Field: 3, Value: `"C"`, From: day(9, 5), Recorded: day(10, 12)}
		rows := append(rows, f4)

		x.Equal(f4.Id, fold(rows, day(9, 6), Known{})[cell].Id)
		x.Equal(f3.Id, fold(rows, day(9, 13), Known{})[cell].Id, "what holds from latest, not what was written last")
		x.Equal(f1.Id, fold(rows, day(9, 6), Known{At: at(day(10, 11))})[cell].Id, "and as known before it was written, it was not")
	})

	t.Run("a field cleared holds nothing from then", func(t *testing.T) {
		x := require.New(t)

		f5 := FactRow{Id: pdid.New(18), Field: 3, Value: Null, From: day(9, 25), Recorded: day(10, 10)}
		rows := append(rows, f5)

		_, ok := fold(rows, day(9, 26), Known{})[cell]
		x.False(ok)
		x.Equal(f3.Id, fold(rows, day(9, 24), Known{})[cell].Id)
	})

	t.Run("and two from one moment are the one written last", func(t *testing.T) {
		x := require.New(t)

		// Both standing, which a write through Set never leaves -- it
		// supersedes the first -- and a read still answers one.
		g := FactRow{Id: pdid.New(18), Field: 3, Value: `"D"`, From: day(9, 12), Recorded: day(10, 10)}
		x.Equal(g.Id, fold([]FactRow{g, f3}, day(9, 13), Known{})[cell].Id)
	})
}

// TestFactsAreReadBackAsTheStatementsTheyWere: rows read back are regrouped by
// the event that wrote them, their subject and their moment -- which every row
// carries, so nothing else says what a statement was -- each in the order of
// its fields, and the statements in the order they hold from.
func TestFactsAreReadBackAsTheStatementsTheyWere(t *testing.T) {
	x := require.New(t)
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }

	ev, other := pdid.New(17), pdid.New(17)
	a, b := pdid.New(19), pdid.New(19)
	row := func(event, subject pdid.Id, from int, field protoreflect.FieldNumber) FactRow {
		return FactRow{Id: pdid.New(18), Event: event, Subject: subject, From: day(from), Field: field, Recorded: day(30)}
	}

	got := regroup([]FactRow{
		row(ev, a, 5, 9), row(ev, a, 5, 8),
		row(ev, a, 1, 8),
		row(ev, b, 5, 8),
		row(other, a, 5, 10),
	})

	type said struct {
		event, subject pdid.Id
		from           int
		fields         []protoreflect.FieldNumber
	}
	var vs []said
	for _, st := range got {
		v := said{st.Event, st.Subject, st.From.Day(), nil}
		for _, r := range st.Rows {
			v.fields = append(v.fields, r.Field)
		}
		vs = append(vs, v)
	}

	x.ElementsMatch([]said{
		{ev, a, 1, []protoreflect.FieldNumber{8}},
		{ev, a, 5, []protoreflect.FieldNumber{8, 9}},
		{ev, b, 5, []protoreflect.FieldNumber{8}},
		{other, a, 5, []protoreflect.FieldNumber{10}},
	}, vs, "one event's facts of one subject from two moments are two statements, and of two subjects two")
	x.Equal(1, vs[0].from, "the one that holds from first, first")
}
