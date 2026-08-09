package pdcli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Entity writes a new entity into the app's schema.
//
// # Why this is a command rather than a paragraph in a README
//
// Almost nothing here saves typing. What it saves is the two things a person
// adding the fourth entity to a schema gets wrong, and both of them fail in the
// way payday exists to prevent:
//
//   - **A domain that is already taken.** Two entities sharing one makes an
//     identifier lie about what it names, and it is caught at generation -- but
//     only after the schema is written and only if the generation is run.
//     Picking a free one is a thing a machine should do.
//   - **Tenancy left unsaid.** Generation refuses it, which is the point, but a
//     scaffold that emitted the option without it would be a scaffold whose
//     first output does not build.
//
// So what it writes is the shape that is hard to get right, and it leaves the
// fields -- which are the app's whole reason for existing and which nothing can
// guess -- as a `TODO`.
type Entity struct {
	Layout Layout

	// Name is the message, in the case a proto message is written in: `Robot`.
	Name string

	// File is the .proto it goes in, relative to the schema directory. Empty is
	// one named after the entity.
	File string

	// Tenanted, Tenant and Global are the three tenancies, and exactly one has
	// to be chosen: see [Entity.Add].
	Tenanted bool
	Tenant   bool
	Global   bool

	// Watch adds a `watch:` and the version field it needs.
	Watch bool
}

// Add writes the entity and answers with the file it went in.
func (e Entity) Add() (string, error) {
	if !valid.MatchString(e.Name) {
		return "", fmt.Errorf("%q is not a message name; it begins with a capital and holds letters and digits", e.Name)
	}

	switch n := count(e.Tenanted, e.Tenant, e.Global); {
	case n == 0:
		// Behind the wall, which is what saying nothing means in the schema too.
		// The two answers that are not the ordinary one are the ones written
		// down, here and there for the same reason: getting tenancy wrong hides
		// every row in one direction and shows every row to everybody in the
		// other, and only the first is ever noticed.
		e.Tenanted = true
	case n > 1:
		return "", fmt.Errorf("an entity is behind the wall or it is not; pick one of --tenanted, --tenant, --global")
	}

	dir := e.Layout.Path(DirProto, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	name := e.File
	if name == "" {
		name = snake(e.Name) + ".proto"
	}
	if !strings.HasSuffix(name, ".proto") {
		name += ".proto"
	}

	p := filepath.Join(dir, name)

	domain, err := e.free()
	if err != nil {
		return "", err
	}

	b, err := os.ReadFile(p)
	switch {
	case err == nil:
		if regexp.MustCompile(`(?m)^message ` + regexp.QuoteMeta(e.Name) + `\s`).Match(b) {
			return "", fmt.Errorf("%s already holds a message %s", name, e.Name)
		}

		b = append(b, '\n')
		b = append(b, e.message(domain)...)

	case os.IsNotExist(err):
		b = append([]byte(e.head()), e.message(domain)...)

	default:
		return "", err
	}

	return p, os.WriteFile(p, b, 0o644)
}

// free is a domain nothing else in this schema has.
//
// It reads every `domain:` in the app's schema and in payday's, which is the
// half a person doing this by hand forgets: payday's own entities hold the low
// numbers, and an app that started at 1 collides with Tenant on its first
// entity.
//
// It is what goes into the file this writes, and nothing else reads it.
// Generation does not allocate: an entity with no `(payday.entity)` is refused
// and told what to write. So this is a first draft that somebody edits, not an
// assignment somebody inherits.
func (e Entity) free() (int, error) {
	taken := map[int]bool{}

	dirs := []string{e.Layout.Path(DirProto)}
	if d, err := SchemaDir(); err == nil {
		dirs = append(dirs, d)
	}

	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
			if err != nil || !strings.HasSuffix(p, ".proto") {
				return nil
			}

			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}

			for _, m := range domainAt.FindAllSubmatch(b, -1) {
				if n, err := strconv.Atoi(string(m[1])); err == nil {
					taken[n] = true
				}
			}

			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	}

	// One past the highest, rather than the lowest that is free, and starting
	// at 7 -- payday keeps the low numbers for what it ships, and the gap means
	// an entity added to payday later does not have to take one an app uses.
	//
	// **Past the highest** because a number is only free in the file. A domain
	// outlives the row it named: an identifier in an audit trail says what kind
	// of thing it was long after the row is gone, so handing a deleted entity's
	// number to the next one makes those rows say the wrong word. Nothing
	// detects that, before or after.
	//
	// Which is a reason to not offer the number, not a reason to refuse it.
	// Reuse is a decision an app can make -- the number is written in its own
	// schema, in plain sight -- and one it should make on purpose rather than
	// by accepting what this suggested.
	//
	// It only goes as far as the schema can say. A schema holds what it is, not
	// what it used to be, so a number is skipped while something above it
	// survives and comes back when the highest-numbered entity is the one that
	// went. Closing that needs a record of every number ever allocated, which
	// is a second file to keep in step with the first.
	next := 6
	for n := range taken {
		if n > next {
			next = n
		}
	}
	if next++; next < 256 {
		return next, nil
	}

	return 0, fmt.Errorf("the highest domain in this schema is 255, and this would be the next one")
}

var domainAt = regexp.MustCompile(`(?m)^\s*domain:\s*(\d+)`)

var valid = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

func (e Entity) head() string {
	return fmt.Sprintf(`edition = "2023";

package app;

import "payday/tenant.proto";
import "google/protobuf/timestamp.proto";
import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = %q;
`, e.Layout.Module)
}

func (e Entity) message(domain int) string {
	b := &strings.Builder{}

	fmt.Fprintf(b, "\n// %s is TODO: say what one of these is, and why it exists.\nmessage %s {\n", e.Name, e.Name)
	b.WriteString("  bytes id = 1 [(orm.field) = {\n    type: TYPE_UUID\n    key: true\n    default: \"\"\n  }];\n")

	if e.Tenanted {
		b.WriteString("\n  payday.Tenant tenant = 2 [(orm.edge) = {immutable: true}];\n")
	}

	b.WriteString("\n  // The name a person writes this as. Every write of it is folded and\n")
	b.WriteString("  // checked on the way in, so \"  Arm-01 \" and \"arm-01\" are one row,\n")
	b.WriteString("  // and one that was not given is made up.\n")
	b.WriteString("  //\n")
	b.WriteString("  // Delete it if nothing here is named by a person -- a measurement, a\n")
	b.WriteString("  // sample, a row of a log. There is no switch to turn off: `alias` is\n")
	b.WriteString("  // found by name, so an entity without one gets no naming and no slug.\n")
	b.WriteString("  string alias = 4;\n")
	b.WriteString("\n  // 5 is `name`, 6 is `desc`, 7 is `map<string, string> labels`. They are\n")
	b.WriteString("  // the header: what anything can read off a row it has no type for.\n")
	b.WriteString("  // Declare the ones this entity wants, at those numbers, and leave the\n")
	b.WriteString("  // rest empty.\n")
	b.WriteString("\n  // 3 is yours, and payday will not spend it. It is where a set smaller\n")
	b.WriteString("  // than a tenant goes -- a site, a cell, a region -- so that a reader\n")
	b.WriteString("  // with no type for this row can still find it. See `docs/SCHEMA.md`.\n")
	b.WriteString("\n  // TODO: this entity's own fields, in 8..12 and from 16.\n")

	if e.Watch {
		b.WriteString("\n  // Erased softly, which is what saying nothing about erasure means: the\n")
	b.WriteString("  // row is stamped and stays, so it cannot be read or changed, its alias\n")
	b.WriteString("  // comes free again, and the trail can still say what it held. Delete\n")
	b.WriteString("  // this and say `erase: {hard: {}}` if losing the row is the point --\n")
	b.WriteString("  // a table of readings cannot be one nothing may be removed from.\n")
	b.WriteString("  google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];\n")
	b.WriteString("\n  // Stamped on every write and refused to a patch document, so it is a\n")
		b.WriteString("  // version and not a field. A `watch:` is refused without one: a watch\n")
		b.WriteString("  // sends state, so two answers about one row have to be orderable or a\n")
		b.WriteString("  // late one erases an early one that was newer.\n")
		b.WriteString("  google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];\n")
	}

	b.WriteString("\n  google.protobuf.Timestamp date_created = 15 [(orm.field) = {\n    immutable: true\n    default: \"\"\n  }];\n")

	b.WriteString("\n  option (orm.message) = {\n    rpc: {crud: true}\n")
	if e.Watch {
		b.WriteString("    indexes: [\n")
		b.WriteString("      // The order a List reads in. Generation warns without it, because\n")
		b.WriteString("      // the read it names would scan the table and sort what it found.\n")
		b.WriteString("      {\n        name: \"page\"\n        refs: [\n")
		b.WriteString("          {\n            name: \"date_created\"\n            number: 15\n          },\n")
		b.WriteString("          {\n            name: \"id\"\n            number: 1\n          }\n")
		b.WriteString("        ]\n      }\n")
		b.WriteString("    ]\n")
	}
	b.WriteString("  };\n")

	fmt.Fprintf(b, "  option (payday.entity) = {\n    domain: %d\n", domain)
	switch {
	case e.Tenanted:
		// Nothing, which is the declaration. What is written out is the answer
		// that leaks when it is wrong.
		b.WriteString("\n    // Nothing about tenancy, which is the declaration: behind the\n")
		b.WriteString("    // wall, by the edge called `tenant`. `global: {}` is the other\n")
		b.WriteString("    // answer and it has to be written, because that is the one that\n")
		b.WriteString("    // shows every row to everybody when it is wrong.\n")
	case e.Tenant:
		b.WriteString("    tenant: {}\n")
	case e.Global:
		b.WriteString("    global: {}\n")
	}
	if e.Watch {
		b.WriteString("\n    list: {\n      order: [{field: \"date_created\"}, {field: \"id\"}]\n")
		b.WriteString("\n      // What a caller may filter by. \"ref\" is naming a row outright,\n")
		b.WriteString("      // and a watch is refused without it: a watch says which rows it\n")
		b.WriteString("      // is about, and a reference is how one is named.\n")
		b.WriteString("      by: [\"ref\"]\n")
		b.WriteString("\n      size: 20\n      max: 100\n    }\n")
		b.WriteString("    watch: {}\n")
	}
	b.WriteString("  };\n}\n")

	return b.String()
}

func count(vs ...bool) int {
	n := 0
	for _, v := range vs {
		if v {
			n++
		}
	}

	return n
}

// snake is a message name as a file name: `RobotArm` is `robot_arm`.
func snake(v string) string {
	b := &strings.Builder{}
	for i, c := range v {
		if unicode.IsUpper(c) {
			if i > 0 {
				b.WriteByte('_')
			}

			b.WriteRune(unicode.ToLower(c))
			continue
		}

		b.WriteRune(c)
	}

	return b.String()
}

// Domains is every domain this schema has taken, for a reader who wants to
// know.
func Domains(l Layout) (map[int]string, error) {
	vs := map[int]string{}

	err := filepath.WalkDir(l.Path(DirProto), func(p string, _ os.DirEntry, err error) error {
		if err != nil || !strings.HasSuffix(p, ".proto") {
			return nil
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		for _, m := range messageAt.FindAllSubmatch(b, -1) {
			name := string(m[1])
			if d := domainAt.FindSubmatch(m[2]); d != nil {
				if n, err := strconv.Atoi(string(d[1])); err == nil {
					vs[n] = name
				}
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return vs, nil
}

// messageAt is one message and its body, which is enough to pair a name with
// the domain declared inside it.
var messageAt = regexp.MustCompile(`(?s)message ([A-Za-z0-9_]+) \{(.*?)\n\}`)

// SortedDomains is [Domains] in order, for printing.
func SortedDomains(vs map[int]string) []int {
	ns := make([]int, 0, len(vs))
	for n := range vs {
		ns = append(ns, n)
	}
	sort.Ints(ns)

	return ns
}
