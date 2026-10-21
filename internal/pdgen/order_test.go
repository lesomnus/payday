package pdgen_test

import (
	"regexp"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/lesomnus/payday/internal/pdgen"
)

// listed is a Robot with every kind of column, listed by `list`.
func listed(list string) string {
	return tenant + `
enum Mood {
  MOOD_UNSPECIFIED = 0;
  MOOD_GOOD = 1;
}
` + entity("Robot", `domain: 7, tenanted: {via: "tenant"}, list: {`+list+`}`,
		`Tenant tenant = 2 [(orm.edge) = {}];`,
		`string nick = 8 [(orm.field) = {nullable: true}];`,
		`string kept = 9 [(orm.field) = {nullable: true, default: "x"}];`,
		`string token = 10 [(payday.field) = {secret: true}];`,
		`bytes blob = 11;`,
		`repeated string tags = 12;`,
		`string doc = 16 [(orm.field) = {type: TYPE_JSON}];`,
		`int64 serial = 17;`,
		`uint64 count = 18;`,
		`Mood mood = 19;`,
		`bool on = 20;`,
		`double weight = 21;`,
		`float height = 22;`,
		`int32 rank = 23;`,
		`uint32 floor = 24;`,
		`string keeper = 25 [(payday.field) = {personal: true}];`,
		`google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];`,
	)
}

// TestAnOrderIsOverColumnsACursorCanCarry: a cursor is the last row's value of
// every column of the order, handed to the caller and back, and the next page
// is the rows past it. A column that may be null is not past or before
// anything, a secret would go out in every page, and a list, a map, JSON or
// bytes have no order a cursor can name -- so none of them is an order's, in
// the list's own order or in one a request names.
func TestAnOrderIsOverColumnsACursorCanCarry(t *testing.T) {
	refused := map[string]string{
		"nick":  "may be null",
		"kept":  "may be null",
		"token": "`secret:`",
		"blob":  "TYPE_BYTES",
		"tags":  "a list or a map",
		"doc":   "TYPE_JSON",
	}
	for column, why := range refused {
		t.Run(column+", as the list's order", func(t *testing.T) {
			_, err := read(t, listed(`order: [{field: {name: "`+column+`"}}, {field: {name: "id"}}], max: 100`))
			if err == nil || !strings.Contains(err.Error(), why) {
				t.Fatalf("want a refusal saying %q, got %v", why, err)
			}
		})
		t.Run(column+", as an order a request names", func(t *testing.T) {
			_, err := read(t, listed(`max: 100, orders: [{name: "by", order: [{field: {name: "`+column+`"}}, {field: {name: "id"}}]}]`))
			if err == nil || !strings.Contains(err.Error(), why) || !strings.Contains(err.Error(), "orders.by") {
				t.Fatalf("want a refusal of orders.by saying %q, got %v", why, err)
			}
		})
	}

	// Every scalar a cursor carries as itself is an order's -- a person's name
	// among them: its cursor carries the value of a row the same page answered
	// with, to the caller it answered.
	for _, column := range []string{"alias", "serial", "count", "mood", "on", "weight", "height", "rank", "floor", "keeper", "date_created", "id"} {
		t.Run(column+" is an order's", func(t *testing.T) {
			order := `{field: {name: "` + column + `"}}, {field: {name: "id"}}`
			if column == "id" {
				order = `{field: {name: "id"}}`
			}
			if _, err := read(t, listed(`max: 100, orders: [{name: "by", order: [`+order+`]}]`)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestACursorIsReadBackAsTheColumnIs: a cursor is JSON, and JSON gives a value
// back exactly only when it is read into the type it was written from. Read
// into `any`, as every column but a string, a time and an identifier used to
// be, a number is a float64 -- and an int64 past 2^53 is a neighbour of
// itself, so the cursor lands short of the row it came from and the next page
// answers that row again: a list read to its end that never ends.
func TestACursorIsReadBackAsTheColumnIs(t *testing.T) {
	want := map[string]string{
		"serial": "int64",
		"count":  "uint64",
		"rank":   "int32",
		"floor":  "uint32",
		"mood":   "int32",
		"on":     "bool",
		"weight": "float64",
		"height": "float32",
		"alias":  "string",
	}

	orders := []string{}
	for column := range want {
		orders = append(orders, `{name: "`+column+`", order: [{field: {name: "`+column+`"}}, {field: {name: "id"}}]}`)
	}
	src := inPkg("test", listed(`max: 100, orders: [`+strings.Join(orders, ", ")+`]`))

	s, p, err := readPlugin(t, map[string]string{"test/schema.proto": src})
	if err != nil {
		t.Fatal(err)
	}

	g := p.NewGeneratedFile("sink.go", "example.com/app/server/pd")
	g.P("package pd")
	g.P()
	pdgen.EmitSink(g, s, pdgen.Paths{
		Ent:  "example.com/app/internal/ent",
		Bare: "example.com/app/server/bare",
	}, protogen.GoImportPath("example.com/app"))

	b, err := g.Content()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)

	for column, typ := range want {
		name := strings.ToUpper(column[:1]) + column[1:]
		at := strings.Index(out, "func afterRobot"+name+"(")
		if at < 0 {
			t.Fatalf("no cursor of the order %q in what the sink wrote", column)
		}
		v := out[at:]
		if end := strings.Index(v[1:], "\nfunc "); end >= 0 {
			v = v[:end+1]
		}
		if !regexp.MustCompile(`\bat0\s+` + typ + `\b`).MatchString(v) {
			t.Errorf("%s: its cursor is not read back as %s:\n%s", column, typ, v)
		}
	}
	if strings.Contains(out, " any\n") {
		t.Errorf("a cursor column is read back as any:\n%s", out)
	}
}
