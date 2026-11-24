package pdcmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// PrintOptions changes human-readable formatting. A nil Location means UTC.
// Serialization formats ignore it, preserving their wire representation.
type PrintOptions struct {
	Location *time.Location
}

// Keep well-known values in their established JSON shape. Only a Timestamp
// has a display zone, and its normal JSON encoding validates it first.
func humanJSON(m protoreflect.Message, indent string, loc *time.Location) ([]byte, error) {
	b, err := jsonOf(m, indent)
	if err != nil || loc == time.UTC || m.Descriptor().FullName() != "google.protobuf.Timestamp" {
		return b, err
	}
	var value string
	if err := json.Unmarshal(b, &value); err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(t.In(loc).Format(time.RFC3339Nano))
}

func printLocation(opts []PrintOptions) *time.Location {
	if len(opts) > 0 && opts[0].Location != nil {
		return opts[0].Location
	}
	return time.UTC
}

// TimeZoneFlag lets an application's own command accept the same --tz flag as
// the generated commands. Resolve its value with Location before doing work.
//
// Its help does not name the variable Location also reads, because a flag is
// made before the command it is on and does not know which root that command
// will be under. A placeholder such as <APP>_TZ would be a name nothing reads,
// so the help says nothing rather than something wrong.
func TimeZoneFlag() *flg.String {
	return &flg.String{Name: "tz", Brief: "display time zone, e.g. Asia/Seoul (default UTC)"}
}

// zoneFlag is --tz on a command this package built, which knows the command it
// is on and so can name, in its help, the variable Location reads for it -- as
// the help of a flag the configuration binds names its own, [$APP_ADDR].
//
// The name is worked out when the help asks, not when the command is built:
// the root is whatever the app mounts the tree under, and xli links each
// command to its parent only on the way to running or printing it.
type zoneFlag struct {
	*flg.String
	cmd *xli.Command
}

func flgZone() *zoneFlag { return &zoneFlag{String: TimeZoneFlag()} }

func (f *zoneFlag) Unwrap() flg.Flag { return f.String }

func (f *zoneFlag) Info() *flg.Info {
	info := f.String.Info()
	if f.cmd != nil {
		info.Env = zoneEnv(f.cmd)
	}
	return info
}

// zoned tells the --tz of c which command it is on, and is c.
func zoned(c *xli.Command) *xli.Command {
	for _, f := range c.Flags {
		if z, ok := f.(*zoneFlag); ok {
			z.cmd = c
		}
	}
	return c
}

// zoneEnv is the variable Location reads for cmd, or "" where its root has no
// name and Location reads none.
func zoneEnv(cmd *xli.Command) string {
	root := cmd.Root().Name
	if root == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToUpper(root)) + "_TZ"
}

// Location resolves --tz, then <APP>_TZ, then UTC. APP is the root command's
// name, uppercased with non-ASCII letters/digits replaced by underscores.
// The process-wide TZ is deliberately ignored so the default stays stable.
// Custom command renderers can use this same helper and pass the result to
// PrinterNamed, Table, or their own time formatting code.
func Location(cmd *xli.Command) (*time.Location, error) {
	name, explicit := flg.Find[string](cmd, "tz")
	if !explicit {
		if env := zoneEnv(cmd); env != "" {
			name = os.Getenv(env)
		}
	}
	if name == "" && !explicit {
		return time.UTC, nil
	}
	if name == "" {
		return nil, fmt.Errorf("--tz %q: name an IANA time zone or UTC", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("--tz %q: %w", name, err)
	}
	return loc, nil
}
