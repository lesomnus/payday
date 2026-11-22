package watchnats

import (
	"slices"

	"github.com/lesomnus/payday/watch"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A change has one canonical subject per application route. Publishing separate
// service/key/topic aliases would multiply traffic even with no overlapping
// interest. Unknown dimensions use a reserved token; native subscriptions also
// ask for it so incomplete metadata always errs toward delivery.
func (b *Broker) subjects(c watch.Change) []string {
	service, key := "u", "u"
	if c.By != "" {
		service = token(watch.ServiceOf(c.By))
	}
	if !c.Key.IsZero() {
		key = token(c.Key.String())
	}
	routes := []string{"u"}
	if c.Routes != nil {
		routes = []string{"n"}
		if len(c.Routes) > 0 {
			routes = nil
			for _, r := range c.Routes {
				routes = append(routes, token(string(r)))
			}
		}
	}
	routes = slices.Compact(slices.Sorted(slices.Values(routes)))
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, b.prefix+"."+service+"."+key+"."+r)
	}
	return out
}
func token(v string) string { return string(watch.RouteKey("payday.watch.nats", v)) }

const maxBindings = 8192

func (b *Broker) patterns(s watch.Interest) ([]string, error) {
	services, keys, routes := []string{"*"}, []string{"*"}, []string{"*"}
	if s.Service != "" {
		services = []string{token(s.Service), "u"}
	}
	if len(s.Keys) > 0 {
		keys = []string{"u"}
		for _, k := range s.Keys {
			keys = append(keys, token(k.String()))
		}
	}
	if len(s.Routes) > 0 {
		routes = []string{"u"}
		for _, r := range s.Routes {
			routes = append(routes, token(string(r)))
		}
	}
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	routes = slices.Compact(slices.Sorted(slices.Values(routes)))
	if len(keys) > maxBindings/len(services) || len(routes) > maxBindings/(len(services)*len(keys)) {
		return nil, status.Errorf(codes.ResourceExhausted, "watchnats: interest exceeds %d native subscriptions", maxBindings)
	}
	out := make([]string, 0, len(services)*len(keys)*len(routes))
	for _, a := range services {
		for _, k := range keys {
			for _, r := range routes {
				out = append(out, b.prefix+"."+a+"."+k+"."+r)
			}
		}
	}
	return out, nil
}
