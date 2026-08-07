// Package config reads what an app was configured with.
//
// The configuration struct belongs to the app. It declares what the app can be
// told, and this package has no way of knowing that, so it does not try. What
// it offers is the two halves that do not change from one app to the next.
//
// The **pieces** are the blocks every payday app is configured with, and an app
// embeds the ones it wants:
//
//	type Config struct {
//	    Server config.ServerConfig `yaml:"server"`
//	    Db     config.DbConfig     `yaml:"db"`
//	    Otel   config.OtelConfig   `yaml:"otel"`
//
//	    Greet GreetConfig `yaml:"greet"` // and whatever else the app has
//	}
//
// The **machinery** is [Loader], which fills in any struct at all -- a file,
// then the environment over the top of it:
//
//	l := config.For("acme")
//
//	var c Config
//	from, err := l.Load(&c, path, os.Environ())
//
// That boundary is the whole reason this package exists. Everything an app
// keeps to itself goes on being the app's, and everything that would otherwise
// be copied into every app is here.
//
// # Where a value comes from
//
// A file, then the environment, and nothing else. The order is the point and it
// is why [Loader.Load] does both rather than leaving them to be called in turn:
// a configuration read the other way round is one where a deployment sets a
// variable, watches the file win, and has nothing to look at that says so.
//
// Inside the file, `${env:NAME}` is resolved before it is decoded, so a secret
// can be named in the file without being written in it. That is a third source
// only in the sense that the file names it; see [ReadFile].
//
// A default is never written in afterwards. It is answered when the value is
// asked for -- [ServerConfig.ListenAddr] rather than a pass that fills the
// field in -- because a pass that fills fields in runs either before the
// environment was read, where it is pointless, or after, where it undoes it.
// There is no order to get wrong if there is nothing to order.
package config

import (
	"errors"
	"os"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/lesomnus/mkot"
	"github.com/lesomnus/z"
)

// Loader reads a configuration on behalf of one app.
//
// It carries the app's name because everything about where a configuration
// comes from is derived from it: the files that are read when nobody named one,
// and the prefix of every environment variable that says something. The
// template this was taken from wrote both out as constants, which is the one
// thing a framework cannot do -- every app built on it would read every other
// app's file and every other app's environment.
type Loader struct {
	name   string
	prefix string
}

// For answers with a loader for the app named `name`.
//
// The name is the app's own, spelled the way its file is: "go-app" reads
// `go-app.yaml` and is told things through `GO_APP_*`. Saying it once here is
// what keeps the two from drifting apart, and it is the only thing the app has
// to say to be configured at all.
//
// It panics on an empty name rather than answering with an error, because that
// is a program built wrong and not a deployment configured wrong: the prefix
// would be "_" and the file ".yaml", and there is nobody to hand an error to
// where this is called.
func For(name string) Loader {
	if name == "" {
		panic("config: an app has to be named: its file and its environment variables are named after it")
	}

	return Loader{name: name, prefix: prefixOf(name)}
}

// Name is the app this loader reads for.
func (l Loader) Name() string { return l.name }

// Prefix is what every environment variable this loader reads starts with.
//
// A name that starts with it and that nothing answers to is reported rather
// than refused -- see [Loaded.Unknown] -- so the app may put its own there
// as well.
func (l Loader) Prefix() string { return l.prefix }

// Paths are the files [Loader.Load] tries when it was given none, in order.
//
// Both spellings of the extension are tried because both are written, and the
// one that is not tried is the one somebody will use.
func (l Loader) Paths() []string {
	return []string{l.name + ".yaml", l.name + ".yml"}
}

// prefixOf is the app's name as the beginning of an environment variable name.
//
// Anything that is not a letter or a digit becomes an underscore, since that is
// all such a name may hold -- so "go-app" and "go.app" both arrive at "GO_APP_"
// rather than at a name no shell can set.
func prefixOf(name string) string {
	v := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - ('a' - 'A')
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, name)

	return v + "_"
}

// Loaded is what a load has to say about itself, beside the configuration it
// filled in.
type Loaded struct {
	// Path is the file that was read, and empty when there was none.
	//
	// Having nothing to read is not a failure, so this is how an app tells the
	// difference: a deployment that says everything in the environment has
	// nothing to put in a file, and one that meant to have a file and does not
	// is going to want to hear about it.
	Path string

	// Unknown are the names that start with the app's prefix and that no field
	// answers to, which is what a typo looks like.
	//
	// They are answered with rather than refused because the build puts a few
	// of its own in the same namespace -- an app's `<APP>_VERSION` is not a
	// misspelt configuration field -- so it is the app that decides whether one
	// is worth a warning.
	Unknown []string
}

// Load fills in `v`, a pointer to the app's configuration struct, from a file
// and then from `environ`, which is what [os.Environ] answers with.
//
// An empty `path` means the app's [Loader.Paths], which are tried in order and
// skipped when they are not there. A `path` that was **named** must exist: a
// file named on a command line and misspelt would otherwise be a server running
// on defaults, serving happily, with nothing anywhere saying that the file
// nobody found was the one holding the address of the database.
func (l Loader) Load(v any, path string, environ []string) (Loaded, error) {
	var from Loaded

	if path != "" {
		if err := ReadFile(path, v); err != nil {
			return from, err
		}

		from.Path = path
	} else {
		for _, p := range l.Paths() {
			err := ReadFile(p, v)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return from, err
			}

			from.Path = p
			break
		}
	}

	unknown, err := l.OverrideFromEnv(v, environ)
	if err != nil {
		return from, z.Err(err, "environment")
	}

	from.Unknown = unknown
	return from, nil
}

// ReadFile fills in `v` from the YAML file at `path`.
//
// It is what [Loader.Load] reads a file with, and is here on its own for
// whatever has a second file to read -- a fixture, a file named by another
// file. It is not a method because nothing about reading one file depends on
// which app is reading it.
func ReadFile(path string, v any) error {
	// Answered as it came, so a caller can tell the file not being there from
	// the file being wrong -- which is how [Loader.Load] tells a path it was
	// handed apart from one it guessed. It names the file already, which is
	// why the two wrapped below name it and this one does not.
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// "${env:NAME}" and "${env:NAME:-default}" are resolved before the file is
	// read, the way the OpenTelemetry Collector resolves them, so a secret can
	// be named in the file without being written in it. A name that is neither
	// set nor given a default is an error rather than an empty string. Write
	// "$$" for a literal dollar sign.
	b, err = mkot.ExpandEnv(b)
	if err != nil {
		return z.Err(err, "expand %s", path)
	}

	if err := yaml.Unmarshal(b, v); err != nil {
		return z.Err(err, "decode %s", path)
	}

	return nil
}
