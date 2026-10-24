package pdgen_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
)

// locked is a lock of two messages: a correction of two kinds, and a move of
// one.
func locked() *pdgen.Lock {
	return &pdgen.Lock{
		Kinds: map[string]string{
			"correct":    "app.Corrected",
			"retract":    "app.Corrected",
			"robot.move": "app.RobotMoved",
		},
		Messages: map[string]pdgen.LockMessage{
			"app.Corrected":  {1: {JSON: "event", Kind: "bytes"}, 2: {JSON: "at", Kind: "message", Type: "google.protobuf.Timestamp"}},
			"app.RobotMoved": {1: {JSON: "robot", Kind: "bytes"}},
		},
	}
}

// TestALockKeepsEveryKindWithItsMessage: a kind no message declares now --
// dropped by its message, or its message gone -- and a kind another message
// declares now are refused, a line for each kind; a kind a message takes is
// added. The message is the lock's once however many kinds hold it, and its
// going is said by its kinds.
func TestALockKeepsEveryKindWithItsMessage(t *testing.T) {
	t.Run("a kind a message takes", func(t *testing.T) {
		x := require.New(t)

		next := locked()
		next.Kinds["undo"] = "app.Corrected"

		v, err := locked().Admit(next)
		x.NoError(err)
		x.Equal(map[string]string{
			"correct":    "app.Corrected",
			"retract":    "app.Corrected",
			"robot.move": "app.RobotMoved",
			"undo":       "app.Corrected",
		}, v.Kinds)
		x.Len(v.Messages, 2)
	})

	for name, tc := range map[string]struct {
		edit func(*pdgen.Lock)
		want []string
	}{
		"a kind its message dropped": {
			func(l *pdgen.Lock) { delete(l.Kinds, "retract") },
			[]string{`kind "retract": its rows hold app.Corrected, and no message declares it now`},
		},
		"a kind moved to another message": {
			func(l *pdgen.Lock) { l.Kinds["retract"] = "app.RobotMoved" },
			[]string{`kind "retract": its rows hold app.Corrected, and app.RobotMoved declares it now`},
		},
		"a message gone": {
			func(l *pdgen.Lock) {
				delete(l.Kinds, "correct")
				delete(l.Kinds, "retract")
				delete(l.Messages, "app.Corrected")
			},
			[]string{
				`kind "correct": its rows hold app.Corrected, and no message declares it now`,
				`kind "retract": its rows hold app.Corrected, and no message declares it now`,
			},
		},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			x := require.New(t)

			next := locked()
			tc.edit(next)

			_, err := locked().Admit(next)
			var lost *pdgen.ErrLock
			x.ErrorAs(err, &lost)
			x.Equal(tc.want, lost.Lost, "a line for each kind, and none for the message whose kinds say it")
		})
	}
}

// TestALockIsReadAsItIsWritten: a lock reads back as the lock that was
// written, and a key it does not know is refused rather than dropped -- one
// dropped is gone from the lock written back. A lock of the payday before a
// message could hold several kinds is told how it is written now.
func TestALockIsReadAsItIsWritten(t *testing.T) {
	x := require.New(t)

	v, err := pdgen.ReadLock(locked().Marshal())
	x.NoError(err)
	x.Equal(locked(), v)

	v, err = pdgen.ReadLock([]byte(" \n"))
	x.NoError(err)
	x.Equal(&pdgen.Lock{}, v, "none at all")

	_, err = pdgen.ReadLock([]byte(`{"events": {"app.Corrected": "correct"}, "messages": {}}`))
	x.ErrorIs(err, pdgen.ErrLockOfEvents)

	_, err = pdgen.ReadLock([]byte(`{"kinds": {}, "later": {}}`))
	x.ErrorContains(err, `unknown field "later"`)

	_, err = pdgen.ReadLock([]byte(`{"messages": {"app.Corrected": {"1": {"json": "event", "kind": "bytes", "since": "v2"}}}}`))
	x.ErrorContains(err, `unknown field "since"`, "nor of a field")

	_, err = pdgen.ReadLock([]byte(`{"kinds": {}} {"kinds": {}}`))
	x.Error(err, "two locks in one")
}
