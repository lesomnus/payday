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
	// names maps a domain back to that name.
	names map[Domain]string
	// shared is a domain two apps in this process numbered differently, so it
	// has no one name. See [Register].
	shared map[Domain]bool

	// tenant is the domain of whichever entity declared `own: OWN_TENANT`.
	tenant Domain
}{
	byEntity: map[string]Domain{},
	byName:   map[string]Domain{},
	names:    map[Domain]string{},
	shared:   map[Domain]bool{},
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
	if v, ok := registry.byName[name]; ok && v != d {
		panic(fmt.Sprintf("pdid: %q: already registered as domain %d, now %d", name, v, d))
	}

	registry.byEntity[entity] = d
	registry.byName[name] = d

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

// Lookup answers with the domain of the message with the given full name.
func Lookup(entity string) (Domain, bool) {
	registry.RLock()
	defer registry.RUnlock()

	d, ok := registry.byEntity[entity]
	return d, ok
}

// DomainOf answers with the domain a person writes as `name`, which is how the
// `#domain` of a slug is read.
func DomainOf(name string) (Domain, bool) {
	registry.RLock()
	defer registry.RUnlock()

	d, ok := registry.byName[name]
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
