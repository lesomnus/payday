package config

import (
	"encoding"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/lesomnus/z"
)

// EnvNames lists every environment variable `v`, a pointer to a struct, is read
// from, in the order the fields appear in it. Nothing else has names, so
// anything else has none.
//
// It is what answers "what can this deployment be told?", which is a question
// nobody can answer by reading the struct once and remembering: `pd config env`
// writes the list out, and CI holds the documented one against it.
func (l Loader) EnvNames(v any) []string {
	root, err := root(v)
	if err != nil {
		return nil
	}

	vs := []string{}
	walk(l.prefix, root, nil, func(name string, _ reflect.Value) (bool, error) {
		vs = append(vs, name)
		return false, nil
	})

	return vs
}

// OverrideFromEnv takes what `environ`, which is what [os.Environ] returns,
// says over what `v`, a pointer to a struct, already holds. A value is named
// after the path to it, so `dsn` of `db` is read from `<APP>_DB_DSN`.
//
// It returns the names that start with [Loader.Prefix] but no field answers to,
// which is what a typo looks like.
func (l Loader) OverrideFromEnv(v any, environ []string) ([]string, error) {
	root, err := root(v)
	if err != nil {
		return nil, err
	}

	vs := map[string]string{}
	for _, e := range environ {
		k, v, ok := strings.Cut(e, "=")
		if ok && strings.HasPrefix(k, l.prefix) {
			vs[k] = v
		}
	}
	if len(vs) == 0 {
		return nil, nil
	}

	_, err = walk(l.prefix, root, nil, func(name string, field reflect.Value) (bool, error) {
		v, ok := vs[name]
		if !ok {
			return false, nil
		}

		delete(vs, name)
		if err := set(field, v); err != nil {
			return false, z.Err(err, "%s", name)
		}

		return true, nil
	})
	if err != nil {
		return nil, err
	}

	return slices.Sorted(maps.Keys(vs)), nil
}

// root is what the walk starts at: something that is a struct and that can be
// written to.
func root(v any) (reflect.Value, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Type().Elem().Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("must be a pointer to a struct, not %T", v)
	}

	return rv, nil
}

// visitor is called with the environment variable name of a field a value can
// be read into. It reports whether it read one.
type visitor func(name string, field reflect.Value) (bool, error)

// walk visits every field of the struct `v`, which may be a pointer to one,
// naming it after the path to it. It reports whether anything was read.
func walk(prefix string, v reflect.Value, path []string, visit visitor) (bool, error) {
	// A struct that is not there yet is made only if something is read into
	// it, so that looking at the environment does not fill a configuration
	// with what nothing was said about.
	var missing reflect.Value
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			if !v.CanSet() {
				return false, nil
			}

			missing = v
			v = reflect.New(v.Type().Elem())
		}

		v = v.Elem()
	}

	set := false
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}

		name, inline, ok := field(f)
		if !ok {
			continue
		}

		var (
			fv  = v.Field(i)
			err error
			one bool
		)
		switch {
		case inline:
			// An inlined struct is part of the one that holds it, so it adds
			// nothing to the path.
			one, err = walk(prefix, fv, path, visit)
		case leaf(f.Type):
			one, err = visit(key(prefix, append(path, name)), fv)
		default:
			one, err = walk(prefix, fv, append(path, name), visit)
		}
		if err != nil {
			return false, err
		}

		set = set || one
	}

	if set && missing.IsValid() {
		missing.Set(v.Addr())
	}

	return set, nil
}

// field reads how a struct field is spelled in YAML, which is what it is named
// after here as well.
func field(f reflect.StructField) (name string, inline bool, ok bool) {
	tag, rest, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	if tag == "-" {
		return "", false, false
	}

	name = tag
	if name == "" {
		// What the YAML decoder falls back to.
		name = strings.ToLower(f.Name)
	}

	return name, slices.Contains(strings.Split(rest, ","), "inline"), true
}

// key is the environment variable a field at the given path is read from.
func key(prefix string, path []string) string {
	name := strings.Join(path, "_")
	name = strings.ReplaceAll(name, "-", "_")

	return prefix + strings.ToUpper(name)
}

var (
	yamlBytes     = reflect.TypeFor[yaml.BytesUnmarshaler]()
	yamlInterface = reflect.TypeFor[yaml.InterfaceUnmarshaler]()
	text          = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// leaf reports whether a value of this type is read as a whole rather than
// walked into: anything that is not a struct, and a struct that knows how to
// read itself.
func leaf(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return true
	}

	p := reflect.PointerTo(t)
	return p.Implements(yamlBytes) || p.Implements(yamlInterface) || p.Implements(text)
}

// set reads `v` into the field.
func set(field reflect.Value, v string) error {
	// A value that is not there yet is made and read into. The decoder is of
	// no help here: handed a pointer to a pointer it leaves it nil and reports
	// nothing.
	if field.Kind() == reflect.Pointer {
		p := reflect.New(field.Type().Elem())
		if err := set(p.Elem(), v); err != nil {
			return err
		}

		field.Set(p)
		return nil
	}

	// A string is taken as it is. Reading it as YAML would give a meaning to
	// the punctuation a data source name or a format string is full of.
	if field.Kind() == reflect.String {
		field.SetString(v)
		return nil
	}

	// Anything else is read by the decoder that reads the file, so a number,
	// a switch or a list means here what it means there.
	return yaml.Unmarshal([]byte(v), field.Addr().Interface())
}
