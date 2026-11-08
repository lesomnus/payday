package pdid_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// TestAFormerNameReadsAsTheDomainItWas: a word and a full name an earlier
// build wrote a domain as answer that domain, and what the domain is called
// now stays what it is registered as -- whichever of the two was said first,
// since generated inits run in no order anybody chose.
func TestAFormerNameReadsAsTheDomainItWas(t *testing.T) {
	x := require.New(t)

	d := pdid.Domain(210)
	// Before the registration, as an init in another file may run.
	pdid.Formerly("test.TenantGadget", d, "tenant-gadget")
	pdid.Register("test.Gadget", d, "gadget")

	v, ok := pdid.DomainOf("tenant-gadget")
	x.True(ok, "the word an earlier build wrote")
	x.Equal(d, v)
	v, ok = pdid.Lookup("test.TenantGadget")
	x.True(ok, "the full name an earlier build gave its message")
	x.Equal(d, v)

	x.Equal("gadget", d.String(), "what it is called now")
	x.Equal("gadget", pdid.Domains()[d])
	v, ok = pdid.DomainOf("gadget")
	x.True(ok)
	x.Equal(d, v)

	t.Run("said again is the same", func(t *testing.T) {
		require.NotPanics(t, func() { pdid.Formerly("test.TenantGadget", d, "tenant-gadget") })
	})

	t.Run("a full name is one domain, then as now", func(t *testing.T) {
		require.Panics(t, func() { pdid.Formerly("test.TenantGadget", pdid.Domain(211), "other") })
		require.Panics(t, func() { pdid.Formerly("test.Nothing", pdid.Unknown, "nothing") })
		require.Panics(t, func() { pdid.Formerly("", d, "nothing") })
	})
}

// TestAWordInUseIsNotAFormerOnes: a word another domain is written as now
// answers that domain, whatever an earlier build meant by it -- and a word two
// apps had for two things answers neither.
func TestAWordInUseIsNotAFormerOnes(t *testing.T) {
	x := require.New(t)

	pdid.Register("test.Sprocket", 212, "sprocket")
	pdid.Formerly("test.OldCog", 213, "sprocket")
	v, ok := pdid.DomainOf("sprocket")
	x.True(ok)
	x.Equal(pdid.Domain(212), v, "what it names now")

	pdid.Formerly("one.Widget", 214, "test-old-widget")
	pdid.Formerly("two.Widget", 215, "test-old-widget")
	_, ok = pdid.DomainOf("test-old-widget")
	x.False(ok, "a former word two apps mean two things by")
}
