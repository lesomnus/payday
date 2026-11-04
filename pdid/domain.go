package pdid

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Domain says what kind of thing an identifier names.
//
// It is a number the schema declares and never a name, because it has to fit in
// one byte of an identifier. The name is what a person writes -- see
// [Register] -- and the two are declared together so that they cannot drift.
type Domain uint8

// Unknown is the domain of an identifier that is not one of ours, and the one
// number nothing may be registered as. An entity that forgot to declare a
// domain would otherwise be every entity that forgot.
const Unknown Domain = 0

// RegisterTenant records which domain is the tenant's, for the runtime that has
// to recognise one and cannot name its Go type.
//
// Generated code calls it from the same init as [Register], out of the entity
// that declared `own: OWN_TENANT`. It is a separate call rather than a name to
// look up because the name is not payday's to know: an app may put payday's
// entities in its own proto package, and then the tenant is `fleet.Tenant` or
// whatever else it chose. What does not move is which entity is the wall.
//
// The number itself is still the schema's to declare.
func RegisterTenant(d Domain) {
	if d == Unknown {
		panic("pdid: the tenant cannot be domain 0")
	}

	registry.Lock()
	defer registry.Unlock()

	if v := registry.tenant; v != Unknown && v != d {
		panic(fmt.Sprintf("pdid: the tenant is already domain %d, now %d", v, d))
	}

	registry.tenant = d
}

// TenantDomain is what generated code registered as the tenant, and false for a
// process that linked none -- which is one that has no schema at all, since
// payday ships the entity and every app copies it in.
func TenantDomain() (Domain, bool) {
	registry.RLock()
	defer registry.RUnlock()

	return registry.tenant, registry.tenant != Unknown
}

var (
	// ErrNotAnId is what bytes that are a UUID but not one of ours are.
	ErrNotAnId = errors.New("not a payday identifier")

	// ErrDomain is what an identifier of the wrong kind is. It is a request
	// being wrong and not the app, so it is answered with rather than logged.
	ErrDomain = errors.New("identifier names the wrong kind of thing")
)

// registry holds what generated code said about the schema. It is written at
// init and read for the life of the process, but the lock is kept anyway: a
// test that registers something is a write after the reads have started, and a
// data race there is a flake nobody enjoys finding.
var registry = struct {
	sync.RWMutex

	// byEntity maps a message's full name -- "app.Robot" -- to its domain.
	byEntity map[string]Domain
	// byName maps the name a person writes -- "robot" -- to its domain.
	byName map[string]Domain
	// ambiguous is a name two apps in this process write for two different
	// things, so it names nothing here. See [Register].
	ambiguous map[string]bool
	// names maps a domain back to that name.
	names map[Domain]string
	// shared is a domain two apps in this process numbered differently, so it
	// has no one name. See [Register].
	shared map[Domain]bool

	// formerEntity and formerName are what an earlier build called a domain:
	// a message's full name then, and the word a person wrote it as. Read only
	// where what is registered now says nothing, and never by [Domain.String]
	// or [Domains]. See [Formerly].
	formerEntity map[string]Domain
	formerName   map[string]Domain
	// formerAmbiguous is a former word two apps in this process mean two
	// domains by, so it names nothing here, as [Register]'s words do not.
	formerAmbiguous map[string]bool

	// tenant is the domain of whichever entity declared `own: OWN_TENANT`.
	tenant Domain
}{
	byEntity:        map[string]Domain{},
	byName:          map[string]Domain{},
	ambiguous:       map[string]bool{},
	names:           map[Domain]string{},
	shared:          map[Domain]bool{},
	formerEntity:    map[string]Domain{},
	formerName:      map[string]Domain{},
	formerAmbiguous: map[string]bool{},
}

// Name is what a person writes an entity as: the word its schema declared, or
// the message name in kebab-case when it declared none -- `Robot` is `robot`
// and `SshKey` is `ssh-key`.
//
// It is the `#word` of a slug, the group a command sits under, and the third
// argument [Register] is given, and those have to be one word rather than three
// derivations that agree by accident. They did not: the generator read the
// declaration and kebab-cased what it found, while `pdcmd` lowercased the
// message name and never looked at the option at all -- so an app that wrote
// `name: "key"` was still typing `sshkey`, and one that wrote nothing had a
// `#ssh-key` in its slugs and a `sshkey` on its command line.
//
// Here rather than beside the option because the option's generated Go is
// generated in whole, and because this package is where the name already means
// something.
func Name(declared string, message string) string {
	if declared != "" {
		return declared
	}

	var b strings.Builder
	for i, r := range message {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}

			b.WriteRune(r - 'A' + 'a')
			continue
		}

		b.WriteRune(r)
	}

	return b.String()
}

// Register records what the schema declared: the message `entity` is of domain
// `d`, and a person writes that domain as `name`.
//
// Generated code calls it from an init, which is why it panics rather than
// answering with an error -- a number used twice, or a domain of [Unknown], is
// a schema that should not have generated and there is nobody to hand an error
// to at that point.
//
// Registering the same entity twice with the same values is allowed, so that a
// package linked in twice under different paths does not bring the process
// down.
func Register(entity string, d Domain, name string) {
	if d == Unknown {
		panic(fmt.Sprintf("pdid: %s: domain 0 is what an unregistered identifier reads as, so nothing may hold it", entity))
	}
	if entity == "" || name == "" {
		panic(fmt.Sprintf("pdid: domain %d: an entity and a name are both required", d))
	}

	registry.Lock()
	defer registry.Unlock()

	if v, ok := registry.byEntity[entity]; ok && v != d {
		panic(fmt.Sprintf("pdid: %s: already registered as domain %d, now %d", entity, v, d))
	}
	registry.byEntity[entity] = d

	// The word a person writes, and this is the other thing two apps in one
	// process can disagree about: the note below on the number says a proto
	// package keeps this unique, and it does not -- a `Site` is an ordinary
	// thing to have, and two apps that both have one both write `site`.
	//
	// Refusing it took the process down before `main`, over a word. What the
	// word resolves is a slug somebody typed, `#site:...`, and in a process
	// where two apps mean two things by it there is no right answer to give:
	// so the name stops answering, [DomainOf] says it knows no such word, and
	// what does not stop is [Lookup], keyed by the full name, which is how
	// anything generated finds its way. An app that is the only one in its
	// process, which is nearly every one, never sees this.
	switch v, ok := registry.byName[name]; {
	case registry.ambiguous[name]:
	case ok && v != d:
		delete(registry.byName, name)
		registry.ambiguous[name] = true
	default:
		registry.byName[name] = d
	}

	// The number, back to a name -- and this is the one that two apps in one
	// process disagree about.
	//
	// A domain number is the **app's** to declare. payday's own are the same
	// everywhere by construction, but 7 is whatever each app numbered first:
	// one app's asset, another's site. So a process holding both has no single
	// answer to "what is 7 called", and it used to panic on the second app to
	// register -- before `main`, taking the process down over a display name.
	//
	// Nothing functional was ever at stake here. What resolves a domain is
	// [Lookup], keyed by the message's full name, and [DomainOf], keyed by the
	// name a person writes; both of those stay unique because a proto package
	// is an app's own. This map is read by [Domain.String] and by [Domains],
	// which are a label and a diagnostic.
	//
	// So a disagreement is recorded rather than refused, and the number stops
	// having a name: `domain(7)` is true in a process where two apps mean two
	// things by it, and a name would be true in only one of them.
	if v, ok := registry.names[d]; ok && v != name {
		registry.shared[d] = true

		return
	}

	registry.names[d] = name
}

// Formerly records what an earlier build called domain `d`: the full name its
// message had then, `entity`, and the word a person wrote it as, `name`
// (`(payday.entity).formerly`). Generated code calls it from the same init as
// [Register], once for each name the schema says the entity had.
//
// # Why a name that is gone is kept
//
// Because an earlier build wrote it down where nothing rewrites it. The
// trail's archive labels each chunk with the word of the kind its rows are
// about, and a chunk is written once and checked as it was written: one
// labelled with a word this build answers nothing to is of no kind -- kept or
// destroyed by the pass about everything else rather than by its own kind's
// window, and not read as that kind's when the rows are checked against the
// trail. A configuration file names a kind by its word too, and so does the
// message a trail's patch names. Answered here, each is read as the domain it
// was, without being written again.
//
// # What it does not change
//
// What a domain is called now: [Domain.String], [Domains] and the word a new
// chunk is labelled with are the name [Register] was given, and a former name
// never shadows one -- [Lookup] and [DomainOf] answer what is registered now
// first, whatever order the inits ran in. So a word one entity had and another
// has now is the other's, which is why `pd gen` refuses a schema that gives a
// name somebody had to somebody else, as it refuses a domain number used twice.
//
// It panics for what [Register] would: a domain of [Unknown], an empty name,
// and a full name that is said to have been two domains.
func Formerly(entity string, d Domain, name string) {
	if d == Unknown {
		panic(fmt.Sprintf("pdid: %s: domain 0 is what an unregistered identifier reads as, so nothing held it", entity))
	}
	if entity == "" || name == "" {
		panic(fmt.Sprintf("pdid: domain %d: what it was called before is an entity and a name", d))
	}

	registry.Lock()
	defer registry.Unlock()

	if v, ok := registry.formerEntity[entity]; ok && v != d {
		panic(fmt.Sprintf("pdid: %s: said to have been domain %d, now %d", entity, v, d))
	}
	registry.formerEntity[entity] = d

	// Two apps in one process that each had a word for different things: it
	// stops answering, as a word two apps write now does.
	switch v, ok := registry.formerName[name]; {
	case registry.formerAmbiguous[name]:
	case ok && v != d:
		delete(registry.formerName, name)
		registry.formerAmbiguous[name] = true
	default:
		registry.formerName[name] = d
	}
}

// Lookup answers with the domain of the message with the given full name: the
// one registered now, or, for a name an earlier build gave it, what
// [Formerly] says it was.
func Lookup(entity string) (Domain, bool) {
	registry.RLock()
	defer registry.RUnlock()

	if d, ok := registry.byEntity[entity]; ok {
		return d, true
	}

	d, ok := registry.formerEntity[entity]
	return d, ok
}

// DomainOf answers with the domain a person writes as `name`, which is how the
// `#domain` of a slug is read -- and with the one a word an earlier build
// wrote it as names ([Formerly]), where no domain is written so now.
func DomainOf(name string) (Domain, bool) {
	registry.RLock()
	defer registry.RUnlock()

	if d, ok := registry.byName[name]; ok {
		return d, true
	}
	if registry.ambiguous[name] {
		// Somebody's word now, in two apps: not a former word's to answer.
		return Unknown, false
	}

	d, ok := registry.formerName[name]
	return d, ok
}

// String is the name the schema gave this domain, or its number if nothing
// registered it -- which is what an identifier from somewhere else looks like.
func (d Domain) String() string {
	registry.RLock()
	defer registry.RUnlock()

	if v, ok := registry.names[d]; ok && !registry.shared[d] {
		return v
	}
	if d == Unknown {
		return "unknown"
	}

	return fmt.Sprintf("domain(%d)", uint8(d))
}

// Domains lists what has been registered, for a diagnostic that wants to say
// what the app does know about.
func Domains() map[Domain]string {
	registry.RLock()
	defer registry.RUnlock()

	vs := make(map[Domain]string, len(registry.names))
	for d, n := range registry.names {
		if registry.shared[d] {
			// Two apps mean two things by this number, so there is no name to
			// list. See [Register].
			continue
		}

		vs[d] = n
	}

	return vs
}
