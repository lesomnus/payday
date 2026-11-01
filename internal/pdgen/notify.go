package pdgen

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

// Notify is the app's declaration of its optional notification records. Public
// values retain the app's namespace; these domains identify their stored rows.
type Notify struct {
	Notification uint8
	Receipt      uint8
	Global       bool
	At           string

	// KeepDays is how many days a notification is kept from its publication,
	// and zero for an app that keeps them forever, which Forever says why of
	// (payday#194). RetryDays is how long its producers retry, when it says.
	KeepDays  uint32
	RetryDays uint32
	Forever   string
}

// Domain answers the domain declared for one notification record's marker.
func (n *Notify) Domain(own pdpb.Own) (uint8, bool) {
	switch {
	case n == nil:
	case own == pdpb.Own_OWN_NOTIFICATION:
		return n.Notification, true
	case own == pdpb.Own_OWN_NOTIFICATION_RECEIPT:
		return n.Receipt, true
	}
	return 0, false
}

// CheckNotify is shared by the source reader and the compiled-schema reader.
func CheckNotify(n *pdpb.Notify, past *pdpb.Past, domains *pdpb.Domains) error {
	if n == nil {
		return nil
	}
	if t := n.GetTenanted(); t != nil && (t.GetVia() != "" || t.GetStamp() != "" || len(t.GetField()) != 1 || t.GetField()[0] != "tenant_id") {
		return fmt.Errorf("notify.tenanted: notification records use field: \"tenant_id\"; via, stamp and other fields do not exist on these records")
	}
	for _, v := range []struct {
		name string
		n    uint32
	}{{"notification", n.GetNotification()}, {"receipt", n.GetReceipt()}} {
		switch {
		case v.n == 0:
			return fmt.Errorf("notify: says no `%s:`; say both notification and receipt domains in %d..%d", v.name, AppDomainMin, AppDomainMax)
		case v.n > 255:
			return fmt.Errorf("notify: %s: %d does not fit in the one byte an identifier carries", v.name, v.n)
		case v.n < AppDomainMin || v.n > AppDomainMax:
			return fmt.Errorf("notify: %s: %d is payday's; notification records take distinct app domains in %d..%d", v.name, v.n, AppDomainMin, AppDomainMax)
		}
		for _, held := range []struct {
			name string
			n    uint32
		}{
			{"past.event", past.GetEvent()}, {"past.fact", past.GetFact()},
			{"domains.archived", domains.GetArchived()}, {"domains.access", domains.GetAccess()},
		} {
			if held.n == v.n {
				return fmt.Errorf("notify: %s: %d is held by %s; give each record a domain nothing else holds", v.name, v.n, held.name)
			}
		}
	}
	if n.GetNotification() == n.GetReceipt() {
		return fmt.Errorf("notify: says %d for both notification and receipt; a domain identifies one entity", n.GetNotification())
	}
	return checkKeep(n.GetKeep())
}

// checkKeep refuses a notification store that does not say how long it keeps
// what it holds (payday#194).
//
// Required, because the two failures are not alike. Keeping notifications
// forever by default is a table of people's interests and their reading that
// grows for as long as the app runs, kept for nothing once read or stale, and
// nobody notices -- nothing fails. An app that keeps them forever on purpose
// says so, with why, which is a line somebody can find and argue with.
func checkKeep(k *pdpb.Notify_Keep) error {
	switch {
	case k == nil:
		return fmt.Errorf("notify: says no `keep:`; say how long a notification is kept from its publication, " +
			"with its receipts and its idempotency key -- `keep: {days: 90}` -- or that nothing goes, and why: " +
			"`keep: {forever: {why: \"...\"}}`")
	case k.HasDays() && k.GetDays() == 0:
		return fmt.Errorf("notify: keep: days: 0 keeps a notification for no time at all; say at least a day, or forever")
	case k.HasForever() && strings.TrimSpace(k.GetForever().GetWhy()) == "":
		return fmt.Errorf("notify: keep: forever: says no `why`, and it is the one place the schema says nothing an app publishes is let go")
	case !k.HasDays() && !k.HasForever():
		return fmt.Errorf("notify: keep: says neither `days:` nor `forever: {why: ...}`")
	case k.HasDays() && k.GetRetryDays() > k.GetDays():
		return fmt.Errorf(
			"notify: keep: retry_days: %d is longer than the %d days a notification is kept, and a retry finds its "+
				"notification by its key only while both are kept: one after %d days publishes it again. Keep it at "+
				"least as long as it is retried", k.GetRetryDays(), k.GetDays(), k.GetDays())
	}

	return nil
}

func notifyProto(n *Notify) *pdpb.Notify {
	if n == nil {
		return nil
	}
	b := pdpb.Notify_builder{Notification: uint32(n.Notification), Receipt: uint32(n.Receipt), Keep: n.keepProto()}
	if n.Global {
		b.Global = &pdpb.Entity_Global{}
	}
	return b.Build()
}

func (n *Notify) keepProto() *pdpb.Notify_Keep {
	k := pdpb.Notify_Keep_builder{RetryDays: n.RetryDays}
	switch {
	case n.KeepDays > 0:
		k.Days = &n.KeepDays
	case n.Forever != "":
		k.Forever = pdpb.Notify_Keep_Forever_builder{Why: n.Forever}.Build()
	default:
		return nil
	}

	return k.Build()
}

func pastProto(p *Past) *pdpb.Past {
	if p == nil {
		return nil
	}
	return pdpb.Past_builder{Event: uint32(p.Event), Fact: uint32(p.Fact)}.Build()
}

func readNotify(files []*protogen.File) (*Notify, error) {
	var found *Notify
	for _, f := range files {
		if !f.Generate {
			continue
		}
		a, _ := proto.GetExtension(f.Desc.Options(), pdpb.E_App).(*pdpb.App)
		if !a.HasNotify() {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%s and %s both declare notifications; say notify once, in the file that says the package is the app's", found.At, f.Desc.Path())
		}
		n := a.GetNotify()
		if err := CheckNotify(n, a.GetPast(), a.GetDomains()); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Desc.Path(), err)
		}
		found = &Notify{Notification: uint8(n.GetNotification()), Receipt: uint8(n.GetReceipt()), Global: n.HasGlobal(), At: f.Desc.Path(),
			KeepDays: n.GetKeep().GetDays(), RetryDays: n.GetKeep().GetRetryDays(), Forever: n.GetKeep().GetForever().GetWhy()}
	}
	return found, nil
}

func (s *Schema) checkNotify() error {
	features, err := schema.Features()
	if err != nil {
		return err
	}
	for _, v := range s.Entities {
		if features[v.Own] != schema.DirNotify {
			continue
		}
		if s.Notify != nil && (v.IsGlobal != s.Notify.Global || (s.Notify.Global && v.Descriptor().Fields().ByNumber(2) != nil)) {
			return fmt.Errorf("%s scope differs from notify tenancy; run pd gen to restore the record", v.FullName())
		}
		want, ok := s.Notify.Domain(v.Own)
		switch {
		case s.Notify == nil:
			return fmt.Errorf("%s is payday's %s, copied only into an app that declares notify; run `pd gen` to remove the copy or declare notify again", v.FullName(), v.Own)
		case !ok:
			return fmt.Errorf("%s is payday's %s, and notify declares no domain for it", v.FullName(), v.Own)
		case v.Domain != want:
			return fmt.Errorf("%s says domain %d, and %s declares %d for it; run `pd gen` to restore the copy", v.FullName(), v.Domain, s.Notify.At, want)
		}
	}
	if s.Notify == nil {
		return nil
	}
	for _, v := range s.Entities {
		if features[v.Own] == schema.DirNotify {
			continue
		}
		for _, held := range []struct {
			name string
			n    uint8
		}{{"NotificationRecord", s.Notify.Notification}, {"NotificationReceiptRecord", s.Notify.Receipt}} {
			if v.Domain == held.n {
				return fmt.Errorf("%s declares domain %d, and %s gives it to notifications' %s; give notify domains nothing else holds", v.FullName(), v.Domain, s.Notify.At, held.name)
			}
		}
	}
	return nil
}
