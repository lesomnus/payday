package pdcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAStalePinIsExplained.
//
// The failure is real and it happened: an app's buf.lock was pinned before
// `erase:` and `own:` were pushed, so `pd gen` died on a file the app never
// wrote, naming fields it had never heard of. Nothing in buf's message says the
// word "pin", so the obvious readings -- a corrupt copy, a bad merge, a broken
// payday -- are all wrong.
//
// In the words of the compiler buf had then, which a buf named by `PD_BUF` may
// still be.
func TestAStalePinIsExplained(t *testing.T) {
	const said = `proto/payday/audit.proto:209:5:message app.Audit: option (payday.entity): field own not found
proto/payday/audit.proto:215:5:message app.Audit: option (payday.entity): field erase not found
proto/payday/tenant.proto:65:5:message app.Tenant: option (payday.entity): field own not found`

	v := whyStale("", []byte(said))
	for _, want := range []string{"`own:`", "`erase:`", "buf dep update", "a pin is yours to move"} {
		if !strings.Contains(v, want) {
			t.Errorf("does not say %q:\n%s", want, v)
		}
	}
	if n := strings.Count(v, "`own:`"); n != 1 {
		t.Errorf("names the same field %d times", n)
	}

	// And it says nothing about anything else, so an ordinary compile error is
	// not decorated with advice that does not apply.
	if v := whyStale("", []byte(`proto/app/thing.proto:12:3: field "alias" already defined`)); v != "" {
		t.Errorf("explained an unrelated failure:\n%s", v)
	}

	// Nor a typo, which no pin has: the payday running here has no such field
	// either.
	if v := whyStale("", []byte(`proto/app/thing.proto:9:5:message app.Thing: option (payday.entity): field tennanted not found`)); v != "" {
		t.Errorf("explained a typo as a stale pin:\n%s", v)
	}
}

// TestAStalePinIsExplainedInTheWordsOfTheBufThatRuns: what the buf payday pins
// said of an app that declares a past against a pin from before one, word for
// word (payday#151). Its compiler names the value and not the field, so the
// field is read where the message points -- and the fourth line is a typo in
// the same run, which the payday running here does not know either.
func TestAStalePinIsExplainedInTheWordsOfTheBufThatRuns(t *testing.T) {
	const said = "app/proto/app/payday/event.proto:141:10:cannot find `OWN_EVENT` in this scope\n" +
		"app/proto/app/payday/fact.proto:192:10:cannot find `OWN_FACT` in this scope\n" +
		"app/proto/app/robot.proto:3:3:cannot resolve message field name for `payday.App`\n" +
		"app/proto/app/robot.proto:6:5:cannot resolve message field name for `payday.Entity`\n"

	// What the lines point at, where they say.
	work := t.TempDir()
	at := filepath.Join(work, "app", "proto", "app", "robot.proto")
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package app;\noption (payday.app) = {\n  past: {event: 17, fact: 18}\n};\nmessage Cell {\n    tennanted: {}\n}\n"
	if err := os.WriteFile(at, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	v := whyStale(work, []byte(said))
	for _, want := range []string{"`past:`", "`OWN_EVENT`", "`OWN_FACT`", "buf dep update"} {
		if !strings.Contains(v, want) {
			t.Errorf("does not say %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "tennanted") {
		t.Errorf("explained a typo as a stale pin:\n%s", v)
	}

	// And a value of payday's that does not exist here is not one either.
	if v := whyStale(work, []byte("app/proto/app/robot.proto:9:9:cannot find `OWN_NOTHING` in this scope")); v != "" {
		t.Errorf("explained a value no payday has:\n%s", v)
	}
}
