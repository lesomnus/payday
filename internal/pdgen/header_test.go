package pdgen_test

import (
	"fmt"
	"strings"
	"testing"
)

func headerEntity(body string) string {
	return tenant + `
message Binding {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
` + body + `
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 7, global: {}, erase: {hard: {}}};
}
`
}

func TestReservedHeaderSlotsRejectAppFields(t *testing.T) {
	for _, tc := range []struct {
		number int
		name   string
	}{
		{4, "alias"}, {5, "name"}, {6, "desc"}, {7, "labels"},
		{13, "date_updated"}, {14, "date_erased"}, {15, "date_created"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := read(t, headerEntity(fmt.Sprintf("string binding_id = %d;", tc.number)))
			if err == nil {
				t.Fatal("an app field occupied a reserved header slot")
			}
			for _, want := range []string{"test.Binding", "binding_id", fmt.Sprint(tc.number), tc.name} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not identify %q: %v", want, err)
				}
			}
		})
	}
}

func TestReservedHeaderSlotsCheckProtobufShape(t *testing.T) {
	for _, body := range []string{
		`repeated string alias = 4;`,
		`repeated string name = 5;`,
		`repeated string desc = 6;`,
		`string labels = 7;`,
		`map<string, int64> labels = 7;`,
		`map<int64, string> labels = 7;`,
		`string date_updated = 13;`,
		`string date_erased = 14;`,
		`string date_created = 15;`,
		`repeated google.protobuf.Timestamp date_created = 15;`,
	} {
		t.Run(body, func(t *testing.T) {
			if _, err := read(t, headerEntity(body)); err == nil {
				t.Fatal("a reserved slot with the wrong protobuf shape was accepted")
			}
		})
	}
}

func TestReservedHeaderSlotsIncludeEdges(t *testing.T) {
	_, err := read(t, headerEntity(`Tenant owner = 4 [(orm.edge) = {}];`))
	if err == nil || !strings.Contains(err.Error(), "reserved for alias") {
		t.Fatalf("an edge occupied the alias slot: %v", err)
	}
}

func TestHeaderAndAppSlotsAreAccepted(t *testing.T) {
	src := headerEntity(`
  string alias = 4;
  string name = 5;
  string desc = 6;
  map<string, string> labels = 7;
  string a = 8;
  string b = 9;
  string c = 10;
  string d = 11;
  string e = 12;
  google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];
  google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {default: "", immutable: true}];
  string binding_id = 16;
  string f = 17;
`)
	src = strings.Replace(src, `domain: 7, global: {}, erase: {hard: {}}`, `domain: 7, global: {}`, 1)
	_, err := read(t, src)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReservedHeaderSlotsDoNotApplyToHelperMessages(t *testing.T) {
	_, err := read(t, tenant+`
message Request {
  string binding_id = 4;
  string value = 15;
}
`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOwnedEntitiesCannotAddAppFieldsInUnusedHeaderSlots(t *testing.T) {
	src := strings.Replace(ownAll(t), "message Outbox {", "message Outbox {\n string binding_id = 4;", 1)
	_, err := readAs(t, "fleet", src)
	if err == nil || !strings.Contains(err.Error(), "reserved for alias") {
		t.Fatalf("an overlay occupied an unused reserved slot: %v", err)
	}
}

func TestUnknownOwnMarkerDoesNotBypassReservedHeaderSlots(t *testing.T) {
	src := strings.Replace(headerEntity(`string binding_id = 4;`), "domain: 7,", "domain: 7, own: 100,", 1)
	_, err := read(t, src)
	if err == nil || !strings.Contains(err.Error(), "reserved for alias") {
		t.Fatalf("an unknown own marker bypassed slot validation: %v", err)
	}
}
