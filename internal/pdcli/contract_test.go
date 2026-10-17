package pdcli

import (
	"strings"
	"testing"
)

// TestAContractRpcPaydayDoesNotKnowIsRefused.
//
// What payday puts in front of an entity is written RPC by RPC, and an RPC a
// layer does not know is the one it inherits from the generated server behind
// it -- served past the gate, the Sink and the Secret layer, compiling, and
// with every test of the others still passing. AddMany was the first such RPC,
// and it would have been exactly that had it reached an app before payday
// knew it.
func TestAContractRpcPaydayDoesNotKnowIsRefused(t *testing.T) {
	const contract = `edition = "2023";

package app;

service RobotService {
  // Add creates a new Robot row.
  rpc Add(RobotAddRequest) returns (Robot);
  rpc AddMany(RobotAddManyRequest) returns (RobotAddManyResponse);
  rpc Get(RobotGetRequest) returns (Robot);
  rpc Patch(RobotPatchRequest) returns (Robot);
  rpc Apply(RobotApplyRequest) returns (Robot);
  rpc Erase(RobotRef) returns (RobotEraseResponse);
}
`
	if err := checkContractRpcs([]byte(contract), "app/robot_svc.g.proto"); err != nil {
		t.Fatalf("refused the RPCs payday knows: %v", err)
	}

	// The shape the next one would have: a write of many rows, which a layer
	// that knows only Patch would let past whatever it says of a Patch.
	unknown := strings.Replace(contract, "  rpc Erase(",
		"  rpc PatchMany(RobotPatchManyRequest) returns (RobotPatchManyResponse);\n  rpc Erase(", 1)

	err := checkContractRpcs([]byte(unknown), "app/robot_svc.g.proto")
	if err == nil {
		t.Fatal("a contract with an RPC payday has never heard of was generated")
	}
	for _, want := range []string{"app/robot_svc.g.proto", "`rpc PatchMany`", "go.mod"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("does not say %q:\n%s", want, err)
		}
	}
}
