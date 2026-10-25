package notify_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/notify"
	"github.com/lesomnus/payday/pdid"
)

type bindingPolicy struct {
	*policy
	bind func(dialect.Driver) (notify.Policy, error)
}

func (p *bindingPolicy) WithDriver(drv dialect.Driver) (notify.Policy, error) {
	return p.bind(drv)
}

var _ notify.PolicyBinder = (*bindingPolicy)(nil)

func unboundPolicy() *policy {
	return &policy{
		publisher: func(context.Context, pdid.Id, notify.Topic) (string, error) {
			return "", errors.New("unbound publisher was used")
		},
		reader: func(context.Context, pdid.Id) (pdid.Id, error) {
			return pdid.Nil, errors.New("unbound reader was used")
		},
		read: func(context.Context, pdid.Id, pdid.Id, notify.Topic) error {
			return errors.New("unbound read policy was used")
		},
	}
}

func TestNewBindsPolicyToAnExistingTransaction(t *testing.T) {
	f := setup(t)
	v := f.add(t, "one")
	drv, tx, err := dialect.BeginTx(f.ctx, f.store.Driver())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	st, err := f.store.Rebind(drv)
	if err != nil {
		t.Fatal(err)
	}
	var binds, reads int
	binder := &bindingPolicy{policy: unboundPolicy(), bind: func(actual dialect.Driver) (notify.Policy, error) {
		binds++
		if actual != drv {
			t.Fatal("constructor bound policy outside the store transaction")
		}
		return &policy{read: func(context.Context, pdid.Id, pdid.Id, notify.Topic) error {
			reads++
			return nil
		}}, nil
	}}
	service, err := notify.New(st, f.watch, binder, f.options)
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Get(f.ctx, v.ID)
	if err != nil || got.ID != v.ID || binds != 1 || reads != 1 {
		t.Fatalf("transaction-bound read: notification=%v binds=%d reads=%d error=%v", got.ID, binds, reads, err)
	}
}

func TestOwnTransactionRebindsPublisherReaderAndReadPolicy(t *testing.T) {
	f := setup(t)
	var binds, publications, readers, reads []dialect.Driver
	binder := &bindingPolicy{policy: unboundPolicy(), bind: func(drv dialect.Driver) (notify.Policy, error) {
		binds = append(binds, drv)
		// Deliberately return a plain Policy. The runtime must retain the
		// original binder instead of losing it after the constructor's bind.
		return &policy{
			publisher: func(context.Context, pdid.Id, notify.Topic) (string, error) {
				publications = append(publications, drv)
				if dialect.InTx(drv) && drv != f.store.owner.rebound {
					t.Fatal("publisher read a different driver from its write")
				}
				return "test-worker", nil
			},
			reader: func(context.Context, pdid.Id) (pdid.Id, error) {
				readers = append(readers, drv)
				if !dialect.InTx(drv) || drv != f.store.owner.rebound {
					t.Fatal("Mark resolved its reader outside its transaction")
				}
				return f.reader, nil
			},
			read: func(context.Context, pdid.Id, pdid.Id, notify.Topic) error {
				reads = append(reads, drv)
				if !dialect.InTx(drv) || drv != f.store.owner.rebound {
					t.Fatal("Mark authorized outside its transaction")
				}
				return nil
			},
		}, nil
	}}
	service, err := notify.New(f.store, f.watch, binder, f.options)
	if err != nil {
		t.Fatal(err)
	}
	v, err := service.Add(f.ctx, notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "job.completed", IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(publications) != 2 || publications[0] != f.store.Driver() || !dialect.InTx(publications[1]) {
		t.Fatalf("publisher drivers: %+v", publications)
	}
	r, err := service.Mark(f.ctx, v.ID)
	if err != nil || r.Reader != f.reader {
		t.Fatalf("transaction-bound Mark: %+v, %v", r, err)
	}
	if len(binds) != 3 || len(readers) != 2 || len(reads) != 2 || readers[0] != binds[2] || reads[1] != binds[2] {
		t.Fatalf("policy was not rebound for each write: binds=%d readers=%d reads=%d", len(binds), len(readers), len(reads))
	}
}

func TestPolicyBindingFailureClosesConstructionAndWrites(t *testing.T) {
	f := setup(t)
	want := errors.New("policy database is unavailable")
	for _, binder := range []*bindingPolicy{
		{policy: unboundPolicy(), bind: func(dialect.Driver) (notify.Policy, error) { return nil, want }},
		{policy: unboundPolicy(), bind: func(dialect.Driver) (notify.Policy, error) { return nil, nil }},
	} {
		if service, err := notify.New(f.store, f.watch, binder, f.options); service != nil || err == nil {
			t.Fatal("constructor accepted a failed policy binding")
		}
	}
	binder := &bindingPolicy{policy: unboundPolicy(), bind: func(drv dialect.Driver) (notify.Policy, error) {
		if dialect.InTx(drv) {
			return nil, want
		}
		return &policy{}, nil
	}}
	service, err := notify.New(f.store, f.watch, binder, f.options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Add(f.ctx, notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "job.completed", IdempotencyKey: "one"})
	if !errors.Is(err, want) || len(f.store.data.notifications) != 0 {
		t.Fatalf("failed transaction binding wrote a notification: %v", err)
	}
	v := f.add(t, "one")
	_, err = service.Mark(f.ctx, v.ID)
	if !errors.Is(err, want) || len(f.store.data.receipts) != 0 {
		t.Fatalf("failed transaction binding wrote a receipt: %v", err)
	}
}

func TestListenPreservesOperationalPolicyErrors(t *testing.T) {
	for _, stage := range []string{"reader", "read"} {
		for _, code := range []codes.Code{codes.Unavailable, codes.Internal, codes.DeadlineExceeded} {
			t.Run(stage+"/"+code.String(), func(t *testing.T) {
				f := setup(t)
				var fail atomic.Bool
				if stage == "reader" {
					f.policy.reader = func(context.Context, pdid.Id) (pdid.Id, error) {
						if fail.Load() {
							return pdid.Nil, status.Error(code, "policy dependency failed")
						}
						return f.reader, nil
					}
				} else {
					f.policy.read = func(context.Context, pdid.Id, pdid.Id, notify.Topic) error {
						if fail.Load() {
							return status.Error(code, "policy dependency failed")
						}
						return nil
					}
				}
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				events := make(chan notify.Event, 2)
				done := make(chan error, 1)
				go func() {
					done <- f.s.Listen(ctx, notify.ListenRequest{Tenant: f.tenant, Topics: []notify.Topic{f.topic}}, func(event notify.Event) error {
						events <- event
						return nil
					})
				}()
				if !receive(t, events).Ready {
					t.Fatal("subscription did not become ready")
				}
				fail.Store(true)
				if err := f.s.Invalidate(f.ctx, f.tenant, []notify.Topic{f.topic}); err != nil {
					t.Fatal(err)
				}
				expectCode(t, receive(t, done), code)
				select {
				case <-events:
					t.Fatal("failed authorization dependency produced a hint")
				default:
				}
			})
		}
	}
}
