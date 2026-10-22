package pdtest

import (
	"crypto"
	"testing"

	"github.com/lesomnus/payday/trail"
)

// Key is a key of this test's own that the trail's archive signs its account
// with: the PEM document `audit.checkpoints.key` reads.
//
// An archive is kept with a key, and there is none to fall back on (payday#115).
// A key everybody knew -- one in a fixture, say -- would sign forgeries for
// anybody the day it reached a deployment, so a test is given a fresh one, made
// as it runs and kept nowhere after it.
func Key(tb testing.TB) string {
	tb.Helper()

	doc, _, err := trail.GenerateKey()
	if err != nil {
		tb.Fatalf("pdtest: a key for the trail: %v", err)
	}

	return string(doc)
}

// Signer is [Key] as `trail.Policy.Signer` is handed it.
func Signer(tb testing.TB) crypto.Signer {
	tb.Helper()

	k, err := trail.ParseKey([]byte(Key(tb)))
	if err != nil {
		tb.Fatalf("pdtest: a key for the trail: %v", err)
	}

	return k
}
