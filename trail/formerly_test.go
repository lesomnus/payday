package trail

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// TestAChunkLabelledWithAFormerWordIsOfItsKind: an earlier build labelled a
// chunk with the word its kind had then, and the schema that renamed the kind
// says what that was (`(payday.entity).formerly`, which `pdid.Formerly`
// registers). The chunk is the kind's: a check of the rows reads it with the
// kind's other chunks, a pass about the kind cuts it by the kind's window and
// a pass about everything else leaves it, and a report counts it under the
// kind's name now. A label nothing answers to is no kind, as before.
func TestAChunkLabelledWithAFormerWordIsOfItsKind(t *testing.T) {
	x := require.New(t)
	pdid.Formerly("trail.test.Android", robot, "android")

	by := chunksByKind([]Chunk{{Kind: "android", Rows: 1}, {Kind: "robot", Rows: 2}, {Kind: "", Rows: 3}, {Kind: "gadget", Rows: 4}})
	x.Len(by["robot"], 2, "the kind's chunks of both names")
	x.NotContains(by, "android")
	x.Len(by[""], 1, "what a format before kinds wrote")
	x.Len(by["gadget"], 1, "a word nothing answers to is left as it was")

	at := time.Now()
	_, ok := Only(robot).CutFor(at)("android")
	x.True(ok, "a pass about the kind cuts it")
	_, ok = Except(robot).CutFor(at)("android")
	x.False(ok, "a pass about everything else leaves it")

	x.Equal("robot", filed("android"))
	x.Equal("gadget", filed("gadget"))
	x.Equal("", filed(""))
}
