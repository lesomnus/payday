package past

import (
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
)

// Names is what names the people and things a sentence names by identifier,
// as they are called when it is read: `pd.PastNames`, which `pd gen` writes
// into an app that declares a past, or one of the app's own.
type Names interface {
	// Name is what the row `id` is called now, and the people whose personal
	// data saying it shows: the row the name was read off -- which need not
	// be `id`'s own, a membership named by the person record it is of -- and
	// the tenant that row is in. None for a name that is nobody's data, a
	// robot's or a tenant's.
	//
	// Empty, and no error, for an identifier nothing names: no row, or one
	// the caller may not see.
	Name(ctx context.Context, id pdid.Id) (string, []access.Subject, error)
}

// Sentences is what an app says of its events: for each kind, a sentence in
// each language it is read in, made from the event when it is read.
//
// An event keeps no sentence. What happened is the message it holds, and a
// sentence written down at the time would be in one language and one zone,
// and would go on naming somebody by the name they had then. Said as it is
// read, it is in the reader's language and zone, it names people as they are
// called now -- the pseudonym of somebody forgotten, the new name of somebody
// renamed -- and each name it says is on the access record of the call that
// read it.
//
// Everything is said where the app starts, before anything reads; from then
// on it is read from any goroutine at once, and nothing more is said.
type Sentences struct {
	// def is the locale said in to a reader of a language nothing is said in.
	def string

	// by is each sentence by the locale it is said in, as a [tag], and the
	// kind it says.
	by map[string]map[string]sayer

	legacy  func(*Reading, EventRow) string
	layouts map[string]string
}

// sayer is one sentence: the locale it was said in as [Say] was given it, the
// message its kind holds, and what says it.
type sayer struct {
	locale string
	typ    protoreflect.MessageType
	say    func(*Reading, EventRow, proto.Message) string
}

// NewSentences is an app's sentences, none of them said yet, which say an
// event in `locale` to a reader of a language they say nothing in: the
// language the app says every kind in.
//
// It panics for no locale, which would leave a reader of any other with no
// language at all.
func NewSentences(locale string) *Sentences {
	if tag(locale) == "" {
		panic("past: sentences are said in some language to a reader of one they say nothing in, and this names none")
	}

	return &Sentences{def: locale, by: map[string]map[string]sayer{}, layouts: map[string]string{}}
}

// Say says each event of the kind the message `M` declares -- a generated
// type, `*app.RobotMoved` -- in `locale`, with `f`: given the reading, the
// event and the message it holds, the sentence.
//
// The message holds what happened as it was then. A thing it holds by value
// -- a tag, a serial, what it was called -- is said as it is held: that is a
// record, and nobody's data. A person it holds by identifier and nothing
// else, and a sentence names them with [Reading.Name], as they are called
// now, which puts them on the access record of the call that reads it.
//
// A sentence that says nothing -- empty -- is none, and the event is said as
// the next locale says it ([Reading.Say]). It panics for a message that
// declares no `(payday.event)` and for a kind already said in `locale`: either
// is the code saying something it does not mean, where it starts.
func Say[M proto.Message](s *Sentences, locale string, f func(*Reading, EventRow, M) string) {
	var m M
	if any(m) == nil {
		panic("past: a sentence is said of a message type, *app.RobotMoved, and this is an interface")
	}
	s.say(locale, m.ProtoReflect().Type(), func(r *Reading, ev EventRow, v proto.Message) string {
		return f(r, ev, v.(M))
	})
}

// say is [Say] of the message type `typ`, which need not be a Go type of its
// own.
func (s *Sentences) say(locale string, typ protoreflect.MessageType, f func(*Reading, EventRow, proto.Message) string) {
	md := typ.Descriptor()
	kind, ok := declared(md)
	if !ok {
		panic(fmt.Sprintf("past: %s says no (payday.event), so no event holds one to be said", md.FullName()))
	}

	l := tag(locale)
	if l == "" {
		panic(fmt.Sprintf("past: %s is said in some language, and this names none", kind))
	}
	if _, ok := s.by[l][kind]; ok {
		panic(fmt.Sprintf("past: %s is said in %s twice", kind, locale))
	}
	if s.by[l] == nil {
		s.by[l] = map[string]sayer{}
	}
	s.by[l][kind] = sayer{locale: locale, typ: typ, say: f}
}

// Legacy says each event that holds no message, with `f`: given the reading
// and the event, the sentence. Such a row was written before the app's events
// were the past's -- the app adopted its own table as it stood, and the rows
// already in it keep what they said in the app's own fields, which an overlay
// adds: the sentence each was written with, and what it named.
//
// The past writes none of those fields and reads none of them, so `f` reads
// them itself, of the row `ev.Id` names, with [Reading.Context], and says what
// went wrong reading them with [Reading.Fail]. It is said in no language of
// its own, being in the words it was written in; [Reading.Locale] is the
// reader's.
//
// Without it, such an event is said as one nothing says: its kind.
func (s *Sentences) Legacy(f func(*Reading, EventRow) string) { s.legacy = f }

// TimeLayout says how a moment is written in `locale`, as a layout of package
// time: `Jan 2 15:04`, `1월 2일 15:04`. A locale that says nothing of it
// writes one as its language does, then as the default locale does, and then
// as `2006-01-02 15:04`. It panics for no locale, as [Say] does.
func (s *Sentences) TimeLayout(locale, layout string) {
	l := tag(locale)
	if l == "" {
		panic("past: a moment is written as some language writes one, and this names none")
	}
	s.layouts[l] = layout
}

// layout0 is how a moment is written in a locale nothing says how of: the day
// and the minute, in the order that reads the same in every language.
const layout0 = "2006-01-02 15:04"

// Read is a reading of events for the call `ctx` is: said in `locale` as far
// as anything is said in it ([Reading.Say]), each moment in `zone`
// ([Reading.Time]), and the people in them named by `names`
// ([Reading.Name]).
//
// A reading asks each name once, so it is for one answer. The next answer is
// a reading of its own, which asks again and finds anybody renamed since. It
// is used from one goroutine, as the call is.
//
// No locale is the default; a nil `zone` is UTC; and nil `names` names
// nobody, every identifier being said as itself.
func (s *Sentences) Read(ctx context.Context, names Names, locale string, zone *time.Location) *Reading {
	if tag(locale) == "" {
		locale = s.def
	}
	if zone == nil {
		zone = time.UTC
	}

	return &Reading{
		s:        s,
		ctx:      ctx,
		names:    names,
		locale:   locale,
		speaking: locale,
		zone:     zone,
		named:    map[pdid.Id]named{},
	}
}

// Reading is events being read and said: what [Sentences.Read] answers, and
// what a sentence is made with.
type Reading struct {
	s     *Sentences
	ctx   context.Context
	names Names

	// locale is the reader's. speaking is the one a sentence is being said
	// in -- the locale its [Say] was given, while it says it, and the
	// reader's otherwise -- which is what says how a moment in it is written:
	// an English sentence said to a reader of French writes its dates as
	// English does.
	locale   string
	speaking string

	zone *time.Location

	// named is each name asked, and the people whose data it is.
	named map[pdid.Id]named

	err error
}

type named struct {
	name string
	saw  []access.Subject
}

// Say is the sentence of `ev`, and never empty: as the reader's locale says
// it, then its language -- `en` for `en-GB` -- then the default locale, and
// that one's language. Where none of them says it, it is the event's kind
// and what its message holds, field by field, each identifier as itself:
// `machine.service {machine: 0199…, by: 0199…}`.
//
// An event that holds no message is said by [Sentences.Legacy]. One whose
// message this binary cannot read -- of a type it does not link -- is said as
// its kind.
func (r *Reading) Say(ev EventRow) string {
	if ev.Value == nil {
		if f := r.s.legacy; f != nil {
			if v := r.in(r.locale, func() string { return f(r, ev) }); v != "" {
				return v
			}
		}

		return r.generic(ev, nil)
	}

	for _, l := range r.s.chain(r.locale) {
		sd, ok := r.s.by[l][ev.Kind]
		if !ok {
			continue
		}
		m, ok := sd.unpack(ev.Value)
		if !ok {
			continue
		}
		if v := r.in(sd.locale, func() string { return sd.say(r, ev, m) }); v != "" {
			return v
		}
	}

	return r.generic(ev, r.s.decode(ev))
}

// in is `f` said in `locale`.
func (r *Reading) in(locale string, f func() string) string {
	was := r.speaking
	r.speaking = locale
	defer func() { r.speaking = was }()

	return f()
}

// Name is what the row `id` is called now, as the reading's [Names] says: the
// identifier itself where nothing names it -- no row, or one the caller may
// not see -- or the name could not be read ([Reading.Err]); and empty for no
// identifier, which is the actor of the deployment's own work, for a sentence
// to say in its own words.
//
// Each person whose data the name is goes on the access record of the call
// the reading is for (`access.Saw`): a name put in a sentence is shown, and no
// declaration of the answer it goes out in can see it there. So a sentence
// names people with this, and says a thing its message holds by value as the
// message holds it.
func (r *Reading) Name(id pdid.Id) string {
	if id.IsZero() {
		return ""
	}

	n, ok := r.named[id]
	if !ok {
		n = r.lookup(id)
		r.named[id] = n
	}
	access.Saw(r.ctx, n.saw...)
	if n.name == "" {
		return id.String()
	}

	return n.name
}

// lookup is what the reading's [Names] says `id` is called. One it could not
// read is named by nothing, and the reading remembers why.
func (r *Reading) lookup(id pdid.Id) named {
	if r.names == nil {
		return named{}
	}

	name, saw, err := r.names.Name(r.ctx, id)
	if err != nil {
		r.Fail(err)
		return named{}
	}

	return named{name: name, saw: saw}
}

// Time is `t` as the reader reads it: in the reading's zone, and written as
// the locale the sentence is said in writes a moment
// ([Sentences.TimeLayout]).
func (r *Reading) Time(t time.Time) string {
	return t.In(r.zone).Format(r.s.layout(r.speaking))
}

// Locale is the locale being said in: in a sentence, the one its [Say] was
// given -- `en` to a reader of `en-GB`, or the default -- and the reader's
// otherwise.
func (r *Reading) Locale() string { return r.speaking }

// Zone is the zone the reader reads a moment in.
func (r *Reading) Zone() *time.Location { return r.zone }

// Context is the call the reading is for: what a sentence that reads more of
// the app's own -- a legacy row's fields -- reads it with.
func (r *Reading) Context() context.Context { return r.ctx }

// Fail says something a sentence says could not be read, and [Reading.Err]
// answers the first. The sentence says what it can without it.
func (r *Reading) Fail(err error) {
	if err != nil && r.err == nil {
		r.err = err
	}
}

// Err is the first thing the reading could not read -- a name, or what a
// sentence said it could not ([Reading.Fail]) -- and nil when it read
// everything. A name it could not read is said as its identifier, so an
// answer made of sentences asks this once they are said, and answers the
// error rather than sentences that name nobody.
func (r *Reading) Err() error { return r.err }

// Unsaid is the kinds of `kinds` -- `pd.EventKinds` -- that nothing says in
// `locale`'s own language, in order: a sentence in the locale or in its
// language says one, and the default locale's does not, being another
// language than the one a reader of this asked for. It is for an app's test
// that every kind is said in every language it is read in:
//
//	for _, l := range []string{"en", "ko"} {
//		require.Empty(t, past.Unsaid(sentences, l, pd.EventKinds), l)
//	}
//
// A kind said with a message of another type than `kinds` has for it is not
// said, since an event of the kind holds one of those.
func Unsaid(s *Sentences, locale string, kinds map[string]protoreflect.MessageType) []string {
	l := tag(locale)
	if l == "" {
		l = tag(s.def)
	}

	var out []string
	for kind, typ := range kinds {
		said := false
		for _, at := range unique(l, language(l)) {
			if sd, ok := s.by[at][kind]; ok && sd.typ.Descriptor().FullName() == typ.Descriptor().FullName() {
				said = true
				break
			}
		}
		if !said {
			out = append(out, kind)
		}
	}
	slices.Sort(out)

	return out
}

// chain is the locales a reader of `locale` is said to in, in order: it, its
// language, the default, and the default's language.
func (s *Sentences) chain(locale string) []string {
	l, d := tag(locale), tag(s.def)
	return unique(l, language(l), d, language(d))
}

// layout is how a moment is written in `locale`.
func (s *Sentences) layout(locale string) string {
	for _, l := range s.chain(locale) {
		if v, ok := s.layouts[l]; ok {
			return v
		}
	}

	return layout0
}

// tag is a locale as it is looked up, whichever way it was written: `en-gb`
// of `en_GB` and `EN-gb`.
func tag(locale string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
}

// language is the language a [tag] is of: `en` of `en-gb`, and `en` of `en`.
func language(tag string) string {
	v, _, _ := strings.Cut(tag, "-")
	return v
}

// unique is `vs` without the empty one or a second of any, in order.
func unique(vs ...string) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}

	return out
}

// unpack is what `v` holds, as the message `sd` is said with: false for a
// value of another type, or one that does not read as its own.
func (sd sayer) unpack(v *anypb.Any) (proto.Message, bool) {
	if v.MessageName() != sd.typ.Descriptor().FullName() {
		return nil, false
	}

	m := sd.typ.New().Interface()
	if err := v.UnmarshalTo(m); err != nil {
		return nil, false
	}

	return m, true
}

// decode is the message `ev` holds: read by the type a sentence of its kind
// is said with, in any locale, and by whatever this binary links where none
// is. Nil for one neither reads.
func (s *Sentences) decode(ev EventRow) protoreflect.Message {
	if ev.Value == nil {
		return nil
	}
	for _, by := range s.by {
		if sd, ok := by[ev.Kind]; ok {
			if m, ok := sd.unpack(ev.Value); ok {
				return m.ProtoReflect()
			}
		}
	}

	m, err := ev.Value.UnmarshalNew()
	if err != nil {
		return nil
	}

	return m.ProtoReflect()
}

// generic is an event said as nothing says it: its kind -- its identifier,
// for a row of none -- and what its message `m` holds, field by field.
//
// An identifier is said as itself, and never named. Nothing here knows which
// identifier is a person and which a thing, so naming one would be a guess at
// whose data the sentence shows, and would put somebody's name where no
// sentence was written to put it; and a thing named now is not what the
// message would have said of it. It is the last of what a reader is said
// before nothing, which [Unsaid] is there to keep anybody from reading.
func (r *Reading) generic(ev EventRow, m protoreflect.Message) string {
	head := ev.Kind
	if head == "" {
		head = ev.Id.String()
	}
	if m == nil {
		return head
	}
	if v := r.message(m); v != "{}" {
		return head + " " + v
	}

	return head
}

// message is what `m` holds, as [Reading.generic] says it: each field it
// holds, in the order of their numbers, a moment as [Reading.Time] writes
// one.
func (r *Reading) message(m protoreflect.Message) string {
	md := m.Descriptor()
	fs := md.Fields()
	switch md.FullName() {
	case "google.protobuf.Timestamp":
		return r.Time(time.Unix(m.Get(fs.ByNumber(1)).Int(), m.Get(fs.ByNumber(2)).Int()))
	case "google.protobuf.Duration":
		return (time.Duration(m.Get(fs.ByNumber(1)).Int())*time.Second + time.Duration(m.Get(fs.ByNumber(2)).Int())).String()
	}

	held := make([]protoreflect.FieldDescriptor, 0, fs.Len())
	for i := range fs.Len() {
		if fd := fs.Get(i); m.Has(fd) {
			held = append(held, fd)
		}
	}
	slices.SortFunc(held, func(a, b protoreflect.FieldDescriptor) int { return cmp.Compare(a.Number(), b.Number()) })

	vs := make([]string, len(held))
	for i, fd := range held {
		vs[i] = string(fd.Name()) + ": " + r.field(fd, m.Get(fd))
	}

	return "{" + strings.Join(vs, ", ") + "}"
}

// field is the value `v` of `fd`, which may be a list or a map: a map's
// entries in the order of what they say.
func (r *Reading) field(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch {
	case fd.IsList():
		l := v.List()
		vs := make([]string, l.Len())
		for i := range l.Len() {
			vs[i] = r.singular(fd, l.Get(i))
		}

		return "[" + strings.Join(vs, ", ") + "]"

	case fd.IsMap():
		var vs []string
		v.Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			vs = append(vs, r.singular(fd.MapKey(), k.Value())+": "+r.singular(fd.MapValue(), v))
			return true
		})
		slices.Sort(vs)

		return "{" + strings.Join(vs, ", ") + "}"
	}

	return r.singular(fd, v)
}

// singular is one value of `fd`: an enum by its name, a string quoted, and
// sixteen bytes -- an identifier, as everything an event names somebody or
// something by is -- as an identifier is written.
func (r *Reading) singular(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return strconv.FormatBool(v.Bool())

	case protoreflect.EnumKind:
		if e := fd.Enum().Values().ByNumber(v.Enum()); e != nil {
			return string(e.Name())
		}
		return strconv.FormatInt(int64(v.Enum()), 10)

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(v.Int(), 10)

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(v.Uint(), 10)

	case protoreflect.FloatKind:
		return strconv.FormatFloat(v.Float(), 'g', -1, 32)

	case protoreflect.DoubleKind:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)

	case protoreflect.StringKind:
		return strconv.Quote(v.String())

	case protoreflect.BytesKind:
		b := v.Bytes()
		if len(b) == len(uuid.UUID{}) {
			return uuid.UUID(b).String()
		}
		return base64.StdEncoding.EncodeToString(b)

	case protoreflect.MessageKind, protoreflect.GroupKind:
		return r.message(v.Message())
	}

	return v.String()
}
