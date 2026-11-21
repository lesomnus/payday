package authsession_test

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
)

type observedStore struct {
	authsession.Store
	puts, deletes int
}

func (s *observedStore) Put(ctx context.Context, v authsession.Session) error {
	s.puts++
	return s.Store.Put(ctx, v)
}

func (s *observedStore) Del(ctx context.Context, key string) error {
	s.deletes++
	return s.Store.Del(ctx, key)
}

func TestRecheckingAStreamDoesNotKeepItsSessionAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		store := &observedStore{Store: authsession.NewMemStore()}
		s := authsession.New(store, authsession.WithIdle(30*time.Minute), authsession.WithLifetime(12*time.Hour))
		v, cookie, err := s.Mint(t.Context(), authsession.Session{Id: "person", TenantId: "tenant", Grant: frame.Whole()})
		x.NoError(err)
		store.puts = 0
		ctx := carrying(cookie)
		check := s.Recheck()
		for range 5 {
			time.Sleep(5 * time.Minute)
			got, err := s.Inspect(ctx, v.Key)
			x.NoError(err)
			x.Equal(v, got)
			id, err := check.Handle(ctx)
			x.NoError(err)
			x.Equal(v.Until(), id.DateExpires)
			x.Equal(v.Id, id.Id)
			x.Equal(v.Grant, id.Grant)
		}
		time.Sleep(5 * time.Minute)
		_, err = check.Handle(ctx)
		x.ErrorIs(err, authsession.ErrNoSession)
		_, err = s.Inspect(ctx, v.Key)
		x.ErrorIs(err, authsession.ErrNoSession)
		_, err = s.Handler().Handle(ctx)
		x.ErrorIs(err, authsession.ErrNoSession, "reconnecting after the original idle deadline must not revive it")
		x.Zero(store.puts)
		x.Zero(store.deletes)
	})
}

func TestRealSessionUseStillRenewsTheIdleDeadline(t *testing.T) {
	for _, handler := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			x := require.New(t)
			s := authsession.New(authsession.NewMemStore(), authsession.WithIdle(30*time.Minute))
			v, cookie, err := s.Mint(t.Context(), authsession.Session{Id: "person", Grant: frame.Whole()})
			x.NoError(err)
			time.Sleep(20 * time.Minute)
			if handler {
				_, err = s.Handler().Handle(carrying(cookie))
			} else {
				_, err = s.Read(t.Context(), v.Key)
			}
			x.NoError(err)
			time.Sleep(15 * time.Minute)
			got, err := s.Inspect(t.Context(), v.Key)
			x.NoError(err)
			x.Equal(v.Idle.Add(20*time.Minute), got.Idle)
		})
	}
}

func TestRecheckKeepsIdentityBindingAndSessionErrors(t *testing.T) {
	x := require.New(t)
	store := &observedStore{Store: authsession.NewMemStore()}
	s := authsession.New(store, authsession.WithIdle(time.Hour))
	v := authsession.Session{Key: "key", Id: "person", Idle: time.Now().Add(5 * time.Minute), Expires: time.Now().Add(time.Hour)}
	x.NoError(store.Store.Put(t.Context(), v))
	cookie := &http.Cookie{Name: authsession.DefaultCookie, Value: v.Key}
	ctx := carrying(cookie)
	_, err := s.Recheck().Handle(context.Background())
	x.ErrorIs(err, auth.ErrNoCredential)
	wrong := metadata.NewIncomingContext(t.Context(), metadata.Pairs("cookie", cookie.String(), authsession.IdentityHeader, "another-identity"))
	_, err = s.Recheck().Handle(wrong)
	x.ErrorIs(err, authsession.ErrOtherIdentity)
	x.Zero(store.puts)
	x.NoError(store.Store.Del(t.Context(), v.Key))
	_, err = s.Recheck().Handle(ctx)
	x.ErrorIs(err, authsession.ErrNoSession)
	x.Zero(store.deletes)
	_, err = authsession.New(brokenStore{}).Recheck().Handle(ctx)
	x.ErrorIs(err, auth.ErrUnavailable)
	_, err = authsession.New(brokenStore{}).Inspect(ctx, v.Key)
	x.ErrorIs(err, auth.ErrUnavailable)
}

func TestInspectHonorsTheAbsoluteClockAndSealedSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		s := authsession.New(sealedWith(t, aKey(t)), authsession.WithLifetime(time.Hour))
		v, cookie, err := s.Mint(t.Context(), authsession.Session{Id: "person", Grant: frame.Whole()})
		x.NoError(err)
		got, err := s.Inspect(t.Context(), v.Key)
		x.NoError(err)
		x.Equal(v, got)
		time.Sleep(time.Hour)
		_, err = s.Recheck().Handle(carrying(cookie))
		x.ErrorIs(err, authsession.ErrNoSession)
	})
}
