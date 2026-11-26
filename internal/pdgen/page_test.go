package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdpb"
)

// paged is payday's own entities with `entity`'s page, `size: <n>` and `max:
// <n>` where it declares them, said as `page` instead -- what the merge hands
// a generator once `pd gen` has put the overlay's word where payday's was.
func paged(t *testing.T, entity, was, page string) string {
	t.Helper()

	src := ownAll(t)
	at := strings.Index(src, "message "+entity+" {")
	require.GreaterOrEqual(t, at, 0, "payday has no %s", entity)
	end := at + strings.Index(src[at:], "\n}\n")
	body := src[at:end]
	require.Contains(t, body, was, "payday's %s does not say its page as this expects", entity)

	return src[:at] + strings.Replace(body, was, page, 1) + src[end:]
}

const paydaysPage = "      size: 20\n      max: 100\n"

// TestAnOverlayMaySayThePageOfPaydaysHolderAndTenant: how many of its people
// or its customers a page holds is the app's to say, as it is of its own
// entities. What it says is the page; what it leaves unsaid is payday's; and
// the page is held to what an app's own is held to, in the same words. Of
// payday's other entities, and of the rest of a list, nothing is the
// overlay's.
func TestAnOverlayMaySayThePageOfPaydaysHolderAndTenant(t *testing.T) {
	for _, tt := range []struct {
		entity    string
		own       pdpb.Own
		page      string
		size, max int
	}{
		{"Holder", pdpb.Own_OWN_HOLDER, "      size: 40\n      max: 120\n", 40, 120},
		{"Tenant", pdpb.Own_OWN_TENANT, "      size: 40\n      max: 120\n", 40, 120},
		// What is left unsaid is payday's: the merge keeps what only payday's
		// says.
		{"Holder", pdpb.Own_OWN_HOLDER, "      size: 20\n      max: 500\n", 20, 500},
		{"Tenant", pdpb.Own_OWN_TENANT, "      size: 5\n      max: 100\n", 5, 100},
	} {
		t.Run(tt.entity+" "+strings.Join(strings.Fields(tt.page), " "), func(t *testing.T) {
			x := require.New(t)

			s, err := readAs(t, "fleet", paged(t, tt.entity, paydaysPage, tt.page)+tenantedRobot)
			x.NoError(err)
			v := s.Own(tt.own)
			x.NotNil(v)
			x.Equal(tt.size, v.List.Size)
			x.Equal(tt.max, v.List.Max)
		})
	}

	for _, tt := range []struct {
		name   string
		entity string
		was    string
		page   string
		says   string
	}{
		// What an app's own entity's page is refused for, in its words.
		{"a size above the max", "Holder", paydaysPage, "      size: 20\n      max: 10\n",
			"list: `max` is 10 and `size` is 20, so what a request gets by asking for nothing is more than it may have"},
		{"a max of none", "Tenant", paydaysPage, "      size: 20\n      max: 0\n",
			"list: `max` is required; a page with no cap is an answer with no cap"},

		// The rest of a list is payday's: the wall and payday's reads are
		// written against it.
		{"a filter count", "Holder", paydaysPage, paydaysPage + "      filters: 4\n",
			"fleet.Holder: (payday.entity) list.filters is not payday's; an overlay may say the page of payday's Holder and Tenant"},

		// And payday's other entities' pages are payday's.
		{"the page of the trail", "Audit", "      size: 50\n      max: 200\n", "      size: 50\n      max: 500\n",
			"fleet.Audit: (payday.entity) list.max is not payday's; an overlay may not add to payday's declaration"},
	} {
		t.Run(tt.name+" is refused", func(t *testing.T) {
			_, err := readAs(t, "fleet", paged(t, tt.entity, tt.was, tt.page)+tenantedRobot)
			require.ErrorContains(t, err, tt.says)
		})
	}

	t.Run("and a value of payday's declaration said otherwise is refused by its path", func(t *testing.T) {
		src := paged(t, "Holder", "    domain: 2\n", "    domain: 9\n")
		_, err := readAs(t, "fleet", src+tenantedRobot)
		require.ErrorContains(t, err, "fleet.Holder: (payday.entity) domain is not payday's")
	})
}
