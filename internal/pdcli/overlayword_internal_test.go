package pdcli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheOverlaysWordIsPutWherePaydaysWas: a value an option of payday's
// entity and its overlay both say, and say differently, is the overlay's in
// what the merge is handed, so that the merge -- which keeps the base's --
// keeps the overlay's. What only one side says, and a list, are left to the
// merge.
func TestTheOverlaysWordIsPutWherePaydaysWas(t *testing.T) {
	const base = `edition = "2023";

message Holder {
  option (payday.entity) = {
    own: OWN_HOLDER
    domain: 2
    list: {
      by: [{name: "ref"}]
      // payday's page
      size: 20
      max: 100
    }
  };
  option deprecated = false;

  message Inner {
    option (orm.message) = {rpc: {erase: {hard: {}}}};
  }
}

message Other {
  option (payday.entity) = {domain: 3};
}
`

	t.Run("a scalar both say, nested in literals, and one written as a value", func(t *testing.T) {
		x := require.New(t)

		const overlay = `message Holder {
  option (payday.entity) = {
    list: {max: 500, by: [{name: "tenant"}]}
  };
  option deprecated = true;
  message Inner {
    option (orm.message) = {rpc: {erase: {hard: {}, disabled: true}}};
  }
}
`
		got, err := overlaysWord([]byte(base), "holder.proto", []byte(overlay), "holder.ext.proto")
		x.NoError(err)
		x.Equal(`edition = "2023";

message Holder {
  option (payday.entity) = {
    own: OWN_HOLDER
    domain: 2
    list: {
      by: [{name: "ref"}]
      // payday's page
      size: 20
      max: 500
    }
  };
  option deprecated = true;

  message Inner {
    option (orm.message) = {rpc: {erase: {hard: {}}}};
  }
}

message Other {
  option (payday.entity) = {domain: 3};
}
`, string(got), "the list and what only the overlay says are the merge's; Other is not the overlay's")
	})

	t.Run("one said the same, spaced otherwise, changes nothing", func(t *testing.T) {
		x := require.New(t)

		const overlay = `message Holder {
  option (payday.entity) = {list: {size:   20}, domain: 2};
}
`
		got, err := overlaysWord([]byte(base), "holder.proto", []byte(overlay), "holder.ext.proto")
		x.NoError(err)
		x.Equal(base, string(got))
	})

	t.Run("an overlay of a message by its path, nested ones too", func(t *testing.T) {
		x := require.New(t)

		const overlay = `message Other {
  option (payday.entity) = {domain: 9};
}
message Holder {
  message Inner {
    option (orm.message) = {rpc: {erase: {hard: {after: "1h"}}}};
  }
}
`
		got, err := overlaysWord([]byte(base), "holder.proto", []byte(overlay), "holder.ext.proto")
		x.NoError(err)
		x.Contains(string(got), "message Other {\n  option (payday.entity) = {domain: 9};")
		x.Contains(string(got), "    domain: 2\n", "Holder's own domain is not Other's")
		x.Contains(string(got), `option (orm.message) = {rpc: {erase: {hard: {}}}};`,
			"an empty literal and a full one are both literals, and what only one says is the merge's to fill in")
	})
}

// TestAnOptionAnOverlaySaysTwiceIsRefused: the merge takes one of them by name
// and the other is gone, and the overlay is never compiled by itself, so
// protoc's refusal of it is never reached.
func TestAnOptionAnOverlaySaysTwiceIsRefused(t *testing.T) {
	const overlay = `message Holder {
  option (payday.entity) = {list: {max: 500}};
  option (payday.entity) = {tenanted: {via: "tenant"}};
}
`
	_, err := overlaysWord([]byte("message Holder {}\n"), "holder.proto", []byte(overlay), "holder.ext.proto")
	require.ErrorContains(t, err, "holder.ext.proto: message Holder says option (payday.entity) twice, and the merge keeps one of them")
}
