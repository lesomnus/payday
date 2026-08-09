package pdgen_test

import (
	"strings"
	"testing"
)

// TestTheGuideSaysWhatTheGeneratorSays checks the refusals docs/guide/schema.md
// quotes, by provoking them rather than by reading the source.
func TestTheGuideSaysWhatTheGeneratorSays(t *testing.T) {
	for _, tc := range []struct {
		what string
		src  string
		says string
	}{{
		"no option",
		`message Robot {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  option (orm.message) = {rpc: {crud: true}};
}`,
		"every entity has to say what its identifiers",
	}, {
		"domain 0",
		tenant + entity("Robot", `domain: 0, global: {}`),
		"0 is what an identifier nothing registered reads as",
	}, {
		"erasure unsaid",
		tenant + `
message Robot {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 7, global: {}};
}`,
		"nothing says what `Erase` does to a row",
	}, {
		"order not ending in the key",
		tenant + entity("Robot", `domain: 7, global: {},
			list: {order: [{field: "date_created"}], by: ["ref"], size: 2, max: 3}`,
			`google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];`),
		"has to end in the key",
	}, {
		"list with no max",
		tenant + entity("Robot", `domain: 7, global: {},
			list: {order: [{field: "id"}], by: ["ref"], size: 2}`),
		"`max` is required",
	}, {
		"watch with no version",
		tenant + entity("Robot", `domain: 7, global: {},
			list: {order: [{field: "id"}], by: ["ref"], size: 2, max: 3}, watch: {}`),
		"needs a version field",
	}, {
		"watch with no ref among the filters",
		tenant + entity("Robot", `domain: 7, global: {},
			list: {order: [{field: "id"}], by: ["alias"], size: 2, max: 3}, watch: {}`,
			`google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];`),
		"needs `ref` among the list's `by:`",
	}, {
		"two entities, one domain",
		tenant + entity("Robot", `domain: 7, global: {}`) + entity("Joint", `domain: 7, global: {}`),
		"both declare domain 7",
	}} {
		t.Run(tc.what, func(t *testing.T) {
			_, err := read(t, tc.src)
			if err == nil {
				t.Fatalf("not refused")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the guide quotes %q; it says:\n%v", tc.says, err)
			}
		})
	}
}
