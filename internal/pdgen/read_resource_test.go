package pdgen_test

import (
	"strings"
	"testing"

	"github.com/lesomnus/payday/internal/pdgen"
)

const computedReading = `
message Reading {
 bytes id = 1;
 string value = 2;
 option (payday.resource) = {name:"reading", filter:".test.ReadingFilter", metadata:".test.ReadingSnapshot", watch:true};
}
message ReadingFilter { bytes parent_id = 1; }
message ReadingSnapshot { string revision = 1; }
`

func TestComputedResourceStaysOutsideORMEntities(t *testing.T) {
	schema, plugin, err := readPlugin(t, map[string]string{"test/reading.proto": inPkg("test", computedReading)})
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Entities) != 0 || len(schema.Resources) != 1 {
		t.Fatalf("computed value became a stored entity: entities=%d resources=%d", len(schema.Entities), len(schema.Resources))
	}
	if err := pdgen.ValidateReadResources(schema, plugin.Files); err != nil {
		t.Fatal(err)
	}
}

func TestComputedResourceDeclarationsRefuseAmbiguousOrIgnoredContracts(t *testing.T) {
	for _, test := range []struct{ name, source, want string }{
		{"wrong identity", strings.Replace(computedReading, "bytes id = 1", "string id = 1", 1), "opaque bytes id = 1"},
		{"nested resource", "message Outer {" + computedReading + "}", "top-level message"},
		{"missing filter", strings.Replace(computedReading, `filter:".test.ReadingFilter"`, `filter:""`, 1), "fully-qualified message"},
		{"invalid type", strings.Replace(computedReading, ".test.ReadingFilter", "bad/type", 1), "fully-qualified message"},
		{"duplicate resource name", computedReading + `message Other {bytes id=1; option(payday.resource)={name:"reading",filter:".test.ReadingFilter"};}`, "collides"},
		{"ORM declaration", strings.Replace(strings.Replace(computedReading, "bytes id = 1", `bytes id = 1 [(orm.field)={key:true,type:TYPE_UUID,default:""}]`, 1), "string value = 2;", `string value=2; option(orm.message)={rpc:{get:{}}};`, 1), "cannot also declare"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := read(t, test.source)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}

func TestComputedResourceMetadataMustResolveToMessagesAfterMerge(t *testing.T) {
	for _, source := range []string{
		strings.Replace(computedReading, ".test.ReadingFilter", ".test.Missing", 1),
		strings.Replace(computedReading, ".test.ReadingSnapshot", ".test.Missing", 1),
		strings.Replace(computedReading, ".test.ReadingFilter", ".test.Kind", 1) + `enum Kind { ZERO = 0; }`,
	} {
		schema, plugin, err := readPlugin(t, map[string]string{"test/reading.proto": inPkg("test", source)})
		if err != nil {
			t.Fatal(err)
		}
		if err := pdgen.ValidateReadResources(schema, plugin.Files); err == nil || !strings.Contains(err.Error(), "not a message after contract merging") {
			t.Fatalf("unresolved/non-message resource contract type was accepted: %v", err)
		}
	}
}

func TestComputedResourceHelpersReserveTheirGoSymbols(t *testing.T) {
	for _, declaration := range []string{
		strings.Replace(computedReading, "message Reading {", "message ResourceServer {", 1),
		computedReading + "message ResourceServer {}",
		computedReading + "message StaticResourceServer {}",
		computedReading + "message ServerWithResources {}",
		computedReading + "message ReadResources {}",
		computedReading + "message ComposeServer {}",
		computedReading + "message RegisterServerWithResources {}",
		computedReading + "enum ResourceServer { UNSPECIFIED = 0; }",
		computedReading + "service Resource { rpc Read(ReadingFilter) returns (Reading); }",
	} {
		_, err := read(t, declaration)
		if err == nil || !strings.Contains(err.Error(), "reserved for generated read resource helpers") {
			t.Errorf("helper symbol collision was not refused: %s: %v", declaration, err)
		}
	}
	// A protobuf dependency in another Go package is qualified at use sites;
	// its type names do not occupy the generated application's package.
	_, err := readFiles(t, map[string]string{
		"test/reading.proto":    inPkg("test", computedReading),
		"external/source.proto": `edition="2023"; package external; option go_package="example.com/external"; message ResourceServer {}`,
	})
	if err != nil {
		t.Fatalf("another package's type incorrectly reserved an app symbol: %v", err)
	}
}

func TestComputedResourceCannotShadowMiddlewareTraversal(t *testing.T) {
	resource := strings.Replace(computedReading, "message Reading {", "message Next {", 1)
	if _, err := read(t, resource); err != nil {
		t.Fatalf("resource-only schemas have no ORM middleware traversal to shadow: %v", err)
	}
	_, err := read(t, entity("Stored", "domain: 7, global: {}")+resource)
	if err == nil || !strings.Contains(err.Error(), "reserved for middleware traversal") {
		t.Fatalf("resource getter shadowed middleware Next: %v", err)
	}
}
