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
// message declares, or its full name when it declares none -- and a message
// that declares nothing, or one the app's schema does not declare by that
// kind, is no event's.
func TestAKindIsWhatItsMessageDeclares(t *testing.T) {
	happened, named, plain := message("Happened"), message("Named"), message("Plain")
	s := kindsOnly{kinds: map[string]protoreflect.MessageType{
		"thing.happen":    dynamicpb.NewMessageType(happened),
		"past.test.Named": dynamicpb.NewMessageType(named),
	}}

	t.Run("declared, and by its full name", func(t *testing.T) {
		x := require.New(t)

		kind, v, err := kindOf(s, dynamicpb.NewMessage(happened))
		x.NoError(err)
		x.Equal("thing.happen", kind)
		x.Equal("type.googleapis.com/past.test.Happened", v.GetTypeUrl())

		kind, _, err = kindOf(s, dynamicpb.NewMessage(named))
		x.NoError(err)
		x.Equal("past.test.Named", kind)
	})

	for name, c := range map[string]struct {
		s Store
		v proto.Message
	}{
		"no message":             {s, nil},
		"one that declares none": {s, dynamicpb.NewMessage(plain)},
		"one the app does not declare": {kindsOnly{kinds: map[string]protoreflect.MessageType{
			"past.test.Named": dynamicpb.NewMessageType(named),
		}}, dynamicpb.NewMessage(happened)},
		"another under its kind": {kindsOnly{kinds: map[string]protoreflect.MessageType{
			"thing.happen": dynamicpb.NewMessageType(named),
		}}, dynamicpb.NewMessage(happened)},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			_, _, err := kindOf(c.s, c.v)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
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
