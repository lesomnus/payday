package past

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/spin"
)

// things is a store whose every identifier of domain 7 is a Thing, as its
// entity declares one, and whose other identifiers are of nothing.
type things struct{ Store }

func (things) Describe(id pdid.Id) (Description, bool) {
	if id.Domain() != 7 {
		return Description{}, false
	}

	return described(), true
}

// TestWhatNoWindowEndsIsHeldToTheSchema: a fact kept past every window is
// named by its kind of resource, its field -- its entry, of a map -- and the
// values it is kept holding, each as [Encode] writes it. A fact's value is
// compared as text, so a value written any other way would keep nothing while
// the app said it kept something: it is refused instead, with what it should
// have said.
func TestWhatNoWindowEndsIsHeldToTheSchema(t *testing.T) {
	keep := func(k FactKeep) error {
		_, err := (Expiry{KeepFacts: []FactKeep{k}}).facts(things{})
		return err
	}

	for name, k := range map[string]FactKeep{
		"every value of a field":      {Domain: 7, Field: 19},
		"an enum, and it cleared":     {Domain: 7, Field: 19, Values: []string{"2", Null}},
		"a string":                    {Domain: 7, Field: 5, Values: []string{`"x"`}},
		"a list, whole":               {Domain: 7, Field: 22, Values: []string{`["a","b"]`}},
		"an entry of a map":           {Domain: 7, Field: 23, Key: "speed", Values: []string{`"slow"`}},
		"a message an entry holds":    {Domain: 7, Field: 29, Key: "3", Values: []string{`{"1":2}`}},
		"a field withheld":            {Domain: 7, Field: 24, Values: []string{Withheld}},
		"a map withheld, of no key":   {Domain: 7, Field: 25},
		"a 64-bit integer, exactly":   {Domain: 7, Field: 9, Values: []string{"18446744073709551615"}},
		"a timestamp, as RFC 3339 is": {Domain: 7, Field: 20, Values: []string{`"2026-10-01T09:00:00Z"`}},
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, keep(k))
		})
	}

	for name, c := range map[string]struct {
		k    FactKeep
		want string
	}{
		"a domain of no entity":             {FactKeep{Domain: 8, Field: 19}, "domain 8 is no entity"},
		"a field it does not have":          {FactKeep{Domain: 7, Field: 99}, "has no field 99"},
		"an enum by its name":               {FactKeep{Domain: 7, Field: 19, Values: []string{`"MODE_RUN"`}}, "is no value of it"},
		"a string unquoted":                 {FactKeep{Domain: 7, Field: 5, Values: []string{"x"}}, "is no value of it"},
		"a string escaped as it need not":   {FactKeep{Domain: 7, Field: 5, Values: []string{escapedX}}, `is written "x"`},
		"an integer quoted":                 {FactKeep{Domain: 7, Field: 8, Values: []string{`"1"`}}, "is no value of it"},
		"a float as Encode does not":        {FactKeep{Domain: 7, Field: 10, Values: []string{"0.50"}}, "is written 0.5"},
		"a key of a field no map":           {FactKeep{Domain: 7, Field: 5, Key: "x"}, "a key is an entry of a map"},
		"a key not as it is written":        {FactKeep{Domain: 7, Field: 29, Key: "03"}, `is written "3"`},
		"a key of a map withheld":           {FactKeep{Domain: 7, Field: 25, Key: "x"}, "a fact of it has no key"},
		"a value of a field withheld":       {FactKeep{Domain: 7, Field: 24, Values: []string{`"Kim"`}}, "whose facts keep none"},
		"no value, of a field not withheld": {FactKeep{Domain: 7, Field: 5, Values: []string{Withheld}}, "is a value withheld"},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			err := keep(c.k)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
			require.ErrorContains(t, err, c.want)
			require.ErrorContains(t, err, "KeepFacts[0]", "does not say which")
		})
	}
}

// TestAnExpirerAsksEachTenant: a pass asks every tenant there is, a page at a
// time, for its window; does nothing for one kept forever or under a hold; and
// for one whose window cannot be read keeps everything of it and goes on to
// the next, as the trail does -- a tenant nobody could answer for is not a
// reason to stop expiring the others.
func TestAnExpirerAsksEachTenant(t *testing.T) {
	x := require.New(t)

	var tenants []pdid.Id
	for range tenantsPage + 50 {
		tenants = append(tenants, pdid.New(1))
	}
	tenants = sorted(tenants)
	broken, forever, held := tenants[0], tenants[1], tenants[2]

	var in []pdid.Id
	e := Expirer{
		Tenants: func(_ context.Context, after pdid.Id, limit int) ([]pdid.Id, error) {
			var out []pdid.Id
			for _, v := range tenants {
				if v.Uuid().Compare(after.Uuid()) > 0 && len(out) < limit {
					out = append(out, v)
				}
			}
			return out, nil
		},
		Window: func(_ context.Context, tenant pdid.Id) (Window, error) {
			switch tenant {
			case broken:
				return Window{}, errors.New("no contract read")
			case forever:
				return Window{}, nil
			case held:
				return Window{Keep: time.Hour, Holds: []string{"case 7"}}, nil
			}
			return Window{Keep: time.Hour}, nil
		},
		In: func(_ context.Context, tenant pdid.Id, _ func(context.Context, Store) error) error {
			in = append(in, tenant)
			return nil
		},
	}

	x.Equal("payday.past.expire", e.SpinName(), "what a lease holds it by")
	x.NoError(e.Pass(t.Context()))
	x.Equal(tenants[3:], in, "each tenant whose window takes something, past the page and past the one with none")

	t.Run("and one missing what it is made of gives up", func(t *testing.T) {
		for _, e := range []Expirer{
			{Window: e.Window, In: e.In},
			{Tenants: e.Tenants, In: e.In},
			{Tenants: e.Tenants, Window: e.Window},
		} {
			require.Error(t, spin.Called("test", e).Spin(t.Context()))
		}
	})
}

// escapedX is the JSON of "x" with its one letter escaped, which reads as "x"
// and is not how [Encode] writes it: a quote, a backslash, `u0078`, a quote.
var escapedX = string([]byte{'"', 92, 'u', '0', '0', '7', '8', '"'})

func sorted(vs []pdid.Id) []pdid.Id {
	out := append([]pdid.Id(nil), vs...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].Uuid().Compare(out[i].Uuid()) < 0 {
				out[i], out[j] = out[j], out[i]
			}
		}
	}

	return out
}

// pointing is a store that says which columns name events, and nothing else.
type pointing struct {
	Store
	ps []Pointer
}

func (s pointing) Pointers() []Pointer { return s.ps }

// TestAnEventNamesAnEventByItsCauseAlone: what an expiry takes of a chain of
// events it follows by `cause_id`, which it reads off every event it takes. A
// store that says an event names an event by another column is refused, as
// `pd gen` refuses the schema that would say it, rather than followed by half.
func TestAnEventNamesAnEventByItsCauseAlone(t *testing.T) {
	x := require.New(t)

	ps := []Pointer{
		{Domain: 17, Table: "event", Column: "cause_id", Own: pdpb.Own_OWN_EVENT},
		{Domain: 18, Table: "fact", Column: "event_id", Own: pdpb.Own_OWN_FACT},
		{Domain: 20, Table: "stint", Column: "reverts", Own: pdpb.Own_OWN_UNSPECIFIED},
	}
	got, err := pointers(pointing{ps: ps})
	x.NoError(err)
	x.Equal(ps, got)

	_, err = pointers(pointing{ps: append(ps, Pointer{Domain: 17, Table: "event", Column: "reverts", Own: pdpb.Own_OWN_EVENT})})
	x.ErrorContains(err, "event.reverts names an event from an event, and only its cause may")
}
