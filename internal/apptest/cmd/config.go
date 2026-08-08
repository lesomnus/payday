// Package cmd is the app's own wiring, and it is here to be counted.
//
// The checkpoint this was written for asks whether extracting the runtime
// actually made an app thinner, or whether it only moved code into
// configuration that the app then has to write out anyway. So this is the
// whole of what an app says to stand up a served, migrated, walled server, and
// it is deliberately not hidden behind a `payday.Serve(cfg)`: the stack, the
// order of the interceptors, and which server the wall is on are the decisions
// a reader of an app most needs to be able to see.
package cmd

import (
	"github.com/lesomnus/xli"

	"github.com/lesomnus/payday/pdcmd"

	"github.com/lesomnus/payday/config"

	// The one driver this app runs on. It is blank-imported here rather than
	// by payday so that an app does not carry an engine it never opens.
	_ "github.com/lesomnus/payday/config/dbsqlite3"
)

// Name is what this app is called, and it is the only place it is written.
// The environment prefix and the names of the configuration files are derived
// from it -- APPTEST_DB_DSN, apptest.yaml -- so there is nothing to keep in
// step.
const Name = "apptest"

// Loader reads this app's configuration.
var Loader = config.For(Name)

// Config is what this app is configured with.
//
// The framework cannot own this struct, since what an app is configured with is
// the app's. What it owns is the pieces: each of these is a payday type, and
// what is written here is only which of them this app has.
type Config struct {
	Server config.ServerConfig `yaml:"server"`
	Db     config.DbConfig     `yaml:"db"`
	Otel   config.OtelConfig   `yaml:"otel"`
	Watch  config.WatchConfig  `yaml:"watch"`
}

// Cmd is this app's own command line: what payday supplies, plus whatever the
// app has of its own.
//
// `config`, `config env` and `version` are payday's -- they are the commands
// that run against a **deployment** rather than against a checkout, and every
// one of them needs something only the app can hand over. `config env` is the
// clearest: listing the variables a deployment can set means walking this
// struct, and the struct is the app's.
//
// `serve` is not among them and will not be. It is the one command whose body
// is the stack -- which layers, in which order, with the wall on which server
// -- and that is the most important thing a reader of an app can see.
func Cmd(c *Config) *xli.Command {
	return &xli.Command{
		Name:  Name,
		Brief: "the app payday is tried against",

		Commands: []*xli.Command{
			pdcmd.NewCmdVersion(),
			pdcmd.NewCmdConfig(Loader, c),
		},

		Handler: xli.RequireSubcommand(),
	}
}
