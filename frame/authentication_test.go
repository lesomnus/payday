package frame_test

import (
	"testing"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
)

func TestAuthenticationFreshnessAndPrincipalCopies(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		date   time.Time
		window time.Duration
		fresh  bool
	}{
		{now, time.Minute, true}, {now.Add(-time.Minute), time.Minute, true}, {now.Add(-time.Minute - time.Nanosecond), time.Minute, false},
		{time.Time{}, time.Minute, false}, {now.Add(time.Nanosecond), time.Minute, false}, {now, 0, false}, {now, -time.Minute, false},
	} {
		require.Equal(t, tc.fresh, (frame.Authentication{Method: "session", Date: tc.date}).Fresh(now, tc.window))
	}
	f := frame.New(pdid.New(200), pdid.New(200), frame.Whole())
	f.Authentication = frame.Authentication{Method: "session", Date: now}
	require.Equal(t, f.Authentication, f.Principal().Authentication)
	require.Equal(t, f.Authentication, f.Principal().Frame(frame.Whole()).Authentication)
	require.Equal(t, f.Authentication, f.WithScope(frame.Nothing).Authentication)
}
