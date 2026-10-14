package watch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestCustomTransactionDiscardsFailedAttempt(t *testing.T) {
	w := New(Memory())
	events, stop := w.Subscribe()
	defer stop()
	_, err := w.Unary()(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/app/Custom"}, func(ctx context.Context, _ any) (any, error) {
		err := w.Transaction(ctx, "/direct/attempt", func(ctx context.Context) error {
			w.Note(ctx, Change{Method: "rolled-back"})
			return errors.New("rollback")
		})
		require.Error(t, err)
		return nil, w.Transaction(ctx, "/direct/attempt", func(ctx context.Context) error {
			require.Equal(t, "/app/Custom", CallMethod(ctx, "fallback"))
			w.Note(ctx, Change{Method: "committed"})
			return nil
		})
	})
	require.NoError(t, err)
	got := <-events
	require.Len(t, got.Changes, 1)
	require.Equal(t, "committed", got.Changes[0].Method)
}
