package pdid

import (
	"errors"
	"fmt"
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
}{
	byEntity: map[string]Domain{},
	byName:   map[string]Domain{},
	names:    map[Domain]string{},
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
	if v, ok := registry.names[d]; ok && v != name {
		panic(fmt.Sprintf("pdid: domain %d: already registered as %q, now %q", d, v, name))
	}
	if v, ok := registry.byName[name]; ok && v != d {
		panic(fmt.Sprintf("pdid: %q: already registered as domain %d, now %d", name, v, d))
	}

	registry.byEntity[entity] = d
	registry.byName[name] = d
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

	if v, ok := registry.names[d]; ok {
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
		vs[d] = n
	}

	return vs
}
