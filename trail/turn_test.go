package trail

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
)

// racy is a store whose first read of rows with their documents runs `hook`
// once it has read them and before it answers: what a pass holds of the rows
// is then what they said before the hook.
type racy struct {
	*fakeStore
	once sync.Once
	hook func()
}

func (r *racy) Older(ctx context.Context, of Scope, at time.Time, after Cursor, limit int) (Rows, error) {
	vs, err := r.fakeStore.Older(ctx, of, at, after, limit)
	if len(vs) > 0 {
		r.once.Do(r.hook)
	}

	return vs, err
}

// TestAnActOnTheRowsTakesItsTurnWithAPass.
//
// A pass holds what it read of a row until the chunk it copies the row into is
// written and the row is forgotten. An erasure that blanked the row meanwhile
// and then looked through the archive before the chunk was there -- or a
// tenant's purge, or a discard by hand -- would have its work undone by the
// copy. With a lease, each changes the rows of the database in its own turn:
// one asked while a pass holds rows waits for them to be written, and once it
// answers it has left nothing behind it.
func TestAnActOnTheRowsTakesItsTurnWithAPass(t *testing.T) {
	ctx := t.Context()

	type act func(ctx context.Context, p Policy, s Store, r fakeRow) error
	for name, tc := range map[string]struct {
		act  act
		gone func(t *testing.T, p Policy, s *fakeStore, r fakeRow)
	}{
		"a subject's erasure": {
			act: func(ctx context.Context, p Policy, s Store, r fakeRow) error {
				_, err := p.Forget(ctx, s, []pdid.Id{pdid.Id(r.object)})
				return err
			},
			gone: func(t *testing.T, p Policy, s *fakeStore, r fakeRow) {
				said := base64.StdEncoding.EncodeToString([]byte(r.value))
				require.False(t, bytes.Contains(bytesOf(t, p.Archive), []byte(said)), "what the erasure blanked is in the archive")
			},
		},
		"a tenant leaving": {
			act: func(ctx context.Context, p Policy, s Store, r fakeRow) error {
				_, err := p.PurgeTenant(ctx, s, pdid.Id(r.tenant))
				return err
			},
			gone: func(t *testing.T, p Policy, s *fakeStore, r fakeRow) {
				cs, err := chunksIn(t.Context(), p.Archive, pdid.Id(r.tenant).String())
				require.NoError(t, err)
				require.Empty(t, cs, "what the tenant took with it is in the archive")
				require.False(t, s.has(r.id))
			},
		},
		"a discard by hand": {
			act: func(ctx context.Context, p Policy, s Store, r fakeRow) error {
				_, _, err := p.Collect(ctx, s, Kinds{}, time.Now())
				return err
			},
			gone: func(t *testing.T, p Policy, s *fakeStore, r fakeRow) {
				require.False(t, s.has(r.id))
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			tenant := newTenant()
			inner := &fakeStore{}
			r := inner.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day), value: "what it said"})
			p := Policy{Signer: signer, Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Lease: spin.Local()}

			// The act, asked while the pass holds the row it has read, given
			// long enough for anything that does not wait for the pass.
			var during error
			s := &racy{fakeStore: inner, hook: func() {
				soon, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()

				during = tc.act(soon, p, inner, r)
			}}
			p.Pass(ctx, s)
			x.ErrorIs(during, context.DeadlineExceeded, "it did not wait for the rows a pass was moving")

			x.NoError(tc.act(ctx, p, inner, r))
			tc.gone(t, p, inner, r)
		})
	}
}

// TestATenantThatLeftIsMadeNoKeyAgain.
//
// A tenant that leaves takes its keys, and leaves a mark where they were: when
// it left. A row written before then is sealed to the tenants it names that
// are still there -- the other side of a transfer -- and one that names nobody
// still there is not archived at all, so that no key is ever made again for
// what the tenant took with it. A row written after it is a tenant that is
// back, and gets a key of its own.
func TestATenantThatLeftIsMadeNoKeyAgain(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving, staying := newTenant(), newTenant()
	k, master := sealing(t)
	p := Policy{Signer: signer, Archive: flob.NewMemStores(), Keep: Keep{Retain: time.Hour}, Keys: k}
	opens := p
	opens.Keys = &Keys{Store: k.Store, Master: k.Master, Identities: []age.Identity{master}}

	s := &fakeStore{}
	s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: ago(day), value: "its own"})
	transfer := s.add(fakeRow{tenant: staying.Uuid(), actor: leaving.Uuid(), domain: person, created: ago(30 * time.Minute), value: "both sides"})
	p.Pass(ctx, s)

	_, err := p.PurgeTenant(ctx, s, leaving)
	x.NoError(err)
	x.Empty(keysOf(t, k, leaving.String()))

	// The other side's row of a transfer, archived after the tenant left.
	p.Keep = Keep{Retain: time.Minute}
	p.Pass(ctx, s)
	x.Empty(keysOf(t, k, leaving.String()), "a key was made again for a tenant that left")
	got, err := said(t, func(fn func([]byte) error) error { return opens.ReadTenant(ctx, s, staying, fn) })
	x.NoError(err)
	x.Equal(transfer.value, got[transfer.id], "the side still there cannot read what it shared")

	// A row of its own written before it left: what a pass read a moment
	// before its purge.
	before := s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: ago(2 * time.Hour), value: "read before it left"})
	p.Pass(ctx, s)
	x.Empty(keysOf(t, k, leaving.String()), "a key was made again for a tenant that left")
	x.NotContains(raw(t, p.Archive), before.id, "a row nobody still there may read was archived")
	x.True(s.has(before.id), "a row was taken out of the database and archived nowhere")

	// A tenant that is back.
	back := s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: time.Now(), value: "back again"})
	p.Keep = Keep{Retain: time.Nanosecond}
	p.Pass(ctx, s)
	x.Len(keysOf(t, k, leaving.String()), 1)
	got, err = said(t, func(fn func([]byte) error) error { return opens.ReadTenant(ctx, s, leaving, fn) })
	x.NoError(err)
	x.Equal(back.value, got[back.id])

	// And what it wrote before it left is not sealed to the key it has now.
	older := s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: ago(3 * time.Hour), value: "from before it left"})
	p.Pass(ctx, s)
	x.NotContains(raw(t, p.Archive), older.id, "what a tenant took with it was sealed to the key it came back with")

	t.Run("and one made as it leaves goes too", func(t *testing.T) {
		x := require.New(t)

		leaving := newTenant()
		k, _ := sealing(t)

		// The tenant leaves the moment a pass has made it a key: its mark
		// is there before the pass asks again.
		k.Store = leavingAsMade{Stores: k.Store, k: k, ns: leaving.String()}
		p := Policy{Signer: signer, Archive: flob.NewMemStores(), Keep: Keep{Retain: time.Hour}, Keys: k}

		s := &fakeStore{}
		r := s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: ago(day), value: "its own"})
		p.Pass(ctx, s)

		x.Empty(keysOf(t, k, leaving.String()), "a key made as its tenant left outlived it")
		x.NotContains(raw(t, p.Archive), r.id)
		x.True(s.has(r.id))
	})
}

// leavingAsMade is a key store in which a namespace's tenant leaves just as a
// key is made for it.
type leavingAsMade struct {
	flob.Stores
	k  *Keys
	ns string
}

func (l leavingAsMade) Use(ns string) flob.Store {
	v := l.Stores.Use(ns)
	if ns != l.ns {
		return v
	}

	return leavingAsMadeStore{Store: v, l: l}
}

func (l leavingAsMade) Unwrap() flob.Stores { return l.Stores }

type leavingAsMadeStore struct {
	flob.Store
	l leavingAsMade
}

func (s leavingAsMadeStore) Unwrap() flob.Store { return s.Store }

func (s leavingAsMadeStore) Add(ctx context.Context, m flob.Meta, r io.Reader) (flob.Meta, error) {
	v, err := s.Store.Add(ctx, m, r)
	if err == nil && m.Labels.Get(labelFormat) == formatKey {
		err = s.l.k.leave(ctx, s.l.ns)
	}

	return v, err
}
