// Package wire carries public watch metadata without requests, responses or patches.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/watch"
	"google.golang.org/protobuf/proto"
)

const MaxWire = 32 << 20

func Encode(v watch.Event) ([]byte, error) {
	w := Envelope{Method: v.Method}
	if v.Actor != [16]byte{} {
		w.Actor = v.Actor.String()
	}
	if v.Tenant != [16]byte{} {
		w.Tenant = v.Tenant.String()
	}
	budget := len(v.Method)
	for _, c := range v.Changes {
		budget += len(c.Method) + len(c.By) + 128
		budget += (len(c.Signal)*4 + 2) / 3
		for _, route := range c.Routes {
			budget += len(route)*6 + 4
		}
		if c.Resource != nil {
			budget += (proto.Size(c.Resource)*4 + 2) / 3
		}
		if budget > MaxWire {
			return nil, errors.New("watch: publication exceeds payload limit")
		}
		item := Change{By: c.By, Key: c.Key.String(), Method: c.Method, Signal: c.Signal, Routes: c.Routes}
		if c.Resource != nil {
			b, err := proto.Marshal(c.Resource)
			if err != nil {
				return nil, err
			}
			item.Resource = b
		}
		w.Changes = append(w.Changes, item)
	}
	data, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxWire {
		return nil, errors.New("watch: publication exceeds payload limit")
	}
	return data, nil
}

// Envelope is one publication.
//
// JSON because a notification is a **string** and this one is read by a person
// as often as by a program: `LISTEN payday_watch` in psql is how somebody finds
// out whether the thing they just did published anything.
type Envelope struct {
	Actor   string   `json:"a,omitempty"`
	Tenant  string   `json:"t,omitempty"`
	Method  string   `json:"m"`
	Changes []Change `json:"c"`
}

type Change struct {
	Method   string        `json:"m,omitempty"`
	Resource []byte        `json:"r,omitempty"`
	Signal   []byte        `json:"s,omitempty"`
	By       string        `json:"b"`
	Key      string        `json:"k"`
	Routes   []watch.Route `json:"q"`
}

func Decode(s string) (watch.Event, error) {
	var w Envelope
	if err := json.Unmarshal([]byte(s), &w); err != nil {
		return watch.Event{}, err
	}

	v := watch.Event{Method: w.Method, Changes: make([]watch.Change, 0, len(w.Changes))}
	for _, c := range w.Changes {
		k, err := pdid.Parse(c.Key)
		// App-wide signals have no resource key. Resource mutations still
		// require a valid identifier, even when they also carry a signal.
		if c.Key == pdid.Nil.String() && len(c.Signal) > 0 && len(c.Resource) == 0 {
			k, err = pdid.Nil, nil
		}
		if err != nil {
			return watch.Event{}, fmt.Errorf("key %q: %w", c.Key, err)
		}

		method := c.Method
		if method == "" {
			method = w.Method
		}
		resource, err := decodeResource(c.Resource)
		if err != nil {
			return watch.Event{}, err
		}
		v.Changes = append(v.Changes, watch.Change{By: c.By, Method: method, Key: k, Resource: resource, Signal: c.Signal, Routes: c.Routes})
	}

	for _, p := range []struct {
		s string
		v *pdid.Id
	}{{w.Actor, &v.Actor}, {w.Tenant, &v.Tenant}} {
		if p.s == "" {
			continue
		}

		k, err := pdid.Parse(p.s)
		if err != nil {
			return watch.Event{}, fmt.Errorf("%w", err)
		}

		*p.v = k
	}

	return v, nil
}

func decodeResource(b []byte) (*pdpb.WatchEvent, error) {
	if len(b) == 0 {
		return nil, nil
	}
	v := &pdpb.WatchEvent{}
	if err := proto.Unmarshal(b, v); err != nil {
		return nil, fmt.Errorf("watch: invalid resource envelope: %w", err)
	}
	return v, nil
}
