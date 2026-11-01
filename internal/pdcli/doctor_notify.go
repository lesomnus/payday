package pdcli

import (
	"fmt"

	"github.com/lesomnus/payday/internal/pdgen"
)

// notifyPkg is the notifications' runtime.
const notifyPkg = "github.com/lesomnus/payday/notify"

// doctorNotify says when an app keeps its notifications for a number of days
// and nothing erases them after it (payday#194).
//
// It is as quiet as anything asked about here. `pd gen` writes the expirer
// the declaration asks for, and an app that never spins it serves as it always
// did: nothing fails, and the notifications and receipts the schema says go
// after so many days stay for as long as the app runs -- which is the one
// thing the declaration was made to stop.
//
// Found by what the app's own code names, as a meter reader is: a call of
// `pd.NotificationExpirer` anywhere outside a test, which builds the one
// thing there is to spin.
func doctorNotify(l Layout, s *pdgen.Schema) []Finding {
	if s.Notify == nil || s.Notify.KeepDays == 0 {
		return nil
	}
	if readWiring(l, notifyPkg, "Expirer", "NotificationExpirer").called {
		return nil
	}

	return []Finding{{
		What: fmt.Sprintf("%s keeps notifications %d days, and nothing erases them after it: `pd.NotificationExpirer` "+
			"is called nowhere, so every notification and receipt stays for as long as the app runs",
			s.Notify.At, s.Notify.KeepDays),
		Fix: `s.Spin = append(s.Spin, pd.NotificationExpirer(drv, w, content)) // the deployment's driver, the Watch, the trail's content

docs/guide/notifications.md has the rest.`,
	}}
}
