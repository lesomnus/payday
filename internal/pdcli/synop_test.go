package pdcli

import (
	"strings"
	"testing"

	"github.com/lesomnus/xli"
)

// TestASynopsisSaysWhatTheParserTakes.
//
// The synopsis is the one line a person reads before typing, and it is written
// by hand beside a parser that is not -- so the two drift, and the half that
// drifts is the half nothing runs. An argument that became optional and stayed
// unbracketed reads as required and sends somebody looking for a directory to
// name; one shown bracketed that is not invites an invocation the parser
// refuses before a handler is ever reached.
//
// Arguments and not flags, deliberately. A flag may be out of a synopsis on
// purpose: `pd entity add --tenant` is taken so that asking for it can be
// answered, and a synopsis is what may be written rather than what is parsed.
//
// The tree is walked rather than listed, for the reason in
// [TestDirIsOptionalWhereItSaysItIs]: the command that grows an argument next
// is the one nobody would remember to add to a list here.
func TestASynopsisSaysWhatTheParserTakes(t *testing.T) {
	seen := 0

	var walk func(path string, c *xli.Command)
	walk = func(path string, c *xli.Command) {
		// It names itself, which is what makes the rest of this readable: a
		// synopsis copied from a sibling passes every assertion below.
		if !strings.HasPrefix(c.Synop, path+" ") {
			t.Errorf("%s: the synopsis is %q, which is not this command", path, c.Synop)
		}

		for _, a := range c.Args {
			name := a.Info().Name
			seen++

			if !strings.Contains(c.Synop, name) {
				t.Errorf("%s: %s is parsed and the synopsis %q does not have it", path, name, c.Synop)
				continue
			}

			bracketed := strings.Contains(c.Synop, "["+name+"]")
			if bracketed == a.IsOptional() {
				continue
			}
			if a.IsOptional() {
				t.Errorf("%s: %s is optional and the synopsis %q asks for it", path, name, c.Synop)
			} else {
				t.Errorf("%s: %s is required and the synopsis %q offers it", path, name, c.Synop)
			}
		}

		for _, sub := range c.Commands {
			walk(path+" "+sub.Name, sub)
		}
	}
	walk("pd", NewCmdRoot())

	// A walk that found nothing would pass every assertion above.
	if seen == 0 {
		t.Fatal("no argument was found anywhere, so this asserted nothing")
	}
}
