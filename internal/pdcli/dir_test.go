package pdcli

import (
	"testing"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
)

// TestDirIsOptionalWhereItSaysItIs.
//
// Every DIR says "the working directory by default" and `discover` writes that
// default down, but neither is worth anything if the parser refuses the
// invocation first: an argument is required unless it says otherwise, so the
// default was unreachable and the brief was a promise the command did not keep.
//
// The tree is walked rather than listed, because the subcommand that takes a
// DIR next is the one nobody would remember to add to a list here.
func TestDirIsOptionalWhereItSaysItIs(t *testing.T) {
	seen := 0

	var walk func(path string, c *xli.Command)
	walk = func(path string, c *xli.Command) {
		for _, a := range c.Args {
			if a.Info().Name != "DIR" {
				continue
			}

			v, ok := a.(*arg.String)
			if !ok {
				t.Errorf("%s: DIR is a %T, and this knows how to read an arg.String", path, a)
				continue
			}

			seen++
			if !v.Optional {
				t.Errorf("%s: DIR is required, and its brief says %q", path, v.Brief)
			}
		}

		for _, sub := range c.Commands {
			walk(path+" "+sub.Name, sub)
		}
	}
	walk("pd", NewCmdRoot())

	// A walk that found nothing would pass every assertion above.
	if seen == 0 {
		t.Fatal("no DIR was found anywhere, so this asserted nothing")
	}
}
