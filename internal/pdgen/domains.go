package pdgen

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

// AppDomains is what an app declared of the domains of payday's `Archived` and
// `Access` (payday#163): the numbers it gives them, because its own entities
// already hold the ones payday does.
//
//	option (payday.app) = {domains: {archived: 240, access: 241}};
//
// It is nil for an app that declared none, which is every app whose own
// entities never held 5 or 6, and a number is zero where the declaration
// leaves that entity at the one payday gives it.
type AppDomains struct {
	Archived uint8
	Access   uint8

	// At is the file that declared it, as the schema names it, for a refusal to
	// point at.
	At string
}

// Domain is the number the declaration gives the entity of that marker, and
// false for one it does not number: one it leaves where payday puts it, and
// one that is not among [Numbered].
func (d *AppDomains) Domain(own pdpb.Own) (uint8, bool) {
	var n uint8
	switch {
	case d == nil:
	case own == pdpb.Own_OWN_ARCHIVED:
		n = d.Archived
	case own == pdpb.Own_OWN_ACCESS:
		n = d.Access
	}

	return n, n != 0
}

// Numbered is payday's entities an app may number, by the marker each carries,
// with the field of `payday.Domains` that numbers it and the message it is.
//
// Two, and written out rather than read off anything: they are the two payday
// gave numbers that apps had already spent (payday#163), and a table payday
// adds later is not one of them until somebody decides it is. Every other
// entity of payday's keeps the number payday gives it -- which is what lets a
// tenant's identifier name a tenant in every payday app.
var Numbered = []NumberedEntity{
	{pdpb.Own_OWN_ARCHIVED, "archived", "Archived"},
	{pdpb.Own_OWN_ACCESS, "access", "Access"},
}

// NumberedEntity is one of [Numbered].
type NumberedEntity struct {
	Own     pdpb.Own
	Field   string
	Message string
}

// numbered answers whether the entity of that marker is one an app may number.
func numbered(own pdpb.Own) bool {
	for _, v := range Numbered {
		if v.Own == own {
			return true
		}
	}

	return false
}

// Shipped is the domain payday gives its entity of that marker, as the file it
// ships says: where an entity of [Numbered] is in an app that does not number
// it. Zero for a marker payday ships nothing of, and for the past's, whose
// number is always the app's.
func Shipped(own pdpb.Own) (uint32, error) {
	vs, err := schema.OwnedOptions()
	if err != nil {
		return 0, err
	}

	return vs[own].Entity.GetDomain(), nil
}

// CheckDomains refuses numbers payday's Archived and Access cannot be given:
// one outside the app's range, one number for both, and one the app's past
// takes. `past` is the past the app declares, and nil for none.
//
// It is the one rule, asked by the two readers [CheckPastDomains] is asked by,
// for its reason: `pd gen` writes the copies at these numbers before anything
// is compiled, so a wrong one is refused before it is in a file, and [Read]
// holds the compiled schema to it. Whether another entity of the app's holds
// one is not here either: only the whole schema can say, and [Read] says it.
//
// Zero is not a number this refuses. It is what a field says when nothing set
// it, and what it means here is the number payday gives the entity.
func CheckDomains(d *pdpb.Domains, past *pdpb.Past, notifications ...*pdpb.Notify) error {
	for _, n := range notifications {
		if err := CheckNotify(n, past, d); err != nil {
			return err
		}
	}
	for _, v := range []struct {
		field   string
		message string
		n       uint32
		own     pdpb.Own
	}{
		{"archived", "Archived", d.GetArchived(), pdpb.Own_OWN_ARCHIVED},
		{"access", "Access", d.GetAccess(), pdpb.Own_OWN_ACCESS},
	} {
		switch n := v.n; {
		case n == 0:
			continue

		case n > 255:
			return fmt.Errorf("domains: %s: %d does not fit in the one byte an identifier carries; it must be %d..%d",
				v.field, n, AppDomainMin, AppDomainMax)

		case n < AppDomainMin || n > AppDomainMax:
			was, err := Shipped(v.own)
			if err != nil {
				return err
			}

			return fmt.Errorf(
				"domains: %s: %d is payday's: it numbers what every app has from 1 up and from 255 down, "+
					"and keeps 1..%d and %d..255 for that. Its %s is %d where nothing numbers it -- say "+
					"nothing to keep it there -- and an app that numbers it numbers it like its own "+
					"entities, %d..%d",
				v.field, n, AppDomainMin-1, AppDomainMax+1, v.message, was, AppDomainMin, AppDomainMax)
		}

		for _, w := range []struct {
			message string
			n       uint32
		}{{"Event", past.GetEvent()}, {"Fact", past.GetFact()}} {
			if w.n != v.n {
				continue
			}

			return fmt.Errorf(
				"domains: %s: %d is what `past:` gives the past's %s, and a domain is one entity's: an "+
					"identifier that could be either is one that says nothing. Give %s a number nothing holds",
				v.field, v.n, w.message, v.message)
		}
	}
	if a := d.GetArchived(); a != 0 && a == d.GetAccess() {
		return fmt.Errorf(
			"domains: says %d for both `archived:` and `access:`, and a domain is one entity's: an "+
				"identifier that could be either is one that says nothing", a)
	}

	return nil
}

// readDomains is the domains the schema's own files give payday's Archived and
// Access, and nil when none does. `past` is the past the same files declare,
// whose numbers neither may take.
//
// Read off the compiled option, for the reason [readPast] is: what a
// generation is held to is the schema it is handed, and a copy numbered by a
// declaration that has since moved is what [Schema.checkDomains] is for.
func readDomains(files []*protogen.File, past *Past, notify *Notify) (*AppDomains, error) {
	var held *pdpb.Past
	if past != nil {
		held = pdpb.Past_builder{Event: uint32(past.Event), Fact: uint32(past.Fact)}.Build()
	}

	var v *AppDomains
	for _, f := range files {
		if !f.Generate {
			continue
		}

		a, _ := proto.GetExtension(f.Desc.Options(), pdpb.E_App).(*pdpb.App)
		if !a.HasDomains() {
			continue
		}
		if v != nil {
			// For the reason a past is said once: the copies can only be
			// numbered by one of them.
			return nil, fmt.Errorf(
				"%s and %s both give payday's entities domains; it is said once, in the file that says "+
					"the package is the app's", v.At, f.Desc.Path())
		}

		d := a.GetDomains()
		if err := CheckDomains(d, held, notifyProto(notify)); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Desc.Path(), err)
		}

		v = &AppDomains{Archived: uint8(d.GetArchived()), Access: uint8(d.GetAccess()), At: f.Desc.Path()}
	}

	return v, nil
}

// checkDomains refuses payday's Archived or Access at a number the schema does
// not stand behind, and another entity at the number either of them holds.
//
// The first is a copy `pd gen` did not write. It writes the two at the numbers
// the app declares, or at payday's where it declares none, on every run -- so
// a copy that says otherwise is buf run by hand over a tree whose declaration
// moved, or an edited copy: a table numbered by nothing the schema says.
//
// The second is the collision payday#163 is about, and it is said as the
// declaration's rather than as two entities', since the declaration is where
// it is mended. An app whose own entity holds the number payday gives one of
// them is told it may give payday's another, which is what it is for; and one
// whose declaration gave a number another entity holds is told which line
// to change.
//
// It runs before the rest of the checks, for the reason [Schema.checkPast]
// does.
func (s *Schema) checkDomains() error {
	// payday's at the number another entity holds where the app numbers it
	// nothing: said together, so that an app holding both is given one line
	// that moves both rather than a second refusal after the first is mended.
	clashes := []string{}
	moving := map[pdpb.Own]bool{}

	for _, n := range Numbered {
		v := s.Own(n.Own)
		if v == nil {
			// A schema without it, which only a test writes: [CheckOwn]
			// refuses one in an app.
			continue
		}

		was, err := Shipped(n.Own)
		if err != nil {
			return err
		}

		want, declared := s.Domains.Domain(n.Own)
		if !declared {
			want = uint8(was)
		}

		switch {
		case v.Domain == want:
		case declared:
			return fmt.Errorf(
				"%s says domain %d, and %s declares %d for it. The copy is numbered from the "+
					"declaration by `pd gen`, so one that says otherwise was not written by it: "+
					"run `pd gen`, which writes it again",
				v.FullName(), v.Domain, s.Domains.At, want)
		default:
			return fmt.Errorf(
				"%s says domain %d, and nothing declares a number for it, which leaves it at payday's %d. "+
					"`pd gen` writes the copy at the number the app declares, so this one was numbered by a "+
					"declaration that has since gone -- and a domain is chosen once. Declare it again if a "+
					"row of it was ever written:\n\n"+
					"    option (payday.app) = {domains: {%s: %d}};\n\n"+
					"or run `pd gen`, which writes it at %d",
				v.FullName(), v.Domain, was, n.Field, v.Domain, was)
		}

		for _, u := range s.Entities {
			if u == v || u.Domain != want {
				continue
			}

			if declared {
				return fmt.Errorf(
					"%s declares domain %d, and %s gives it to payday's %s; a domain is one entity's, so "+
						"an old identifier would start lying about what it named. Give %s a number nothing holds",
					u.FullName(), want, s.Domains.At, n.Message, n.Message)
			}

			clashes = append(clashes, fmt.Sprintf("%s declares domain %d, which payday gives its %s", u.FullName(), want, n.Message))
			moving[n.Own] = true
		}
	}
	if len(clashes) == 0 {
		return nil
	}

	where := " where an app gives it no other"
	if len(clashes) > 1 {
		where = ", where an app gives them no other"
	}

	return fmt.Errorf(
		"%s%s. A domain is one entity's, so an old identifier would start lying about what it named. "+
			"An app whose own entity holds one of payday's numbers keeps it, and gives payday's entity "+
			"a number of its own instead, on the file that says the package is the app's:\n\n"+
			"    option (payday.app) = {domains: {%s}};",
		strings.Join(clashes, ", and "), where, s.declaring(moving))
}

// declaring is the inside of the `domains:` a refusal writes out for an app to
// say: what it declares now, and a number nothing holds for each entity in
// `moving`, which it does not.
func (s *Schema) declaring(moving map[pdpb.Own]bool) string {
	taken := map[int]bool{}
	for _, v := range s.Entities {
		taken[int(v.Domain)] = true
	}

	vs := []string{}
	for _, n := range Numbered {
		d, ok := s.Domains.Domain(n.Own)
		switch {
		case moving[n.Own]:
			d := freeOf(taken)
			taken[d] = true
			vs = append(vs, fmt.Sprintf("%s: %d", n.Field, d))
		case ok:
			vs = append(vs, fmt.Sprintf("%s: %d", n.Field, d))
		}
	}

	return strings.Join(vs, ", ")
}

// free is the first number of the app's that no entity holds, for a refusal
// that suggests one -- which the app then reads before it writes.
func (s *Schema) free() int {
	taken := map[int]bool{}
	for _, v := range s.Entities {
		taken[int(v.Domain)] = true
	}

	return freeOf(taken)
}

// freeOf is the first number of the app's not in `taken`, and the first of
// them when every one is.
func freeOf(taken map[int]bool) int {
	for n := AppDomainMin; n <= AppDomainMax; n++ {
		if !taken[n] {
			return n
		}
	}

	return AppDomainMin
}
