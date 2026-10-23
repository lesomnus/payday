package pdcli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/pdpb"
)

// What buf says about a payday option written against a newer payday than the
// one the app has pinned: a field of `(payday.entity)`, `(payday.field)` or
// `(payday.app)` the pinned module does not have, or a value of its `Own`.
//
// In the words of the compiler the buf payday pins has, first -- which name
// the enum value and not the field, and say where the field is:
//
//	proto/app/robot.proto:24:3:cannot resolve message field name for `payday.App`
//	proto/app/payday/event.proto:141:10:cannot find `OWN_EVENT` in this scope
//
// and an option it has no extension for at all, as an event message names
// one (payday#151):
//
//	proto/app/machine.proto:10:11:cannot find `payday.event` in this scope
//
// and in those of the one buf had before it, which a buf named by [BufEnv] may
// still be:
//
//	proto/payday/audit.proto:209:5:message app.Audit: option (payday.entity): field own not found
var (
	staleField  = regexp.MustCompile("(?m)^(\\S+?):(\\d+):(\\d+):cannot resolve message field name for `(payday\\.\\w+)`")
	staleValue  = regexp.MustCompile("cannot find `(OWN_\\w+)` in this scope")
	staleExt    = regexp.MustCompile("cannot find `\\.?(payday\\.\\w+)` in this scope")
	staleOption = regexp.MustCompile(`option \((payday\.\w+)\): (?:field (\w+) not found|enum payday\.\w+ has no value named (\w+))`)
)

// whyStale explains that failure, because the message buf gives names the
// symptom and nothing else.
//
// The app's schema is copied in from **this** payday and the option it is
// written against comes from `buf.build/payday/payday`, so the two can be of
// different ages. `pd gen` writes `buf.lock` only when nothing has, which is
// right -- a pin is the app's to move -- and it means an app that has generated
// once stays where it was pinned however far payday travels afterwards.
//
// What comes out of that is a field or a value refused on a file the app never
// wrote, naming something it has never heard of. Nothing in it says the word
// "pin", and the file it points at is regenerated on every run, so the obvious
// readings are all wrong: a corrupt copy, a bad merge, a broken payday. And
// since payday#151 it is the app's own file as well: `past:` is written by the
// app, in `option (payday.app)`, against the payday it read the guide of.
//
// # Only what this payday has
//
// A name the pinned module does not resolve is a stale pin when the payday
// running here has it, and a typo when it does not -- `tennanted:` is not
// solved by `buf dep update`. So each is looked up in the options this binary
// was built with, which are the ones the app's schema is written against, and
// only those it finds are explained. buf's current compiler does not name the
// field, so it is read off the file at the line and the column it points at,
// relative to `work`, where buf was run.
func whyStale(work string, b []byte) string {
	seen := map[string]bool{}
	names := []string{}
	add := func(v string) {
		if !seen[v] {
			seen[v], names = true, append(names, v)
		}
	}

	for _, m := range staleField.FindAllSubmatch(b, -1) {
		line, _ := strconv.Atoi(string(m[2]))
		col, _ := strconv.Atoi(string(m[3]))
		name := wordAt(filepath.Join(work, string(m[1])), line, col)
		if name != "" && has(protoreflect.FullName(m[4]), name) {
			add("`" + name + ":`")
		}
	}
	for _, m := range staleValue.FindAllSubmatch(b, -1) {
		if _, ok := pdpb.Own_value[string(m[1])]; ok {
			add("`" + string(m[1]) + "`")
		}
	}
	for _, m := range staleExt.FindAllSubmatch(b, -1) {
		if _, err := protoregistry.GlobalTypes.FindExtensionByName(protoreflect.FullName(m[1])); err == nil {
			add("`(" + string(m[1]) + ")`")
		}
	}
	for _, m := range staleOption.FindAllSubmatch(b, -1) {
		switch {
		case len(m[2]) > 0:
			x, err := protoregistry.GlobalTypes.FindExtensionByName(protoreflect.FullName(m[1]))
			if err == nil && has(x.TypeDescriptor().Message().FullName(), string(m[2])) {
				add("`" + string(m[2]) + ":`")
			}
		case len(m[3]) > 0:
			if _, ok := pdpb.Own_value[string(m[3])]; ok {
				add("`" + string(m[3]) + "`")
			}
		}
	}
	if len(names) == 0 {
		return ""
	}

	return fmt.Sprintf(
		"\nThe schema says %s, which the payday options this app has pinned do not have. payday's "+
			"entities were copied in from the payday you are running, and what the app declares is "+
			"written against that one, while the options are the `buf.build/payday/payday` buf.lock "+
			"names.\n\n"+
			"    buf dep update\n\n"+
			"`pd gen` writes buf.lock only when nothing has, because a pin is yours to move.",
		strings.Join(names, ", "))
}

// has reports whether the payday this is has a field of that name on that
// message of its options.
func has(message protoreflect.FullName, field string) bool {
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(message)
	if err != nil {
		return false
	}

	m, ok := d.(protoreflect.MessageDescriptor)
	return ok && m.Fields().ByName(protoreflect.Name(field)) != nil
}

var identAt = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)

// wordAt is the identifier at a line and a column of a file, counted from one
// as buf counts them, and empty when there is none to read.
func wordAt(p string, line int, col int) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}

	ls := strings.Split(string(b), "\n")
	if line < 1 || line > len(ls) {
		return ""
	}

	l := ls[line-1]
	if col < 1 || col > len(l) {
		return ""
	}

	return identAt.FindString(l[col-1:])
}
