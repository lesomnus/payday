package pdid_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// TestName is the one rule for what a person calls an entity.
//
// It is one rule because it had been three: the generator kebab-cased what the
// schema declared, `pdcmd` lowercased the message name and read no declaration
// at all, and the two agreed on every entity anybody had tried -- all of which
// are one word. `SshKey` is where they part.
func TestName(t *testing.T) {
	for _, tc := range []struct {
		desc     string
		declared string
		message  string
		want     string
	}{
		{
			desc:    "one word is itself, lowercased",
			message: "Robot",
			want:    "robot",
		},
		{
			desc:    "two words are hyphenated, which is what the option promises",
			message: "SshKey",
			want:    "ssh-key",
		},
		{
			desc:    "and three",
			message: "WorkCellGroup",
			want:    "work-cell-group",
		},
		{
			desc:     "what the schema said wins, which is why it may be said",
			declared: "key",
			message:  "SshKey",
			want:     "key",
		},
		{
			desc:     "including a word the derivation would never have reached",
			declared: "people",
			message:  "Holder",
			want:     "people",
		},
		{
			desc:    "a name already lowercase is left alone",
			message: "outbox",
			want:    "outbox",
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			require.Equal(t, tc.want, pdid.Name(tc.declared, tc.message))
		})
	}
}
