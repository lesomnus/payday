package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sync"

	"github.com/lesomnus/payday/pdid"
)

// Route is an opaque, server-authored application interest. It is not a public
// notification topic or a broker subject, and possessing one grants no access.
type Route string

// RouteKey keeps namespaces, identifiers and arbitrary names distinct without
// imposing a broker's separators or wildcards on an application's vocabulary.
// Include the topic's kind as well as its value when constructing a route.
func RouteKey(namespace string, parts ...string) Route {
	b, _ := json.Marshal(append([]string{namespace}, parts...))
	sum := sha256.Sum256(b)
	return Route(hex.EncodeToString(sum[:]))
}

// Interest is a conservative routing hint, never a read policy. Dimensions are
// intersected; values within a slice are alternatives. An empty dimension is
// unrestricted. Service is the full service prefix returned by ServiceOf.
// Unknown publication metadata must overdeliver rather than lose a change.
type Interest struct {
	Service string
	Keys    []pdid.Id
	Routes  []Route
}

func (s Interest) clone() Interest {
	s.Keys = slices.Clone(s.Keys)
	s.Routes = slices.Clone(s.Routes)
	return s
}

// Matches says whether any change may concern this interest. Event.Tenant is
// deliberately absent: it names the caller's tenant, not a changed row's owner.
func (s Interest) Matches(v Event) bool {
	if len(v.Changes) == 0 {
		return true // A legacy control event whose scope was not described.
	}
	for _, c := range v.Changes {
		if s.Match(c) {
			return true
		}
	}
	return false
}

// Match is Matches for a single change. It is also the check an adapter uses
// after a deliberately broader native subscription.
func (s Interest) Match(c Change) bool {
	if s.Service != "" && c.By != "" && ServiceOf(c.By) != s.Service {
		return false
	}
	if len(s.Keys) != 0 && !c.Key.IsZero() && !slices.Contains(s.Keys, c.Key) {
		return false
	}
	if len(s.Routes) == 0 || c.Routes == nil {
		return true
	}
	for _, route := range c.Routes {
		if slices.Contains(s.Routes, route) {
			return true
		}
	}
	return false
}

// ScopedBroker optionally carries interests to the transport. Open returns
// only after the subscription is installed, or returns an error. A lost
// connection or backlog closes affected subscriptions; reconnecting the broker
// must not silently continue streams which may have missed events.
//
// Adapters should use Fanout to share local event interpretation and bound slow
// subscribers. Broker remains unchanged for existing application adapters.
type ScopedBroker interface {
	Open(context.Context, Interest) (*Subscription, error)
}

// Subscription is installed before Open returns. Close is idempotent and
// releases this listener's interest; its owner must call it on every exit.
type Subscription struct {
	Events <-chan Event
	stop   func()
}

// NewSubscription lets an adapter pair an event channel with its interest
// release. The adapter owns closing the channel, including on connection loss.
func NewSubscription(events <-chan Event, stop func()) *Subscription {
	return &Subscription{Events: events, stop: sync.OnceFunc(stop)}
}

func (s *Subscription) Close() { s.stop() }

// Shared constructs a value once for all local copies of this event. A key
// identifies a resolver, not a user. Only context-independent interpretations
// belong here: never read decisions, field projections or reader state. A
// promise can be shared so each caller can independently stop waiting for I/O.
// Legacy adapters that do not prepare an event simply compute the value again.
// Keys must be comparable, as with sync.Map.
func (v Event) Shared(key any, create func() any) any {
	if v.shared == nil {
		return create()
	}
	f, _ := v.shared.LoadOrStore(key, sync.OnceValue(create))
	return f.(func() any)()
}
