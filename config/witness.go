package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/lesomnus/payday/trail"
)

// witnesses holds what each registered scheme opens.
var witnesses = map[string]func(v string) (trail.Witness, error){}

// shippedWitnesses is the witnesses payday has, named here only so that a
// configuration naming one this binary does not link is told which package
// would.
var shippedWitnesses = map[string]string{
	"log":   "github.com/lesomnus/payday/config/witnesslog",
	"rekor": "github.com/lesomnus/payday/config/witnessrekor",
	"store": "github.com/lesomnus/payday/config/witnessstore",
}

// RegisterWitness records how to open a witness that
// `audit.checkpoints.witnesses` names by its scheme -- `log:`, `store:…` -- so
// that where the head of the chain is heard is a line of a deployment's
// configuration (payday#115; see trail.Witness).
//
// It is [RegisterSigner]'s shape, for its reason: each witness is a package of
// its own that an app imports, and what talks to a log service is linked in
// only where it is wanted:
//
//	import _ "github.com/lesomnus/payday/config/witnessstore"
//
// `open` is handed the whole of what was written, scheme and all, once, where
// the process comes up. The scheme is a URI's, compared without case.
func RegisterWitness[W trail.Witness](scheme string, open func(v string) (W, error)) {
	if !isScheme(scheme) {
		panic(fmt.Sprintf("config.RegisterWitness: %q is not a scheme of two characters or more", scheme))
	}

	witnesses[strings.ToLower(scheme)] = func(v string) (trail.Witness, error) {
		w, err := open(v)
		if err != nil {
			return nil, err
		}
		// A nil pointer is a witness to the interface, and one that would
		// panic at its first checkpoint rather than say it opened nothing.
		if r := reflect.ValueOf(w); !r.IsValid() || r.Kind() == reflect.Pointer && r.IsNil() {
			return nil, nil
		}

		return w, nil
	}
}

// Witnesses lists every scheme a deployment may name a witness by in
// `audit.checkpoints.witnesses`, in lexical order.
func Witnesses() []string {
	return slices.Sorted(maps.Keys(witnesses))
}

// witnessesOf opens the witnesses `audit.checkpoints.witnesses` names, by the
// name a finding says each by: what was written, up to a `?`, which is where a
// URI would carry what is not to be printed.
func witnessesOf(vs []string) (map[string]trail.Witness, error) {
	if len(vs) == 0 {
		return nil, nil
	}

	out := map[string]trail.Witness{}
	for i, v := range vs {
		v = strings.TrimSpace(v)
		name, _, _ := strings.Cut(v, "?")
		if _, ok := out[name]; ok {
			return nil, fmt.Errorf("[%d]: %s is named twice", i, name)
		}

		scheme, named := schemeOf(v)
		if !named {
			return nil, fmt.Errorf("[%d]: %q is named by no scheme; a witness is `log:`, `store:/mnt/worm/witness`, or a scheme something registered", i, name)
		}
		open, ok := witnesses[scheme]
		if !ok {
			return nil, fmt.Errorf("[%d]: %w", i, unlinkedWitness(scheme))
		}

		w, err := open(v)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %s: %w", i, scheme, err)
		}
		if w == nil {
			return nil, fmt.Errorf("[%d]: %s: opened nothing", i, scheme)
		}

		out[name] = w
	}

	return out, nil
}

// unlinkedWitness refuses a scheme nothing in this binary opens.
func unlinkedWitness(scheme string) error {
	has := "none"
	if len(witnesses) > 0 {
		has = strings.Join(Witnesses(), ", ")
	}
	how := "import the package that registers it, or see config.RegisterWitness to write one"
	if pkg, ok := shippedWitnesses[scheme]; ok {
		how = "import " + pkg
	}

	return fmt.Errorf("`%s:` is not a witness this binary opens (it opens %s): %s", scheme, has, how)
}
