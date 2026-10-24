package past

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/lesomnus/payday/pdid"
)

// kindsOnly is a store that says what the app's event messages are, which is
// all a kind is read against.
type kindsOnly struct {
	Store
	kinds map[string]protoreflect.MessageType
}

func (s kindsOnly) Kinds() map[string]protoreflect.MessageType { return s.kinds }

// TestAKindIsWhatItsMessageDeclares: an event is written under the kind its
// message declares, or its full name when it declares none -- and of a message
// of several kinds, under the one the event says. A message that declares
// nothing, one the app's schema does not declare by that kind, an event of a
// message of several that says none of them, and a kind its message does not
// hold are no event's.
func TestAKindIsWhatItsMessageDeclares(t *testing.T) {
	happened, named, plain, corrected := message("Happened"), message("Named"), message("Plain"), message("Corrected")
	s := kindsOnly{kinds: map[string]protoreflect.MessageType{
		"thing.happen":    dynamicpb.NewMessageType(happened),
		"past.test.Named": dynamicpb.NewMessageType(named),
		"thing.correct":   dynamicpb.NewMessageType(corrected),
		"thing.retract":   dynamicpb.NewMessageType(corrected),
	}}

	t.Run("declared, and by its full name", func(t *testing.T) {
		x := require.New(t)

		kind, v, err := kindOf(s, "", dynamicpb.NewMessage(happened))
		x.NoError(err)
		x.Equal("thing.happen", kind)
		x.Equal("type.googleapis.com/past.test.Happened", v.GetTypeUrl())

		kind, _, err = kindOf(s, "", dynamicpb.NewMessage(named))
		x.NoError(err)
		x.Equal("past.test.Named", kind)

		kind, _, err = kindOf(s, "thing.happen", dynamicpb.NewMessage(happened))
		x.NoError(err, "the one kind of a message of one, said")
		x.Equal("thing.happen", kind)
	})

	t.Run("of a message of several, the one the event says", func(t *testing.T) {
		x := require.New(t)

		kind, v, err := kindOf(s, "thing.retract", dynamicpb.NewMessage(corrected))
		x.NoError(err)
		x.Equal("thing.retract", kind)
		x.Equal("type.googleapis.com/past.test.Corrected", v.GetTypeUrl())

		kind, _, err = kindOf(s, "thing.correct", dynamicpb.NewMessage(corrected))
		x.NoError(err)
		x.Equal("thing.correct", kind)
	})

	for name, c := range map[string]struct {
		s    Store
		kind string
		v    proto.Message
		says string
	}{
		"no message":             {s, "", nil, "a message the app declares with (payday.event)"},
		"one that declares none": {s, "", dynamicpb.NewMessage(plain), "says no (payday.event)"},
		"one the app does not declare": {kindsOnly{kinds: map[string]protoreflect.MessageType{
			"past.test.Named": dynamicpb.NewMessageType(named),
		}}, "", dynamicpb.NewMessage(happened), `is of kind "thing.happen", which this app's schema does not declare`},
		"another under its kind": {kindsOnly{kinds: map[string]protoreflect.MessageType{
			"thing.happen": dynamicpb.NewMessageType(named),
		}}, "", dynamicpb.NewMessage(happened), `is of kind "thing.happen", which this app's schema does not declare`},
		"one of several kinds, of none": {s, "", dynamicpb.NewMessage(corrected),
			`past.test.Corrected is what events of kinds "thing.correct" and "thing.retract" hold, and an event of it says which it is: past.Event{Kind: "thing.correct", ...}`},
		"a kind its message does not hold": {s, "thing.happen", dynamicpb.NewMessage(corrected),
			`past.test.Corrected is what events of kinds "thing.correct" and "thing.retract" hold, and not of "thing.happen"`},
		"another kind than the one of a message of one": {s, "thing.correct", dynamicpb.NewMessage(happened),
			`past.test.Happened is what events of kind "thing.happen" hold, and not of "thing.correct"`},
		"a kind of its message the app does not declare": {kindsOnly{kinds: map[string]protoreflect.MessageType{
			"thing.correct": dynamicpb.NewMessageType(corrected),
		}}, "thing.retract", dynamicpb.NewMessage(corrected), `is of kind "thing.retract", which this app's schema does not declare`},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			_, _, err := kindOf(c.s, c.kind, c.v)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
			require.ErrorContains(t, err, c.says)
		})
	}
}

// TestWhatAnOperationMakesIsNamedAfterIt: the nth thing of a kind an
// operation makes is the operation's identifier in that kind's domain, the
// nth differing from it in its last two bytes -- the same name each time it is
// asked, so that a copy sent again finds what the first made.
func TestWhatAnOperationMakesIsNamedAfterIt(t *testing.T) {
	x := require.New(t)
	o := &Op{event: pdid.New(17)}

	a := o.Made(7, 1)
	x.Equal(a, o.Made(7, 1))
	x.Equal(pdid.Domain(7), a.Domain())
	x.NotEqual(a, o.Made(7, 2))
	x.NotEqual(a, (&Op{event: pdid.New(17)}).Made(7, 1), "another operation's")
	x.Equal(o.event, o.Made(17, 0), "the operation's own event")
	m := o.Made(7, 65535)
	x.Equal(o.event[:9], m[:9], "only the domain and the last two bytes differ")
	x.Equal(o.event[10:14], m[10:14])

	x.Panics(func() { o.Made(7, -1) })
	x.Panics(func() { o.Made(7, 1<<16) })
}
