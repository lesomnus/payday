package pdgen_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
)

func notificationCopies(t *testing.T, notification, receipt int) string {
	t.Helper()
	drop := regexp.MustCompile(`(?m)^(edition|package|import|option (features|go_package))\b.*$`)
	zero := regexp.MustCompile(`(?m)^(\s*)domain: 0$`)
	var out strings.Builder
	for _, v := range []struct {
		name string
		n    int
	}{{"notification", notification}, {"notification_receipt", receipt}} {
		b, err := os.ReadFile(filepath.Join("..", "..", "schema", "notify", v.name+".proto"))
		require.NoError(t, err)
		src := drop.ReplaceAllString(string(b), "")
		require.Len(t, zero.FindAllString(src, -1), 1)
		out.WriteString(zero.ReplaceAllString(src, fmt.Sprintf("${1}domain: %d", v.n)))
	}
	return out.String()
}

func TestNotifyAndPastAreIndependentFeatures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		past   bool
		notify bool
	}{{"neither", false, false}, {"past", true, false}, {"notify", false, true}, {"both", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			var option, records string
			if tc.past {
				option += "past: {event: 17, fact: 16} "
				records += copies(t, 17, 16)
			}
			if tc.notify {
				option += "notify: {notification: 40, receipt: 41} "
				if !tc.past {
					records += "import \"google/protobuf/any.proto\";\n"
				}
				records += notificationCopies(t, 40, 41)
			}
			s, err := readAs(t, "fleet", "option (payday.app) = {"+option+"};\n"+ownAll(t)+records)
			x.NoError(err)
			x.NoError(pdgen.CheckOwn(s))
			x.Equal(tc.past, s.Past != nil)
			x.Equal(tc.notify, s.Notify != nil)
			if tc.notify {
				x.Equal("fleet.NotificationRecord", string(s.Own(pdpb.Own_OWN_NOTIFICATION).FullName()))
				x.Equal(uint8(40), s.Own(pdpb.Own_OWN_NOTIFICATION).Domain)
				x.Equal(uint8(41), s.Own(pdpb.Own_OWN_NOTIFICATION_RECEIPT).Domain)
			}
		})
	}
}

func TestNotifyRefusesMissingAndRenumberedCopies(t *testing.T) {
	x := require.New(t)
	declaration := "option (payday.app) = {notify: {notification: 40, receipt: 41}};\n"
	s, err := readAs(t, "fleet", declaration+ownAll(t))
	x.NoError(err)
	x.ErrorContains(pdgen.CheckOwn(s), "declares notifications")

	prefix := "import \"google/protobuf/any.proto\";\n"
	_, err = readAs(t, "fleet", prefix+ownAll(t)+notificationCopies(t, 40, 41))
	x.ErrorContains(err, "declares notify")
	_, err = readAs(t, "fleet", prefix+declaration+ownAll(t)+notificationCopies(t, 42, 41))
	x.ErrorContains(err, "declares 40 for it")

	other := `
message Other {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  option (orm.message) = {rpc: {get: {}}};
  option (payday.entity) = {domain: 40, global: {}, erase: {hard: {}}};
}
`
	_, err = readAs(t, "fleet", prefix+declaration+ownAll(t)+notificationCopies(t, 40, 41)+other)
	x.ErrorContains(err, "gives it to notifications")
}

func TestNotifyDomainCollisionsAreRefusedBeforeCopying(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    *pdpb.Notify
		past *pdpb.Past
		d    *pdpb.Domains
	}{
		{"missing", pdpb.Notify_builder{Notification: 40}.Build(), nil, nil},
		{"same", pdpb.Notify_builder{Notification: 40, Receipt: 40}.Build(), nil, nil},
		{"reserved", pdpb.Notify_builder{Notification: 2, Receipt: 41}.Build(), nil, nil},
		{"too large", pdpb.Notify_builder{Notification: 256, Receipt: 41}.Build(), nil, nil},
		{"past", pdpb.Notify_builder{Notification: 40, Receipt: 41}.Build(), pdpb.Past_builder{Event: 40, Fact: 16}.Build(), nil},
		{"domains", pdpb.Notify_builder{Notification: 40, Receipt: 41}.Build(), nil, pdpb.Domains_builder{Access: 41}.Build()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, pdgen.CheckNotifyDomains(tc.n, tc.past, tc.d))
		})
	}
}

func TestGlobalNotifyScopeIsPartOfTheGeneratedContract(t *testing.T) {
	tenant := notificationCopies(t, 40, 41)
	global := strings.ReplaceAll(tenant, `  bytes tenant_id = 2 [(orm.field) = {type: TYPE_UUID}];`, "")
	global = regexp.MustCompile(`(?s)          \{\s*name: "tenant_id"\s*number: 2\s*\},?\n`).ReplaceAllString(global, "")
	global = strings.ReplaceAll(global, `tenanted: {field: "tenant_id"}`, `global: {}`)
	declaration := `option (payday.app) = {notify: {notification: 40, receipt: 41, global: true}};`
	prefix := "import \"google/protobuf/any.proto\";\n"
	s, err := readAs(t, "fleet", prefix+declaration+ownAll(t)+global)
	require.NoError(t, err)
	require.NoError(t, pdgen.CheckOwn(s))
	for _, own := range []pdpb.Own{pdpb.Own_OWN_NOTIFICATION, pdpb.Own_OWN_NOTIFICATION_RECEIPT} {
		require.True(t, s.Own(own).IsGlobal)
		require.Nil(t, s.Own(own).Descriptor().Fields().ByNumber(2))
	}
	_, err = readAs(t, "fleet", prefix+declaration+ownAll(t)+tenant)
	require.ErrorContains(t, err, "scope differs")
	_, err = readAs(t, "fleet", prefix+strings.Replace(declaration, "global: true", "global: false", 1)+ownAll(t)+global)
	require.ErrorContains(t, err, "scope differs")
	for _, name := range []string{"NotificationRecord", "NotificationReceiptRecord"} {
		modified := strings.Replace(global, "message "+name+" {", "message "+name+" {\nbytes tenant_id = 2 [(orm.field) = {type: TYPE_UUID}];", 1)
		_, err = readAs(t, "fleet", prefix+declaration+ownAll(t)+modified)
		require.ErrorContains(t, err, "scope differs")
	}
}
