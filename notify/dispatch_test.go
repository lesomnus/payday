package notify

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"
	"github.com/stretchr/testify/require"
)

type countedResolver struct {
	Store
	count   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (s *countedResolver) Change(ctx context.Context, _ watch.Change) (Hint, bool, error) {
	s.count.Add(1)
	close(s.entered)
	select {
	case <-s.release:
		return Hint{Topics: []Topic{{Name: "arbitrary.*.>"}}}, true, nil
	case <-ctx.Done():
		return Hint{}, false, ctx.Err()
	}
}
func TestInterpretationIsOncePerEventAndIndependentOfFirstReadersCancellation(t *testing.T) {
	store := &countedResolver{entered: make(chan struct{}), release: make(chan struct{})}
	service := &Service{store: store}
	var hub watch.Fanout
	first, _ := hub.Open(t.Context(), watch.Interest{})
	defer first.Close()
	second, _ := hub.Open(t.Context(), watch.Interest{})
	defer second.Close()
	hub.Publish(watch.Event{Changes: []watch.Change{{Key: pdid.New(3)}}})
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { _, err := service.resolve(ctx, <-first.Events); require.Error(t, err) })
	<-store.entered
	cancel()
	wg.Wait()
	close(store.release)
	hints, err := service.resolve(t.Context(), <-second.Events)
	require.NoError(t, err)
	require.Equal(t, []Topic{{Name: "arbitrary.*.>"}}, hints[0].Topics)
	require.EqualValues(t, 1, store.count.Load())
}
func TestRoutesSeparateTopicKindNamespaceTenantAndReceiptReader(t *testing.T) {
	id := pdid.New(3)
	tenant := pdid.New(1)
	h := Hint{Tenant: tenant, Topics: []Topic{{Name: id.String()}}}
	base := Routes("app", h)
	require.NotEqual(t, base, Routes("another", h))
	h.Topics = []Topic{{Resource: id}}
	require.NotEqual(t, base, Routes("app", h))
	h.Topics = []Topic{{Name: id.String()}}
	h.Reader = pdid.New(2)
	require.NotEqual(t, base, Routes("app", h))
	h.Reader = pdid.Nil
	h.Tenant = pdid.New(1)
	require.NotEqual(t, base, Routes("app", h))
}
