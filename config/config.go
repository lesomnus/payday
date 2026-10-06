// Package config is the blocks every payday app is configured with.
//
// The configuration struct belongs to the app. It declares what the app can be
// told, and this package has no way of knowing that, so it does not try. What
// it offers is the **pieces** -- the blocks every payday app is configured
// with -- and an app embeds the ones it wants:
//
//	type Config struct {
//	    Server config.ServerConfig `yaml:"server"`
//	    Db     config.DbConfig     `yaml:"db"`
//	    Otel   config.OtelConfig   `yaml:"otel"`
//
//	    Greet GreetConfig `yaml:"greet"` // and whatever else the app has
//	}
//
// # What reads them
//
// Not this package. What fills in an app's struct -- a file, then the
// environment over the top of it, then the flags the command line was given --
// is xli's `cfg`, which walks any struct at all:
//
//	c := cmd.Config{}
//	l := cfg.New("acme", &c) // acme.yaml, ACME_*
//
// It was here first, and it moved because it was never payday's. Nothing about
// reading a struct from a file and the environment knows about RPC, and the
// half payday could not write -- binding a flag to the field it sets, so that
// the flags are a layer over the file rather than something every command
// copies in by hand -- belongs beside the library that parses the flag. What
// payday keeps is what only payday knows: which blocks every app has, and what
// each of them means.
//
// This package does not import `cfg`, and should not. An app's sandbox links
// `config`, through `cmd`, and has no command line to configure; the blocks are
// read by whatever the app reads them with. A field that holds a secret says so
// with a tag -- `cfg:",secret"` -- which costs nothing to import, and most do
// not need even that: `config` redacts every value whose name says it is
// secret, and the password in anything named `dsn`.
//
// # Where a value comes from
//
// The default, then the file, then the environment, then a flag, in one pass
// and in that order: a configuration read the other way round is one where a
// deployment sets a variable, watches the file win, and has nothing to look at
// that says so. `<app> config` prints what came out, with where each value came
// from.
//
// Inside the file, `${env:NAME}` is resolved, so a secret can be named in the
// file without being written in it, and `${file:/path}` names a secret that is
// re-read when it is rotated.
//
// A block's default is answered when the value is asked for --
// [ServerConfig.ListenAddr] -- rather than written into the field, so that an
// app embedding a block gets its defaults without writing them, and one that
// wants another default writes it into its own struct before the loader is
// made: what the struct holds then is the lowest layer.
package config
