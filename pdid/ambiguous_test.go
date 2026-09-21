package pdid

import "testing"

// Two apps in one process that both have a `site`: the word stops answering,
// the full names still do, and nothing panics before main.
func TestTwoAppsOneWord(t *testing.T) {
	Register("one.Widget", 201, "test-widget")
	Register("two.Widget", 202, "test-widget")
	// Once more, which is a package linked in twice: still nothing.
	Register("one.Widget", 201, "test-widget")

	if d, ok := DomainOf("test-widget"); ok {
		t.Fatalf("an ambiguous word answered domain %d", d)
	}
	if d, ok := Lookup("one.Widget"); !ok || d != 201 {
		t.Fatalf("one.Widget: %d, %v", d, ok)
	}
	if d, ok := Lookup("two.Widget"); !ok || d != 202 {
		t.Fatalf("two.Widget: %d, %v", d, ok)
	}
	// A word only one app writes keeps answering.
	Register("one.Gadget", 203, "test-gadget")
	if d, ok := DomainOf("test-gadget"); !ok || d != 203 {
		t.Fatalf("test-gadget: %d, %v", d, ok)
	}
}
