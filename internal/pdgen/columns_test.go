package pdgen_test

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestColumnsAreValidatedDuringGeneration(t *testing.T) {
	schema := func(cols string) string {
		return `message Detail { string label=1; string token=2; }` + entity("Part", `domain:7 global:{} columns:[`+cols+`]`,
			`string number=8;`, `Detail detail=9;`, `repeated string tags=10;`, `string token=11 [(payday.field).secret=true];`)
	}
	_, err := read(t, schema(`{field:"number" title:"PART"},{field:"detail.label"},{field:"id" wide:true}`))
	require.NoError(t, err)
	for cols, why := range map[string]string{
		`{field:"missing"}`:      "unknown field",
		`{field:"number.child"}`: "scalar field",
		`{field:"tags"}`:         "list or map",
		`{field:"token"}`:        "secret",

		`{field:"detail"}`:                      "scalar field",
		`{field:"id" wide:true}`:                "without wide",
		`{field:"number"},{field:"number"}`:     "duplicate",
		`{field:"number" title:"PART\nNUMBER"}`: "one table cell",
	} {
		t.Run(cols, func(t *testing.T) { _, err := read(t, schema(cols)); require.ErrorContains(t, err, why) })
	}
}
