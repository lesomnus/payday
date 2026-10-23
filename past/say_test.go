package past

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
)

// The sentences of the past: said when an event is read, in the reader's
// language and zone, naming people as they are called then (payday#151).

var (
	happenedMd = message("Happened")
	namedMd    = message("Named")
	plainMd    = message("Plain")
	servicedMd = message("Serviced")
	partMd     = servicedMd.Messages().ByName("Part")
)

// sayOf says the messages of `md` in `locale` with `f`, as [Say] says the
// messages of a Go type: these are dynamic, and [Say] reads its message type
// off its type parameter.
func sayOf(s *Sentences, locale string, md protoreflect.MessageDescriptor, f func(*Reading, EventRow, protoreflect.Message) string) {
	s.say(locale, dynamicpb.NewMessageType(md), func(r *Reading, ev EventRow, m proto.Message) string {
		return f(r, ev, m.ProtoReflect())
	})
}

// says is a sentence that is the locale it was said in, and what with.
func says(text string) func(*Reading, EventRow, protoreflect.Message) string {
	return func(r *Reading, _ EventRow, _ protoreflect.Message) string { return r.Locale() + ": " + text }
}

// eventOf is a row of an event holding `m`, of the kind `kind`.
func eventOf(t *testing.T, kind string, m protoreflect.Message) EventRow {
	t.Helper()

	v, err := anypb.New(m.Interface())
	require.NoError(t, err)

	return EventRow{Id: pdid.New(17), Kind: kind, Value: v}
}

func happened() protoreflect.Message { return dynamicpb.NewMessage(happenedMd) }

// TestASentenceIsSaidInTheReadersLanguage: in the reader's locale, then its
// language, then the default locale and its language -- and a sentence that
// says nothing is none, said past as the next one in the line.
func TestASentenceIsSaidInTheReadersLanguage(t *testing.T) {
	s := NewSentences("ko-KR")
	sayOf(s, "en", happenedMd, says("happened"))
	sayOf(s, "en-GB", happenedMd, says("happened, innit"))
	sayOf(s, "ko", happenedMd, says("일어남"))
	sayOf(s, "pt", happenedMd, func(*Reading, EventRow, protoreflect.Message) string { return "" })

	ev := eventOf(t, "thing.happen", happened())
	for locale, want := range map[string]string{
		"en-GB": "en-GB: happened, innit",
		"en_gb": "en-GB: happened, innit",
		"en-AU": "en: happened",
		"EN":    "en: happened",
		"ko":    "ko: 일어남",
		"fr":    "ko: 일어남",
		"":      "ko: 일어남",
		"pt":    "ko: 일어남",
	} {
		t.Run(locale, func(t *testing.T) {
			require.Equal(t, want, s.Read(t.Context(), nil, locale, nil).Say(ev))
		})
	}
}

// TestAnEventNothingSaysIsItsKindAndWhatItHolds: an event no locale a reader
// is said to says is its kind, and its message field by field, in the order
// of their numbers -- each identifier as itself, named by nothing, and a
// moment in the reader's zone -- so that no event is ever said as nothing.
func TestAnEventNothingSaysIsItsKindAndWhatItHolds(t *testing.T) {
	thing := pdid.New(7)
	by := pdid.New(8)

	m := dynamicpb.NewMessage(servicedMd)
	f := servicedMd.Fields().ByName
	part := func(serial string, n uint32) protoreflect.Value {
		p := dynamicpb.NewMessage(partMd)
		p.Set(partMd.Fields().ByName("serial"), protoreflect.ValueOfString(serial))
		p.Set(partMd.Fields().ByName("count"), protoreflect.ValueOfUint32(n))
		return protoreflect.ValueOfMessage(p)
	}
	m.Set(f("thing"), protoreflect.ValueOfBytes(thing.Bytes()))
	m.Set(f("by"), protoreflect.ValueOfBytes(by.Bytes()))
	m.Set(f("mode"), protoreflect.ValueOfEnum(2))
	m.Mutable(f("parts")).List().Append(part("P-1", 2))
	m.Mutable(f("parts")).List().Append(part("P-2", 1))
	m.Mutable(f("swapped")).Map().Set(protoreflect.ValueOfString("right").MapKey(), part("P-9", 1))
	m.Mutable(f("swapped")).Map().Set(protoreflect.ValueOfString("left").MapKey(), part("P-0", 1))
	m.Set(f("due"), protoreflect.ValueOfMessage(timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)).ProtoReflect()))
	m.Set(f("note"), protoreflect.ValueOfString(`a "quoted" note`))
	m.Set(f("blob"), protoreflect.ValueOfBytes([]byte{1, 2, 3}))
	m.Set(f("took"), protoreflect.ValueOfMessage(durationpb.New(90*time.Minute).ProtoReflect()))
	m.Set(f("load"), protoreflect.ValueOfFloat64(0.5))
	m.Set(f("done"), protoreflect.ValueOfBool(true))
	m.Set(f("count"), protoreflect.ValueOfInt64(1<<53+1))
	m.Set(f("ratio"), protoreflect.ValueOfFloat32(0.1))
	// And `left`, which is a count of nothing: a field that holds its zero
	// is no part of what happened.

	// Said in German alone: what reads the message for a reader of neither
	// the default nor German, and nothing that says it to them.
	s := NewSentences("en")
	sayOf(s, "de", servicedMd, says("gewartet"))

	seoul, err := time.LoadLocation("Asia/Seoul")
	require.NoError(t, err)
	ev := eventOf(t, "thing.service", m)

	x := require.New(t)
	x.Equal("thing.service {"+
		"thing: "+thing.String()+", "+
		"by: "+by.String()+", "+
		"mode: MODE_RUN, "+
		`parts: [{serial: "P-1", count: 2}, {serial: "P-2", count: 1}], `+
		`swapped: {"left": {serial: "P-0", count: 1}, "right": {serial: "P-9", count: 1}}, `+
		"due: 2026-11-01 09:00, "+
		`note: "a \"quoted\" note", `+
		"blob: AQID, "+
		"took: 1h30m0s, "+
		"load: 0.5, "+
		"done: true, "+
		"count: 9007199254740993, "+
		"ratio: 0.1"+
		"}", s.Read(t.Context(), nil, "fr", seoul).Say(ev))
	x.Equal("de: gewartet", s.Read(t.Context(), nil, "de", seoul).Say(ev))

	t.Run("and an event of a message nothing reads is its kind", func(t *testing.T) {
		x := require.New(t)

		// `past.test.Named` is linked by nothing and said by nothing.
		v := dynamicpb.NewMessage(namedMd)
		v.Set(namedMd.Fields().ByName("thing"), protoreflect.ValueOfBytes(thing.Bytes()))
		x.Equal("past.test.Named", s.Read(t.Context(), nil, "en", nil).Say(eventOf(t, "past.test.Named", v)))

		// Nor is a message of another type than its kind is said with.
		x.Equal("thing.service", s.Read(t.Context(), nil, "de", nil).Say(eventOf(t, "thing.service", v)))
	})

	t.Run("and one that holds nothing is its kind", func(t *testing.T) {
		x := require.New(t)

		x.Equal("thing.service", s.Read(t.Context(), nil, "en", nil).Say(eventOf(t, "thing.service", dynamicpb.NewMessage(servicedMd))))
		x.Equal("thing.service", s.Read(t.Context(), nil, "en", nil).Say(EventRow{Id: pdid.New(17), Kind: "thing.service"}))

		id := pdid.New(17)
		x.Equal(id.String(), s.Read(t.Context(), nil, "en", nil).Say(EventRow{Id: id}), "a row of no kind, by what it is")
	})
}

// TestAMomentIsReadInTheReadersZone: a moment in the reading's zone, written
// as the locale the sentence is said in writes one -- or its language, the
// default, or the day and the minute.
func TestAMomentIsReadInTheReadersZone(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	require.NoError(t, err)
	york, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	s := NewSentences("en")
	s.TimeLayout("en", "Jan 2 15:04")
	s.TimeLayout("ko", "1월 2일 15:04")
	sayOf(s, "en", happenedMd, func(r *Reading, _ EventRow, _ protoreflect.Message) string {
		return "on " + r.Time(at)
	})

	x := require.New(t)
	x.Equal("11월 1일 09:00", s.Read(t.Context(), nil, "ko-KR", seoul).Time(at))
	x.Equal("Oct 31 20:00", s.Read(t.Context(), nil, "en", york).Time(at))
	x.Equal("Nov 1 00:00", s.Read(t.Context(), nil, "fr", nil).Time(at), "as the default writes one, in UTC")
	x.Equal("on Nov 1 09:00", s.Read(t.Context(), nil, "ko", seoul).Say(eventOf(t, "thing.happen", happened())),
		"an English sentence said to a reader of Korean writes a moment as English does, in the reader's zone")
	x.Equal("2026-11-01 09:00", NewSentences("en").Read(t.Context(), nil, "en", seoul).Time(at), "as no locale says how")
	x.Equal(seoul, s.Read(t.Context(), nil, "en", seoul).Zone())
}

// names is a [Names] that says what it is told, and counts what it is asked.
type names struct {
	of    map[pdid.Id]named
	err   map[pdid.Id]error
	asked map[pdid.Id]int
}

func (n *names) Name(_ context.Context, id pdid.Id) (string, []access.Subject, error) {
	if n.asked == nil {
		n.asked = map[pdid.Id]int{}
	}
	n.asked[id]++
	if err := n.err[id]; err != nil {
		return "", nil, err
	}

	v := n.of[id]
	return v.name, v.saw, nil
}

// TestANameIsAskedOnceAndRecordedWhereItIsShown: a reading asks a name once,
// however often a sentence says it, and every time it is said puts whose data
// it is on the access record of the call -- the row it was read off, which
// need not be the one asked for. An identifier nothing names is said as
// itself, no identifier as nothing, and a name that could not be read is the
// reading's error.
func TestANameIsAskedOnceAndRecordedWhereItIsShown(t *testing.T) {
	holder, party, tenant := pdid.New(3), pdid.New(9), pdid.New(1)
	robot, gone, broken, other := pdid.New(7), pdid.New(7), pdid.New(3), pdid.New(3)
	failed := errors.New("the database is gone")
	n := &names{
		of: map[pdid.Id]named{
			// A membership, named by the person record it is of.
			holder: {name: "Alice", saw: []access.Subject{{Id: party, Tenant: tenant}}},
			// A thing, which is nobody's data.
			robot: {name: "arm-01"},
		},
		err: map[pdid.Id]error{broken: failed},
	}

	ctx, call := access.Into(t.Context(), "/test.Past/Say")
	r := NewSentences("en").Read(ctx, n, "en", nil)

	x := require.New(t)
	x.Equal("Alice", r.Name(holder))
	x.Equal("Alice", r.Name(holder))
	x.Equal("arm-01", r.Name(robot))
	x.Equal(gone.String(), r.Name(gone), "nothing names it")
	x.Equal("", r.Name(pdid.Nil), "no identifier, which a sentence says in its own words")
	x.Equal(map[pdid.Id]int{holder: 1, robot: 1, gone: 1}, n.asked, "each asked once, and no identifier not at all")

	_, saw := call.Unsaid()
	x.Equal([]access.Subject{{Id: party, Tenant: tenant}}, saw, "the person record the name was read off, and nobody's a robot is")
	x.NoError(r.Err())

	x.Equal(broken.String(), r.Name(broken))
	x.Equal(other.String(), r.Name(other))
	x.ErrorIs(r.Err(), failed)
	x.Equal(1, n.asked[other], "one that could not be read is no reason not to ask the next")

	t.Run("and another reading asks again", func(t *testing.T) {
		n.of[holder] = named{name: "Alicia", saw: n.of[holder].saw}

		r := NewSentences("en").Read(ctx, n, "en", nil)
		require.Equal(t, "Alicia", r.Name(holder))
		require.NoError(t, r.Err())
	})

	t.Run("and one with nothing to name by names nobody", func(t *testing.T) {
		ctx, call := access.Into(t.Context(), "/test.Past/Say")

		r := NewSentences("en").Read(ctx, nil, "en", nil)
		require.Equal(t, holder.String(), r.Name(holder))

		_, saw := call.Unsaid()
		require.Empty(t, saw)
	})
}

// TestUnsaidIsWhatALanguageDoesNotSay: the kinds a locale's own language says
// nothing of -- in it or in its language, and not in the default, which is a
// language a reader of it did not ask for.
func TestUnsaidIsWhatALanguageDoesNotSay(t *testing.T) {
	kinds := map[string]protoreflect.MessageType{
		"thing.happen":    dynamicpb.NewMessageType(happenedMd),
		"past.test.Named": dynamicpb.NewMessageType(namedMd),
		"thing.service":   dynamicpb.NewMessageType(servicedMd),
	}

	s := NewSentences("en")
	sayOf(s, "en", happenedMd, says("happened"))
	sayOf(s, "en-GB", namedMd, says("named"))
	sayOf(s, "ko", servicedMd, says("정비"))

	x := require.New(t)
	x.Equal([]string{"past.test.Named", "thing.service"}, Unsaid(s, "en", kinds))
	x.Equal([]string{"past.test.Named", "thing.service"}, Unsaid(s, "", kinds), "no locale is the default")
	x.Equal([]string{"thing.service"}, Unsaid(s, "en_GB", kinds))
	x.Equal([]string{"past.test.Named", "thing.happen"}, Unsaid(s, "ko-KR", kinds), "said in English, which is no Korean")
	x.Equal([]string{"past.test.Named", "thing.happen", "thing.service"}, Unsaid(s, "fr", kinds))

	sayOf(s, "ko", happenedMd, says("일어남"))
	sayOf(s, "ko", namedMd, says("이름"))
	x.Empty(Unsaid(s, "ko", kinds))

	x.Equal([]string{"thing.happen"}, Unsaid(s, "ko", map[string]protoreflect.MessageType{
		"thing.happen": dynamicpb.NewMessageType(namedMd),
	}), "said with a message an event of the kind does not hold")
}

// TestAnEventOfNoMessageIsSaidByLegacy: a row from before the app's events
// were the past's holds no message, and is said by what the app says of such
// rows -- in the reader's locale, since it is in the words it was written in
// -- or as its kind.
func TestAnEventOfNoMessageIsSaidByLegacy(t *testing.T) {
	ev := EventRow{Id: pdid.New(17), Kind: "robot.oil"}

	s := NewSentences("en")
	sayOf(s, "en", happenedMd, says("happened"))
	x := require.New(t)
	x.Equal("robot.oil", s.Read(t.Context(), nil, "en", nil).Say(ev))

	s.Legacy(func(r *Reading, ev EventRow) string { return r.Locale() + ": " + ev.Kind + ", as it was written" })
	x.Equal("ko-KR: robot.oil, as it was written", s.Read(t.Context(), nil, "ko-KR", nil).Say(ev))
	x.Equal("en: happened", s.Read(t.Context(), nil, "en", nil).Say(eventOf(t, "thing.happen", happened())),
		"and an event that holds one is said as its kind is")

	s.Legacy(func(r *Reading, ev EventRow) string {
		r.Fail(errors.New("its row is gone"))
		return ""
	})
	r := s.Read(t.Context(), nil, "en", nil)
	x.Equal("robot.oil", r.Say(ev), "one it says nothing of")
	x.EqualError(r.Err(), "its row is gone")
}

// TestWhatIsSaidIsSaidOnce: a message that is no event's, a kind said twice
// in one locale, and a sentence of no language are the code saying what it
// does not mean, and are refused where it starts.
func TestWhatIsSaidIsSaidOnce(t *testing.T) {
	x := require.New(t)

	s := NewSentences("en")
	sayOf(s, "en", happenedMd, says("happened"))
	x.Panics(func() { sayOf(s, "EN", happenedMd, says("again")) })
	x.Panics(func() { sayOf(s, "en", plainMd, says("plain")) })
	x.Panics(func() { sayOf(s, " ", namedMd, says("named")) })
	x.Panics(func() { NewSentences("") })
	x.Panics(func() { s.TimeLayout("", "15:04") })
	x.Panics(func() { Say(s, "en", func(*Reading, EventRow, proto.Message) string { return "" }) }, "a message of no type")

	x.NotPanics(func() { sayOf(s, "en-GB", happenedMd, says("happened")) })
}
