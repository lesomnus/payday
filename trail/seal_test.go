package trail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"uuid"

	"filippo.io/age"
	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// sealing is a key store and a master the way an operator makes one --
// `age-keygen`, the private half kept apart -- and the policy's keys as the
// serving process holds them: the master's public half and nothing else.
func sealing(t *testing.T) (*Keys, age.Identity) {
	t.Helper()

	master, err := age.GenerateX25519Identity()
	require.NoError(t, err)

	return &Keys{Store: flob.NewMemStores(), Master: []age.Recipient{master.Recipient()}}, master
}

// raw is every row the archive holds, as it is stored: by identifier, the
// document's members.
func raw(t *testing.T, a flob.Stores) map[uuid.UUID]map[string]json.RawMessage {
	t.Helper()

	vs, err := Chunks(t.Context(), a)
	require.NoError(t, err)

	out := map[uuid.UUID]map[string]json.RawMessage{}
	for _, c := range vs {
		r, _, err := a.Use(c.Namespace).Open(t.Context(), c.Digest)
		require.NoError(t, err)

		require.NoError(t, lines(r, func(doc []byte) error {
			var v map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(doc, &v))

			var id string
			require.NoError(t, json.Unmarshal(v["id"], &id))
			b, err := base64.StdEncoding.DecodeString(id)
			require.NoError(t, err)

			out[uuid.UUID(b)] = v
			return nil
		}))
		r.Close()
	}

	return out
}

// bytesOf is every byte the archive holds, gzip and all, for asking whether
// something is in there in any form.
func bytesOf(t *testing.T, a flob.Stores) []byte {
	t.Helper()

	vs, err := Chunks(t.Context(), a)
	require.NoError(t, err)

	out := []byte{}
	for _, c := range vs {
		r, _, err := a.Use(c.Namespace).Open(t.Context(), c.Digest)
		require.NoError(t, err)

		b, err := io.ReadAll(r)
		require.NoError(t, err)
		r.Close()

		out = append(out, b...)

		// And opened, since a gzip stream is no proof of anything.
		require.NoError(t, lines(bytes.NewReader(b), func(doc []byte) error {
			out = append(out, doc...)
			return nil
		}))
	}

	return out
}

// said is what each row of a read says, by identifier.
func said(t *testing.T, read func(fn func(doc []byte) error) error) (map[uuid.UUID]string, error) {
	t.Helper()

	out := map[uuid.UUID]string{}
	err := read(func(doc []byte) error {
		h, err := headOf(doc)
		require.NoError(t, err)
		require.Empty(t, h.Sealed, "a read handed over a row still sealed")

		b, err := base64.StdEncoding.DecodeString(h.Id)
		require.NoError(t, err)
		v, err := base64.StdEncoding.DecodeString(h.Value)
		require.NoError(t, err)

		out[uuid.UUID(b)] = string(v)
		return nil
	})

	return out, err
}

func keysOf(t *testing.T, k *Keys, ns string) []key {
	t.Helper()

	vs, err := k.keysIn(t.Context(), ns)
	require.NoError(t, err)

	return vs
}

// TestWhatARowSaysIsSealedAndWhatHappenedIsNot.
//
// The contents go into the archive sealed, row by row, to the key of every
// tenant the row names, and nothing of them is anywhere in the archive's bytes.
// The event stays as it was: who acted, on what, when, and whose it is. The
// process that wrote it cannot read it back, and one given the master can.
func TestWhatARowSaysIsSealedAndWhatHappenedIsNot(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	a, b := newTenant(), newTenant()
	s := &fakeStore{}
	mine := s.add(fakeRow{tenant: a.Uuid(), domain: person, created: ago(day), value: "what a's row says"})
	theirs := s.add(fakeRow{tenant: b.Uuid(), domain: person, created: ago(day), value: "what b's row says"})
	shared := s.add(fakeRow{tenant: a.Uuid(), actor: b.Uuid(), domain: person, created: ago(day), value: "what the row they share says"})
	nobodys := s.add(fakeRow{domain: robot, created: ago(day), value: "what the deployment's row says"})
	quiet := s.add(fakeRow{tenant: a.Uuid(), domain: person, created: ago(day)})

	k, master := sealing(t)
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: time.Hour}, Keys: k}
	x.NoError(p.Valid())
	p.Pass(ctx, s)

	for _, r := range []fakeRow{mine, theirs, shared, nobodys} {
		x.False(s.has(r.id), "a row did not leave the database")
	}

	in := raw(t, p.Archive)
	for _, r := range []fakeRow{mine, theirs, shared, nobodys} {
		v := in[r.id]
		x.NotNil(v, "a row is not in the archive")
		x.NotContains(v, "value")
		x.NotContains(v, "patch")
		x.Contains(v, sealedField)
		for _, f := range []string{"id", "tenantId", "actorTenantId", "objectId", "action", "dateCreated", "domain"} {
			x.Contains(v, f, "what happened was sealed with what it said")
		}
	}
	x.NotContains(in[quiet.id], sealedField, "a row that says nothing was sealed")

	all := bytesOf(t, p.Archive)
	for _, r := range []fakeRow{mine, theirs, shared, nobodys} {
		x.NotContains(string(all), r.value)
		x.NotContains(string(all), base64.StdEncoding.EncodeToString([]byte(r.value)))
	}

	// One key each, and the deployment's for the row that names nobody.
	for _, ns := range []string{a.String(), b.String(), DeploymentNamespace} {
		x.Len(keysOf(t, k, ns), 1, "%s has no key, or more than one", ns)
	}

	t.Run("and the process that wrote it cannot read it back", func(t *testing.T) {
		x := require.New(t)

		_, err := said(t, func(fn func([]byte) error) error { return p.Read(ctx, s, fn) })
		x.ErrorIs(err, ErrSealed)
		_, err = said(t, func(fn func([]byte) error) error { return Read(ctx, p.Archive, fn) })
		x.ErrorIs(err, ErrSealed, "a read that takes no key handed something over")
	})

	t.Run("and one given the master can", func(t *testing.T) {
		x := require.New(t)

		open := p
		open.Keys = &Keys{Store: k.Store, Master: k.Master, Identities: []age.Identity{master}}

		got, err := said(t, func(fn func([]byte) error) error { return open.Read(ctx, s, fn) })
		x.NoError(err)
		for _, r := range []fakeRow{mine, theirs, shared, nobodys} {
			x.Equal(r.value, got[r.id])
		}

		// Each tenant reads its own and what it shares, opened by its own key.
		got, err = said(t, func(fn func([]byte) error) error { return open.ReadTenant(ctx, s, b, fn) })
		x.NoError(err)
		x.Equal(map[uuid.UUID]string{theirs.id: theirs.value, shared.id: shared.value}, got)

		// And a master that is not this one opens nothing.
		other, err := age.GenerateX25519Identity()
		x.NoError(err)
		open.Keys.Identities = []age.Identity{other}
		_, err = said(t, func(fn func([]byte) error) error { return open.Read(ctx, s, fn) })
		x.ErrorContains(err, "the master key given does not open it")
	})
}

// TestWhatNeedsNoKeyIsDoneWithout.
//
// Every act on the archive but reading what a row said decides by what is in
// the clear, so a process holding no master does all of them: a subject's
// erasure, a month folded, a window run out, a tenant leaving.
func TestWhatNeedsNoKeyIsDoneWithout(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	a, b := newTenant(), newTenant()
	k, master := sealing(t)
	arch := flob.NewMemStores()
	p := Policy{Archive: arch, Keep: Keep{Retain: time.Hour}, Keys: k}

	s := &fakeStore{}
	person1 := pdid.New(person)
	forgotten := s.add(fakeRow{tenant: a.Uuid(), object: person1.Uuid(), domain: person, created: ago(day), value: "about a person"})
	kept := s.add(fakeRow{tenant: a.Uuid(), domain: person, created: ago(day), value: "about something else"})
	leaving := s.add(fakeRow{tenant: b.Uuid(), domain: person, created: ago(day), value: "b's"})
	p.Pass(ctx, s)

	// A subject's erasure.
	f, err := p.Forget(ctx, s, []pdid.Id{person1})
	x.NoError(err)
	x.Equal(1, f.Archived)
	in := raw(t, arch)
	x.NotContains(in[forgotten.id], sealedField, "what was sealed outlived the erasure")
	x.Contains(in[forgotten.id], "objectId", "the erasure took the event with it")
	x.Contains(in[kept.id], sealedField)

	// A tenant leaving.
	_, err = p.PurgeTenant(ctx, s, b)
	x.NoError(err)
	x.NotContains(raw(t, arch), leaving.id)

	// A month gone, written by two passes, and folded by the second.
	month := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := range 4 {
		s.add(fakeRow{tenant: a.Uuid(), domain: robot, created: month.Add(time.Duration(i) * day), value: "old"})
		if i%2 == 1 {
			p.Pass(ctx, s)
		}
	}
	cs, err := chunksIn(ctx, arch, a.String())
	x.NoError(err)
	robots := slices.DeleteFunc(cs, func(c Chunk) bool { return c.Kind != "robot" })
	x.Len(robots, 1, "a closed month was not folded")
	x.Equal(4, robots[0].Rows)

	p.By = map[pdid.Domain]Keep{robot: {Retain: time.Hour, Destroy: 100 * day}}
	p.Pass(ctx, s)
	cs, err = chunksIn(ctx, arch, a.String())
	x.NoError(err)
	x.False(slices.ContainsFunc(cs, func(c Chunk) bool { return c.Kind == "robot" }), "a window ran out and its chunk was kept")

	// And what is left still opens.
	open := p
	open.Keys = &Keys{Store: k.Store, Master: k.Master, Identities: []age.Identity{master}}
	got, err := said(t, func(fn func([]byte) error) error { return open.Read(ctx, s, fn) })
	x.NoError(err)
	x.Equal(kept.value, got[kept.id])
}

// TestATenantThatLeavesTakesItsKeyFirst.
//
// Its key goes before anything else of it, and with the key what every copy of
// what was its alone said: the archive's, a backup's, a file somebody saved.
// The record of that is written at once, so a purge that fails after it has
// still said so. Somebody else's rows still open.
func TestATenantThatLeavesTakesItsKeyFirst(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving, staying := newTenant(), newTenant()
	k, master := sealing(t)
	arch := flob.NewMemStores()
	p := Policy{Archive: arch, Keep: Keep{Retain: time.Hour}, Keys: k}

	s := &fakeStore{}
	mine := s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: ago(day), value: "what it said"})
	theirs := s.add(fakeRow{tenant: staying.Uuid(), domain: person, created: ago(day), value: "what they said"})
	p.Pass(ctx, s)

	// A copy, as a backup of the archive keeps one.
	doc, err := json.Marshal(raw(t, arch)[mine.id])
	x.NoError(err)

	opens := &Keys{Store: k.Store, Master: k.Master, Identities: []age.Identity{master}}
	before, err := opens.Open(ctx, doc)
	x.NoError(err)
	x.Contains(string(before), base64.StdEncoding.EncodeToString([]byte(mine.value)))

	plan, err := p.PlanTenantPurge(ctx, s, leaving)
	x.NoError(err)
	x.Equal(1, plan.Keys)
	x.Len(keysOf(t, k, leaving.String()), 1, "a plan destroyed a key")

	// An archive that refuses to let go of the tenant's chunks: the purge
	// fails there, after the key.
	p.Archive = refusing{arch, leaving.String()}
	_, err = p.PurgeTenant(ctx, s, leaving)
	x.Error(err)
	x.Empty(keysOf(t, k, leaving.String()), "the key outlived a purge that got as far as the rows")

	keys := 0
	for _, v := range receiptsIn(t, arch) {
		if v.Act == "purge-tenant" && v.Where == "keys" && v.Tenant == leaving.String() {
			keys += v.Keys
		}
	}
	x.Equal(1, keys, "the key went and nothing says so")

	_, err = opens.Open(ctx, doc)
	x.ErrorIs(err, ErrKeyGone, "a copy of what it said opens after it left")

	// Asked again, it finishes the rows.
	p.Archive = arch
	got, err := p.PurgeTenant(ctx, s, leaving)
	x.NoError(err)
	x.Zero(got.Keys)
	x.NotContains(raw(t, arch), mine.id)

	open := p
	open.Keys = opens
	left, err := said(t, func(fn func([]byte) error) error { return open.Read(ctx, s, fn) })
	x.NoError(err)
	x.Equal(map[uuid.UUID]string{theirs.id: theirs.value}, left)
}

// refusing is an archive that refuses to erase anything of one namespace.
type refusing struct {
	flob.Stores
	ns string
}

func (r refusing) Use(ns string) flob.Store {
	s := r.Stores.Use(ns)
	if ns != r.ns {
		return s
	}

	return refusingStore{s}
}

type refusingStore struct{ flob.Store }

func (s refusingStore) Unwrap() flob.Store { return s.Store }

func (s refusingStore) Erase(context.Context, flob.Digest) error {
	return errors.New("the archive refused")
}

// TestAHoldKeepsWhatItHoldsReadable.
//
// A held tenant is not purged, so its key stays. And a row it shares with a
// tenant that leaves stays readable to it: the row was sealed to its key as
// well, which is what the hold is for.
func TestAHoldKeepsWhatItHoldsReadable(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving, held := newTenant(), newTenant()
	k, master := sealing(t)
	arch := flob.NewMemStores()
	p := Policy{
		Archive: arch,
		Keep:    Keep{Retain: time.Hour},
		Keys:    k,
		Tenants: answers(map[pdid.Id]Tenant{held: {Hold: onHold()}}),
	}

	s := &fakeStore{}
	shared := s.add(fakeRow{tenant: leaving.Uuid(), actor: held.Uuid(), domain: person, created: ago(day), value: "evidence"})
	p.Pass(ctx, s)

	_, err := p.PurgeTenant(ctx, s, held)
	x.ErrorIs(err, ErrHeld)
	x.Len(keysOf(t, k, held.String()), 1, "a held tenant's key was destroyed")

	_, err = p.PurgeTenant(ctx, s, leaving)
	x.NoError(err)
	x.Empty(keysOf(t, k, leaving.String()))

	in := raw(t, arch)
	x.Contains(in[shared.id], sealedField, "the row a hold is on was not kept as it was")

	open := p
	open.Keys = &Keys{Store: k.Store, Master: k.Master, Identities: []age.Identity{master}}
	got, err := said(t, func(fn func([]byte) error) error { return open.ReadTenant(ctx, s, held, fn) })
	x.NoError(err)
	x.Equal("evidence", got[shared.id], "what a hold keeps cannot be read after the other side left")
}

// TestOnlyATenantLeavingDestroysAKey.
//
// A window that destroys every chunk of a tenant leaves its key: the tenant is
// still there, and its next row is sealed to the same one. Which is also what
// the grace an app gives a tenant on its way out comes to here -- nothing
// before the purge touches the key.
func TestOnlyATenantLeavingDestroysAKey(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	k, _ := sealing(t)
	arch := flob.NewMemStores()
	p := Policy{Archive: arch, Keep: Keep{Retain: time.Hour, Destroy: 2 * time.Hour}, Keys: k}

	s := &fakeStore{}
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(year), value: "long gone"})
	p.Pass(ctx, s)
	p.Pass(ctx, s)

	x.Empty(raw(t, arch), "the window destroyed nothing")
	ks := keysOf(t, k, tenant.String())
	x.Len(ks, 1, "the window took the key with the chunks")

	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(3 * time.Hour / 2), value: "next"})
	p.Keep = Keep{Retain: time.Hour}
	p.Pass(ctx, s)
	x.Equal(ks, keysOf(t, k, tenant.String()), "a tenant with a key was given another")
}

// TestTwoKeysAreOneTenantsAndTheOldestIsSealedTo.
//
// Two replicas that find no key make one each, and both are kept: a row sealed
// to either opens, and from then on everybody seals to the older.
func TestTwoKeysAreOneTenantsAndTheOldestIsSealedTo(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	k, master := sealing(t)
	ns := tenant.String()
	x.NoError(k.make(ctx, ns))
	time.Sleep(time.Millisecond)
	x.NoError(k.make(ctx, ns))

	ks := keysOf(t, k, ns)
	x.Len(ks, 2)
	x.True(ks[0].at.Before(ks[1].at))

	r, err := newSealer(k).recipient(ctx, ns)
	x.NoError(err)
	x.Equal(ks[0].recipient, r.(*age.X25519Recipient).String())

	opens := &Keys{Store: k.Store, Master: k.Master, Identities: []age.Identity{master}}
	ids, err := opens.identities(ctx, ns)
	x.NoError(err)
	x.Len(ids, 2)
}

// TestNothingSetSealsNothing.
//
// With no keys an archive is what it was: the contents in the clear, and every
// read handing them over.
func TestNothingSetSealsNothing(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	s := &fakeStore{}
	r := s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(day), value: "plain"})

	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: time.Hour}}
	p.Pass(ctx, s)

	in := raw(t, p.Archive)
	x.Contains(in[r.id], "value")
	x.NotContains(in[r.id], sealedField)

	got, err := said(t, func(fn func([]byte) error) error { return Read(ctx, p.Archive, fn) })
	x.NoError(err)
	x.Equal("plain", got[r.id])
}

// TestAKeyConfigurationThatCannotWorkIsRefused.
func TestAKeyConfigurationThatCannotWorkIsRefused(t *testing.T) {
	x := require.New(t)

	k, _ := sealing(t)
	x.ErrorContains(Policy{Keys: k}.Valid(), "audit.archive")
	x.ErrorContains(Policy{Archive: flob.NewMemStores(), Keys: &Keys{Master: k.Master}}.Valid(), "audit.keys.dir")
	x.ErrorContains(Policy{Archive: flob.NewMemStores(), Keys: &Keys{Store: k.Store}}.Valid(), "audit.keys.master")
	x.NoError(Policy{Archive: flob.NewMemStores(), Keys: k}.Valid())
}

// TestAKeyMadeWhileATenantLeavesGoesWithIt.
//
// A pass sealing the tenant's rows at the same moment as its purge finds no key
// and makes one. The purge goes round again for rows a pass wrote meanwhile,
// and for that key too.
func TestAKeyMadeWhileATenantLeavesGoesWithIt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	leaving := newTenant()
	k, _ := sealing(t)
	arch := flob.NewMemStores()
	p := Policy{Archive: arch, Keep: Keep{Retain: time.Hour}, Keys: k}

	s := &fakeStore{}
	s.add(fakeRow{tenant: leaving.Uuid(), domain: person, created: ago(day), value: "v"})
	p.Pass(ctx, s)

	p.Archive = meanwhile{arch, leaving.String(), func() { x.NoError(k.make(ctx, leaving.String())) }}
	got, err := p.PurgeTenant(ctx, s, leaving)
	x.NoError(err)
	x.Equal(2, got.Keys)
	x.Empty(keysOf(t, k, leaving.String()), "a key made while the tenant left outlived it")
}

// meanwhile is an archive that does something else the first time a chunk of
// one namespace is erased.
type meanwhile struct {
	flob.Stores
	ns string
	fn func()
}

func (m meanwhile) Use(ns string) flob.Store {
	s := m.Stores.Use(ns)
	if ns != m.ns || m.fn == nil {
		return s
	}

	return &meanwhileStore{Store: s, fn: m.fn}
}

type meanwhileStore struct {
	flob.Store
	fn   func()
	done bool
}

func (s *meanwhileStore) Unwrap() flob.Store { return s.Store }

func (s *meanwhileStore) Erase(ctx context.Context, d flob.Digest) error {
	if !s.done {
		s.done = true
		s.fn()
	}

	return s.Store.Erase(ctx, d)
}
