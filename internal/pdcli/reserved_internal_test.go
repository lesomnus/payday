package pdcli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheMergeOfAMessageKeepsWhatTheOverlayReserves is the pass over
// protobuf-merge's output on its own, where both sides can be written to
// reserve something: none of payday's entities reserves anything, so
// [TestAnOverlayOfPaydaysEntityKeepsWhatItReserves] cannot show a number
// both sides reserve being said once.
func TestTheMergeOfAMessageKeepsWhatTheOverlayReserves(t *testing.T) {
	const merged = `edition = "2023";

package app;

message Holder {
  string id = 1;
  reserved 3, 20 to 22;
  reserved gone;

  message Inner {
    string a = 1;
  }
}

message Other {
  string id = 1;
}
`
	const overlay = `edition = "2023";

package app;

message Holder {
  // A string once, at 8.
  reserved 8;

  // 3 and 20 to 22 are the base's already.
  reserved 3, 18 to 25;
  reserved gone, went;

  message Inner {
    reserved 2 to max;
  }
}
`
	got, err := keepReserved([]byte(merged), []byte(overlay), "holder.ext.proto")
	require.NoError(t, err)
	require.Equal(t, `edition = "2023";

package app;

message Holder {
  string id = 1;
  reserved 3, 20 to 22;
  reserved gone;

  message Inner {
    string a = 1;

    reserved 2 to max;
  }

  // A string once, at 8.
  reserved 8;

  // 3 and 20 to 22 are the base's already.
  reserved 18 to 19, 23 to 25;

  reserved went;
}

message Other {
  string id = 1;
}
`, string(got))

	// What is merged already is not said again: generation merges a
	// contract with one overlay after another, and runs twice.
	again, err := keepReserved(got, []byte(overlay), "holder.ext.proto")
	require.NoError(t, err)
	require.Equal(t, string(got), string(again))

	t.Run("and a file in proto3 names what it reserves as strings", func(t *testing.T) {
		got, err := keepReserved([]byte("syntax = \"proto3\";\nmessage M {\n  reserved \"a\";\n}\n"),
			[]byte("syntax = \"proto3\";\nmessage M {\n  reserved \"a\", \"b\";\n}\n"), "m.proto")
		require.NoError(t, err)
		require.Equal(t, "syntax = \"proto3\";\nmessage M {\n  reserved \"a\";\n\n  reserved \"b\";\n}\n", string(got))
	})
}

// TestTheMergeOfAnEnumKeepsWhatTheOverlayReserves is the same for an enum
// both sides declare, which protobuf-merge drops the overlay's `reserved` of
// in the same way: at the top of a file and inside a message, where an
// enum's numbers may be negative and its `max` is int32's.
func TestTheMergeOfAnEnumKeepsWhatTheOverlayReserves(t *testing.T) {
	const merged = `edition = "2023";

package app;

enum Kind {
  KIND_UNSPECIFIED = 0;
  reserved 3, 20 to 22;
  reserved KIND_GONE;
}

message Holder {
  enum State {
    STATE_UNSPECIFIED = 0;
  }
}
`
	const overlay = `edition = "2023";

package app;

enum Kind {
  // KIND_OLD was here.
  reserved -2, 8;

  // 3 and 20 to 22 are the base's already.
  reserved 3, 18 to 25;
  reserved KIND_GONE, KIND_OLD;
}

message Holder {
  enum State {
    reserved 2 to max;
  }
}
`
	got, err := keepReserved([]byte(merged), []byte(overlay), "kind.ext.proto")
	require.NoError(t, err)
	require.Equal(t, `edition = "2023";

package app;

enum Kind {
  KIND_UNSPECIFIED = 0;
  reserved 3, 20 to 22;
  reserved KIND_GONE;

  // KIND_OLD was here.
  reserved -2, 8;

  // 3 and 20 to 22 are the base's already.
  reserved 18 to 19, 23 to 25;

  reserved KIND_OLD;
}

message Holder {
  enum State {
    STATE_UNSPECIFIED = 0;

    reserved 2 to max;
  }
}
`, string(got))

	again, err := keepReserved(got, []byte(overlay), "kind.ext.proto")
	require.NoError(t, err)
	require.Equal(t, string(got), string(again))
}
