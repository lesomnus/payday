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
func TimeZoneFlag() *flg.String {
	return &flg.String{Name: "tz", Brief: "display time zone, e.g. Asia/Seoul (default <APP>_TZ or UTC)"}
}

// Location resolves --tz, then <APP>_TZ, then UTC. APP is the root command's
// name, uppercased with non-ASCII letters/digits replaced by underscores.
// The process-wide TZ is deliberately ignored so the default stays stable.
// Custom command renderers can use this same helper and pass the result to
// PrinterNamed, Table, or their own time formatting code.
func Location(cmd *xli.Command) (*time.Location, error) {
	name, explicit := flg.Find[string](cmd, "tz")
	if !explicit {
		root := cmd.Root().Name
		if root != "" {
			prefix := strings.Map(func(r rune) rune {
				if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
					return r
				}
				return '_'
			}, strings.ToUpper(root))
			name = os.Getenv(prefix + "_TZ")
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
