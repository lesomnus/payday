package pdcli_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

func TestNotifyIsReadBeforeCompilation(t *testing.T) {
	for _, option := range []string{
		`option (payday.app) = {notify: {notification: 40, receipt: 41}};`,
		`option (payday.app).notify = {notification: 40, receipt: 41};`,
		"option (payday.app).notify.notification = 40;\noption (payday.app).notify.receipt = 41;",
		`option (payday.app) = {past: {event: 17, fact: 16}, notify: {notification: 40, receipt: 41}, domains: {archived: 42}};`,
	} {
		root := app(t, "github.com/acme/thing")
		past(t, root, "widget.proto", option)
		l, err := pdcli.Discover(root)
		require.NoError(t, err)
		require.Equal(t, &pdcli.Notify{Notification: 40, Receipt: 41, At: "proto/app/widget.proto"}, l.Notify)
	}
	root := app(t, "github.com/acme/thing")
	past(t, root, "widget.proto", `option (payday.app) = {past: {event: 17, fact: 16}};`)
	l, err := pdcli.Discover(root)
	require.NoError(t, err)
	require.Nil(t, l.Notify)
}

func TestNotifyDeclarationsAreRefusedBeforeCompilation(t *testing.T) {
	for _, option := range []string{
		`option (payday.app) = {notify: {notification: 40}};`,
		`option (payday.app) = {notify: {notification: 40, receipt: 40}};`,
		`option (payday.app) = {notify: {notification: 2, receipt: 41}};`,
		`option (payday.app) = {notify: {notification: 40, receipt: 256}};`,
		`option (payday.app) = {past: {event: 40, fact: 16}, notify: {notification: 40, receipt: 41}};`,
		`option (payday.app) = {notify: {notification: 40, receipt: 41}, domains: {access: 41}};`,
		`option (payday.app) = {notify: {notificaton: 40, receipt: 41}};`,
	} {
		root := app(t, "github.com/acme/thing")
		past(t, root, "widget.proto", option)
		_, err := pdcli.Discover(root)
		require.Error(t, err, option)
		require.Contains(t, err.Error(), "proto/app/widget.proto")
	}
}
