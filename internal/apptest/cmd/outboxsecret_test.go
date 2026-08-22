package cmd_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
)

// TestTheQueueHoldsNoVerifierEither.
//
// F15 found a `secret:` column in `Audit.patch`: the declaration cleared the
// row payday records and said nothing about the **document** the write was
// compiled from, so a `Patch` on a verifier copied it into the one table
// nothing erases. The fix was a redactor the trail's recorder runs.
//
// There are three recorders reading the same `bare.Change`, and only one of
// them was fixed. This is the second: the queue holds the same document, at
// rest in a table until something drains it, and what it is drained into is
// whichever broker a deployment names. Nothing carries a patch off the box
// today -- `watchpg` deliberately sends none -- which is a property of the
// brokers that exist rather than of this line.
//
// Written against the row rather than against the published event, because the
// row is the part that persists and the part a second reader of the database
// can see.
func TestTheQueueHoldsNoVerifierEither(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)

	secret := []byte("this is a verifier and must not be here")

	v, err := b.Ungated.Seal().Add(ctx, app.SealAddRequest_builder{
		Alias:  "one",
		Secret: secret,
	}.Build())
	x.NoError(err)

	// The Add is not the interesting one -- it has no patch document at all --
	// so the queue is emptied and the assertion is about the write that does.
	x.NoError(drainOnce(t, b))

	_, err = b.Ungated.Seal().Patch(ctx, app.SealPatchRequest_builder{
		Ref:    app.SealRef_builder{Id: v.GetId()}.Build(),
		Secret: []byte("a second verifier, equally not for here"),
	}.Build())
	x.NoError(err)

	vs, err := b.Ent.Outbox.Query().All(ctx)
	x.NoError(err)
	x.NotEmpty(vs, "nothing was queued, so this proves nothing")

	for _, u := range vs {
		x.False(bytes.Contains(u.Patch, secret),
			"a verifier reached the queue in %s", u.Method)
		x.False(bytes.Contains(u.Patch, []byte("a second verifier")),
			"a verifier reached the queue in %s", u.Method)
	}
}
