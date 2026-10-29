package pdcli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"

	"github.com/lesomnus/payday/internal/pdgen"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/schema"
)

// Notify is read before compilation because it decides which notification
// sources are copied into the app, and which domains their records carry.
type Notify struct {
	Notification uint32
	Receipt      uint32
	Global       bool
	At           string
}

var notifyFiles = []pastFile{
	{"notification.proto", "NotificationRecord", pdpb.Own_OWN_NOTIFICATION},
	{"notification_receipt.proto", "NotificationReceiptRecord", pdpb.Own_OWN_NOTIFICATION_RECEIPT},
}

func (n *Notify) Domain(own pdpb.Own) uint32 {
	if n == nil {
		return 0
	}
	switch own {
	case pdpb.Own_OWN_NOTIFICATION:
		return n.Notification
	case pdpb.Own_OWN_NOTIFICATION_RECEIPT:
		return n.Receipt
	}
	return 0
}

func (n *Notify) proto() *pdpb.Notify {
	if n == nil {
		return nil
	}
	return pdpb.Notify_builder{Notification: n.Notification, Receipt: n.Receipt, Global: n.Global}.Build()
}

// NotifyDir is beside the required entity sources, in the same payday module.
func NotifyDir() (string, error) {
	d, err := SchemaDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(d), schema.DirNotify), nil
}

func readNotify(l Layout) (*Notify, error) {
	var found *Notify
	var past *pdpb.Past
	if l.Past != nil {
		past = pdpb.Past_builder{Event: l.Past.Event, Fact: l.Past.Fact}.Build()
	}
	err := l.eachOwn(func(p string, b []byte) error {
		if !protoAppAt.Match(b) {
			return nil
		}
		a, err := appOf(l.rel(p), b)
		if err != nil {
			return err
		}
		if !a.HasNotify() {
			return nil
		}
		if found != nil {
			return fmt.Errorf("%s and %s both declare notifications; say notify once, in the file that says the package is the app's", found.At, l.rel(p))
		}
		n := a.GetNotify()
		if err := pdgen.CheckNotifyDomains(n, past, a.GetDomains()); err != nil {
			return fmt.Errorf("%s: %w", l.rel(p), err)
		}
		found = &Notify{Notification: n.GetNotification(), Receipt: n.GetReceipt(), Global: n.GetGlobal(), At: l.rel(p)}
		return nil
	})
	return found, err
}

func (g Gen) notify(ctx context.Context, dst string) error {
	if g.Layout.Notify == nil {
		for _, v := range notifyFiles {
			if err := os.Remove(filepath.Join(dst, v.Name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	src, err := NotifyDir()
	if err != nil {
		return err
	}
	for _, v := range notifyFiles {
		n := g.Layout.Notify.Domain(v.Own)
		g.say("  notify: %s, domain %d", v.Message, n)
		if err := g.copyEntity(ctx, filepath.Join(src, v.Name), dst, func(b []byte) ([]byte, error) {
			b, err := numbered(b, 0, n)
			if err != nil {
				return nil, err
			}
			if g.Layout.Notify.Global {
				return globalNotificationSource(b, v.Message)
			}
			return b, nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// checkNotifyNames also protects the public values and RPC messages in the
// copied file. They share one generated Go package with all of the app's files.
func checkNotifyNames(l Layout) error {
	if l.Notify == nil {
		return nil
	}
	names := map[string]string{}
	for _, source := range notifyFiles {
		b, err := schema.FS().Open(filepath.ToSlash(filepath.Join(schema.DirNotify, source.Name)))
		if err != nil {
			return err
		}
		f, err := parser.Parse(source.Name, b, reporter.NewHandler(nil))
		b.Close()
		if err != nil {
			return err
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.MessageNode:
				names[string(d.Name.AsIdentifier())] = source.Name
			case *ast.ServiceNode:
				names[string(d.Name.AsIdentifier())] = source.Name
			}
		}
	}
	return l.eachOwn(func(p string, b []byte) error {
		base := filepath.Base(p)
		if slices.ContainsFunc(notifyFiles, func(v pastFile) bool { return v.Name == base }) {
			return fmt.Errorf("%s has the same generated Go filename as a notification copy, and %s declares notify; rename this source file", l.rel(p), l.Notify.At)
		}
		f, err := parser.Parse(p, bytes.NewReader(b), reporter.NewHandler(nil))
		if err != nil {
			return nil // buf reports malformed application sources with locations.
		}
		for _, d := range f.Decls {
			var name string
			switch d := d.(type) {
			case *ast.MessageNode:
				name = string(d.Name.AsIdentifier())
			case *ast.ServiceNode:
				name = string(d.Name.AsIdentifier())
			}
			if source, exists := names[name]; exists {
				return fmt.Errorf("%s declares %s, and %s declares notify, which copies that name into the same Go package; add application fields through %s",
					l.rel(p), name, l.Notify.At, filepath.ToSlash(filepath.Join(DirExt, "payday", strings.TrimSuffix(source, ".proto")+".ext.proto")))
			}
		}
		return nil
	})
}

// Only the private record changes scope; public tenant selectors remain present.
func globalNotificationSource(b []byte, message string) ([]byte, error) {
	at := bytes.Index(b, []byte("message "+message+" {"))
	if at < 0 {
		return nil, fmt.Errorf("missing notification record %s", message)
	}
	head, record := b[:at], string(b[at:])
	column := regexp.MustCompile(`(?m)^  bytes tenant_id = 2 \[\(orm.field\) = \{type: TYPE_UUID\}\];\n`)
	if len(column.FindAllString(record, -1)) != 1 {
		return nil, fmt.Errorf("notification tenant field is not the shipped shape")
	}
	record = column.ReplaceAllString(record, "")
	index := regexp.MustCompile(`(?s)          \{\s*name: "tenant_id"\s*number: 2\s*\},?\n`)
	record = index.ReplaceAllString(record, "")
	record = strings.Replace(record, `tenanted: {field: "tenant_id"}`, `global: {}`, 1)
	return append(bytes.Clone(head), []byte(record)...), nil
}
