package authsession_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/stretchr/testify/require"
)

func TestSessionUseNeverRefreshesAuthentication(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var store authsession.Store = authsession.NewMemStore()
			if sealed {
				store = sealedWith(t, aKey(t))
			}
			s := authsession.New(store, authsession.WithIdle(time.Hour), authsession.WithLifetime(24*time.Hour))
			for _, date := range []time.Time{time.Now().UTC().Add(-time.Hour).Add(123 * time.Nanosecond), {}} {
				original := authsession.Session{Id: "person", Grant: frame.Whole(), DateAuthenticated: date}
				v, cookie, err := s.Mint(t.Context(), original)
				require.NoError(t, err)
				require.True(t, date.Equal(v.DateAuthenticated), "Mint manufactured a proof time")
				time.Sleep(time.Minute)
				id, err := s.Handler().Handle(carrying(cookie))
				require.NoError(t, err)
				require.Equal(t, authsession.Method, id.Method)
				require.True(t, date.Equal(id.DateAuthenticated))
				require.False(t, id.Authentication().Fresh(time.Now(), 5*time.Minute))
				got, err := s.Inspect(t.Context(), cookie.Value)
				require.NoError(t, err)
				require.True(t, date.Equal(got.DateAuthenticated))
				id, err = s.Recheck().Handle(carrying(cookie))
				require.NoError(t, err)
				require.True(t, date.Equal(id.DateAuthenticated))
			}
		})
	}
}
