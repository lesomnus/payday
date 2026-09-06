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
	"github.com/lesomnus/payday/config"
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

	// How long the trail keeps a row, per kind of thing. Empty is forever,
	// which is what this app is: nothing here is regulated and a test that
	// swept its own trail would be a test fighting the harness.
	//
	// Here anyway, because a block payday ships and its own app does not
	// compose is one nobody has checked composes.
	Audit config.AuditConfig `yaml:"audit"`
}
