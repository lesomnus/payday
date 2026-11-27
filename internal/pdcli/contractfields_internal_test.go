package pdcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// contract is a contract as the service generator writes one, with a field's
// options over lines as it writes them, a oneof, and a nested message, which
// it writes none of today and an overlay is held to all the same.
const contract = `edition = "2023";

package app;

import "orm.proto";

service MachineService {
	rpc Add(MachineAddRequest) returns (Machine);
	rpc Get(MachineGetRequest) returns (Machine);
}

message MachineAddRequest {
	bytes id = 1 [
		(orm.field) = {type: TYPE_UUID}
	];
	string alias = 4 [
		features.field_presence = IMPLICIT
	];
	string model = 8 [
		features.field_presence = IMPLICIT
	];
	int64 hours = 9 [
		features.field_presence = IMPLICIT
	];
	map<string, string> settings = 23;

	message Spec {
		string size = 1;
	}
}

message MachineRef {
	oneof key {
		bytes id = 1 [
			(orm.field) = {type: TYPE_UUID}
		];
	}
}
`

func overlayAt(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "machine_svc.ext.proto")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// TestAServiceOverlayDoesNotSayAGeneratedFieldAgain: the merge takes a field
// an overlay says again whole and keeps nothing of the generator's, so a
// number or a name the contract's message has is refused, naming the file,
// the message, the number and both spellings.
func TestAServiceOverlayDoesNotSayAGeneratedFieldAgain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		overlay string
		want    []string
	}{
		{
			name:    "a generated number with another type",
			overlay: "message MachineAddRequest {\n  int32 model = 8;\n}\n",
			want: []string{
				"machine_svc.ext.proto: message MachineAddRequest says field 8 again, which the contract's message already has.",
				"the contract's: string model = 8 [features.field_presence = IMPLICIT];",
				"the overlay's:  int32 model = 8;",
			},
		},
		{
			name:    "a generated number with only an option added",
			overlay: "message MachineAddRequest {\n  int64 hours = 9 [deprecated = true];\n}\n",
			want: []string{
				"message MachineAddRequest says field 9 again",
				"the contract's: int64 hours = 9 [features.field_presence = IMPLICIT];",
				"the overlay's:  int64 hours = 9 [deprecated = true];",
			},
		},
		{
			name:    "the same field, said the same, options over lines",
			overlay: "message MachineAddRequest {\n  string alias = 4 [\n    features.field_presence = IMPLICIT\n  ];\n}\n",
			want:    []string{"message MachineAddRequest says field 4 again"},
		},
		{
			name:    "inside a oneof",
			overlay: "message MachineRef {\n  oneof key {\n    string id = 1;\n  }\n}\n",
			want: []string{
				"message MachineRef says field 1 again",
				"the contract's: bytes id = 1 [(orm.field) = {type: TYPE_UUID}];",
				"the overlay's:  string id = 1;",
			},
		},
		{
			name:    "a generated oneof's field said outside it",
			overlay: "message MachineRef {\n  bytes id = 1;\n}\n",
			want:    []string{"message MachineRef says field 1 again"},
		},
		{
			name:    "a generated name under a new number",
			overlay: "message MachineAddRequest {\n  string alias = 900;\n}\n",
			want: []string{
				"message MachineAddRequest says field `alias` again at 900, which the contract's message already has at 4.",
				"the overlay's:  string alias = 900;",
			},
		},
		{
			name:    "a map",
			overlay: "message MachineAddRequest {\n  map<string, int64> settings = 23;\n}\n",
			want:    []string{"the contract's: map<string, string> settings = 23;"},
		},
		{
			name:    "a nested message's field",
			overlay: "message MachineAddRequest {\n  message Spec {\n    int32 size = 1;\n  }\n}\n",
			want: []string{
				"message MachineAddRequest.Spec says field 1 again",
				"the contract's: string size = 1;",
				"Add what is wanted at a number MachineAddRequest.Spec leaves empty",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			err := checkOverlayFields([]byte(contract), overlayAt(t, "edition = \"2023\";\n\npackage app;\n\n"+tc.overlay))
			x.Error(err)
			for _, w := range tc.want {
				x.ErrorContains(err, w)
			}
			x.ErrorContains(err, "The merge would take the overlay's field whole -- its type, its presence and its options -- and keep nothing the generator said of it")
			x.ErrorContains(err, "An overlay **adds**.")
			x.ErrorContains(err, "give it an RPC of its own under its own name")
		})
	}
}

// TestAServiceOverlayStillAdds: what is new to the contract's message, a
// message of the app's own and an RPC of its own are as they were.
func TestAServiceOverlayStillAdds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		overlay string
	}{
		{
			name:    "a new field in a generated message",
			overlay: "message MachineAddRequest {\n  string reason = 102 [\n    features.field_presence = IMPLICIT\n  ];\n  oneof why {\n    string note = 103;\n  }\n}\n",
		},
		{
			name:    "a new message, with numbers and names a generated one has",
			overlay: "message MachineRetireRequest {\n  MachineRef ref = 1;\n  string alias = 4;\n  message Spec {\n    int32 size = 1;\n  }\n}\n",
		},
		{
			name:    "a new message nested in a generated one",
			overlay: "message MachineAddRequest {\n  message Extra {\n    string alias = 4;\n  }\n  Extra extra = 100;\n}\n",
		},
		{
			name:    "a new RPC",
			overlay: "service MachineService {\n  rpc Retire(MachineRetireRequest) returns (Machine);\n}\nmessage MachineRetireRequest {\n  MachineRef ref = 1;\n}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, checkOverlayFields([]byte(contract), overlayAt(t, "edition = \"2023\";\n\npackage app;\n\n"+tc.overlay)))
		})
	}
}

// TestAServiceOverlayIsRefusedForOneThingFirst: of an overlay that says again
// both an RPC and a field, and two fields, the refusal names the same one
// every time -- the RPC, then the first field as the overlay writes them.
func TestAServiceOverlayIsRefusedForOneThingFirst(t *testing.T) {
	x := require.New(t)

	p := overlayAt(t, `edition = "2023";

package app;

message MachineAddRequest {
  int32 model = 8;
  int64 hours = 9 [deprecated = true];
}

service MachineService {
  rpc Get(MachineGetRequest) returns (Machine);
}
`)
	for range 20 {
		err := checkContractOverlay([]byte(contract), p)
		x.Error(err)
		x.ErrorContains(err, "declares `rpc Get`, which the contract already has")
	}

	x.NoError(os.WriteFile(p, []byte("edition = \"2023\";\n\npackage app;\n\nmessage MachineAddRequest {\n  int64 hours = 9 [deprecated = true];\n  int32 model = 8;\n}\n"), 0o644))
	for range 20 {
		err := checkContractOverlay([]byte(contract), p)
		x.Error(err)
		x.ErrorContains(err, "says field 9 again")
	}
}
