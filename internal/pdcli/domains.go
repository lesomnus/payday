package pdcli

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

// AppDomains is what an app declared of the domains of payday's `Archived` and
// `Access` (payday#163): the numbers it gives them, because its own entities
// already hold the ones payday does.
//
//	option (payday.app) = {domains: {archived: 240, access: 241}};
//
// It is read off the schema before anything is compiled, for the reason
// [Past] is: it decides what `pd gen` writes for buf to compile -- the two
// copies, numbered. `pdgen.Read` reads the same option again off the compiled
// schema, and holds the copies to it. A number is zero where the declaration
// leaves that entity at the one payday gives it.
type AppDomains struct {
	Archived uint32
	Access   uint32

	// At is the file that declared it, as a person would type it.
	At string
}

// Domain is the number the declaration gives the entity of that marker, and
// false for one it leaves where payday puts it.
func (d *AppDomains) Domain(own pdpb.Own) (uint32, bool) {
	var n uint32
	switch {
	case d == nil:
	case own == pdpb.Own_OWN_ARCHIVED:
		n = d.Archived
	case own == pdpb.Own_OWN_ACCESS:
		n = d.Access
	}

	return n, n != 0
}

// numberedFiles is payday's entities an app may number, as they ship: the file
// each is copied from, the message it declares, and its marker -- the files of
// `pdgen.Numbered`, which says which markers those are.
var numberedFiles = []pastFile{
	{"archived.proto", "Archived", pdpb.Own_OWN_ARCHIVED},
	{"access.proto", "Access", pdpb.Own_OWN_ACCESS},
}

// readDomains is the domains this app's own files give payday's Archived and
// Access, and nil when none does.
//
// Parsed and not compiled, for the reason [readPast] parses: the copies it
// numbers are what a compile would need. And held to the rule `pdgen.Read`
// holds the compiled schema to, `pdgen.CheckDomains`, with the past this app
// declares -- [Layout.Past], read first -- since neither may take its numbers.
// Whether an entity of the app's own holds one is the compiled schema's to
// say, as it is of the past's.
func readDomains(l Layout) (*AppDomains, error) {
	var past *pdpb.Past
	if l.Past != nil {
		past = pdpb.Past_builder{Event: l.Past.Event, Fact: l.Past.Fact}.Build()
	}

	var v *AppDomains
	err := l.eachOwn(func(p string, b []byte) error {
		if !protoAppAt.Match(b) {
			return nil
		}

		app, err := appOf(l.rel(p), b)
		if err != nil {
			return err
		}
		if !app.HasDomains() {
			return nil
		}
		if v != nil {
			return fmt.Errorf(
				"%s and %s both give payday's entities domains; it is said once, in the file that says "+
					"the package is the app's", v.At, l.rel(p))
		}

		d := app.GetDomains()
		if err := pdgen.CheckDomains(d, past, l.Notify.proto()); err != nil {
			return fmt.Errorf("%s: %w", l.rel(p), err)
		}

		v = &AppDomains{Archived: d.GetArchived(), Access: d.GetAccess(), At: l.rel(p)}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return v, nil
}

// unknownField is the field prototext names when the message it reads into has
// none of that name.
var unknownField = regexp.MustCompile(`unknown field: (\w+)`)

// keptBy is the refusal of an `option (payday.app)` that names one of payday's
// entities an app does not number in `domains:` -- `owed:`, `row_seal:` -- and
// "" when the field prototext refused is no such name, for which its own words
// are the better answer.
//
// Said rather than left to prototext because it is no misspelling: the app
// meant payday's entity, and the sentence it needs is that this one keeps its
// number (payday#163) and what to do instead. A name is read as payday's marker
// is spelt, `row_seal`, and as its message is, `rowseal`; the past's two are
// pointed at `past:`, which is where they are numbered.
func keptBy(err error) string {
	m := unknownField.FindStringSubmatch(err.Error())
	if m == nil {
		return ""
	}

	features, oerr := schema.Features()
	if oerr != nil {
		return ""
	}

	for n, v := range pdpb.Own_name {
		own := pdpb.Own(n)
		word := strings.ToLower(strings.TrimPrefix(v, "OWN_"))
		if own == pdpb.Own_OWN_UNSPECIFIED || (m[1] != word && m[1] != strings.ReplaceAll(word, "_", "")) {
			continue
		}

		message := ""
		for _, part := range strings.Split(word, "_") {
			message += strings.ToUpper(part[:1]) + part[1:]
		}

		if features[own] == schema.DirEntitle {
			return fmt.Sprintf("names %s, and the copy of entitlements is numbered by `entitlements:` -- `option (payday.app) = {entitlements: {domain: 43}}`", m[1])
		}
		if features[own] == schema.DirNotify {
			return fmt.Sprintf("names %s, and notification records are numbered by `notify:` -- `option (payday.app) = {notify: {notification: 40, receipt: 41}}`", m[1])
		}
		if features[own] == schema.DirPast {
			return fmt.Sprintf(
				"names %s, and the past's %s is numbered by `past:`, beside its other one -- "+
					"`option (payday.app) = {past: {event: 17, fact: 16}}`", m[1], message)
		}
		if slices.ContainsFunc(pdgen.Numbered, func(v pdgen.NumberedEntity) bool { return v.Own == own }) {
			// Known to `domains:`, so it was said outside it.
			return fmt.Sprintf(
				"names %s outside `domains:`, which is where payday's %s is numbered -- "+
					"`option (payday.app) = {domains: {%s: 240}}`", m[1], message, word)
		}

		was, serr := pdgen.Shipped(own)
		if serr != nil {
			return ""
		}

		return fmt.Sprintf(
			"names %s, which is payday's %s, and it keeps %d in every app: an app numbers payday's "+
				"Archived and Access, whose numbers apps had spent before payday took them, and no other "+
				"of payday's entities. An entity of the app's own at %d moves to a number of its own instead",
			m[1], message, was, was)
	}

	return ""
}

// numbering is what is done to payday's file `name` before it is copied: for
// Archived or Access in an app that numbers it, the app's number written over
// payday's. Nil for every other file, and for one the app leaves where payday
// puts it -- which is copied as it ships.
//
// Before the overlay is merged, as the past's number is and for its reason:
// the line is payday's, in payday's file, and the merge reprints the file in a
// shape payday does not control.
func (g Gen) numbering(name string) (func([]byte) ([]byte, error), error) {
	i := slices.IndexFunc(numberedFiles, func(v pastFile) bool { return v.Name == name })
	if i < 0 {
		return nil, nil
	}

	f := numberedFiles[i]
	n, ok := g.Layout.Domains.Domain(f.Own)
	if !ok {
		return nil, nil
	}

	was, err := pdgen.Shipped(f.Own)
	if err != nil {
		return nil, err
	}

	g.say("  %s, domain %d, for %s", f.Message, n, g.Layout.Domains.At)

	return func(b []byte) ([]byte, error) { return numbered(b, was, n) }, nil
}

// domainLine is the `domain:` a file of payday's says in its `(payday.entity)`,
// one per file.
var domainLine = regexp.MustCompile(`(?m)^(\s*)domain: (\d+)$`)

// numbered is one of payday's files with the app's number `to` where it said
// `from`: the `domain: 0` a template of the past says in place of one, or the
// number payday gives its Archived or Access.
//
// Exactly one line, saying `from`, or the file is not one this was written
// for: a second would number something nobody meant, none would leave the
// copy at a number the declaration does not stand behind -- which generation
// refuses, in a file the app did not write -- and another number would be a
// template moved without this.
func numbered(b []byte, from, to uint32) ([]byte, error) {
	m := domainLine.FindAllSubmatch(b, -1)
	switch {
	case len(m) != 1:
		return nil, fmt.Errorf("payday's file says `domain:` %d times, and is numbered by replacing the one", len(m))
	case string(m[0][2]) != strconv.FormatUint(uint64(from), 10):
		return nil, fmt.Errorf("payday's file says `domain: %s`, and is numbered by replacing `domain: %d`", m[0][2], from)
	}

	return domainLine.ReplaceAll(b, []byte(fmt.Sprintf("${1}domain: %d", to))), nil
}
