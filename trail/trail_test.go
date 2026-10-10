package trail

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"uuid"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// Two kinds, which is what a policy per kind needs to be about something.
var (
	person = pdid.Domain(101)
	robot  = pdid.Domain(102)
)

func init() {
	pdid.Register("trail.test.Person", person, "person")
	pdid.Register("trail.test.Robot", robot, "robot")
}

// fakeRow is a row of the audit table, as much of it as the runtime reads.
type fakeRow struct {
	id          uuid.UUID
	tenant      uuid.UUID
	actor       uuid.UUID
	counterpart *uuid.UUID
	object      uuid.UUID
	domain      pdid.Domain
	created     time.Time
	value       string
}

func (r fakeRow) doc() []byte {
	b64 := func(v uuid.UUID) string { return base64.StdEncoding.EncodeToString(v[:]) }

	m := map[string]any{
		"id":            b64(r.id),
		"tenantId":      b64(r.tenant),
		"actorTenantId": b64(r.actor),
		"objectId":      b64(r.object),
		"domain":        r.domain,
		"action":        "/test/Write",
		"dateCreated":   r.created.UTC().Format(time.RFC3339Nano),
	}
	if r.counterpart != nil {
		m["counterpartTenantId"] = b64(*r.counterpart)
	}
	if r.value != "" {
		m["value"] = base64.StdEncoding.EncodeToString([]byte(r.value))
	}

	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}

	return b
}

// fakeStore is the audit table in memory, answering as the generated store
// does.
type fakeStore struct {
	mu   sync.Mutex
	rows []fakeRow
}

var _ Store = (*fakeStore)(nil)

func (s *fakeStore) add(r fakeRow) fakeRow {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.id == uuid.Nil() {
		r.id = uuid.NewV7()
	}
	if r.object == uuid.Nil() {
		r.object = uuid.NewV7()
	}
	if r.actor == uuid.Nil() && r.tenant != uuid.Nil() {
		r.actor = r.tenant
	}
	s.rows = append(s.rows, r)

	return r
}

func (s *fakeStore) has(id uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.ContainsFunc(s.rows, func(r fakeRow) bool { return r.id == id })
}

func (s *fakeStore) match(of Scope, r fakeRow) bool {
	if len(of.Only) > 0 && !slices.Contains(of.Only, r.domain) {
		return false
	}
	if len(of.Only) == 0 && slices.Contains(of.Except, r.domain) {
		return false
	}

	t, none := of.Tenant.Uuid(), uuid.Nil()
	alone := r.tenant == t &&
		(r.actor == t || r.actor == none) &&
		(r.counterpart == nil || *r.counterpart == t || *r.counterpart == none)

	switch of.Whose {
	case FiledUnder:
		return r.tenant == t
	case Alone:
		return alone
	case Together:
		names := r.tenant == t || r.actor == t || (r.counterpart != nil && *r.counterpart == t)
		return names && !alone
	default:
		return true
	}
}

func (s *fakeStore) Tenants(ctx context.Context, after pdid.Id, limit int) ([]pdid.Id, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := []pdid.Id{}
	for _, r := range s.rows {
		id := pdid.Id(r.tenant)
		if r.tenant.Compare(after.Uuid()) > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, byId)

	return out[:min(limit, len(out))], nil
}

func (s *fakeStore) Older(ctx context.Context, of Scope, at time.Time, after Cursor, limit int) (Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	vs := []fakeRow{}
	for _, r := range s.rows {
		if !s.match(of, r) || !r.created.Before(at) {
			continue
		}
		if after.Key != nil {
			k := after.Key.(uuid.UUID)
			if r.created.Before(after.Created) || (r.created.Equal(after.Created) && r.id.Compare(k) <= 0) {
				continue
			}
		}

		vs = append(vs, r)
	}
	slices.SortFunc(vs, func(a, b fakeRow) int {
		if c := a.created.Compare(b.created); c != 0 {
			return c
		}

		return a.id.Compare(b.id)
	})

	out := Rows{}
	for _, r := range vs[:min(limit, len(vs))] {
		out = append(out, Row{
			Doc:     r.doc(),
			Key:     r.id,
			Domain:  r.domain,
			Created: r.created,
			Tenants: TenantsOf(r.tenant, r.actor, r.counterpart),
		})
	}

	return out, nil
}

func (s *fakeStore) Count(ctx context.Context, of Scope, at time.Time) (int, error) {
	vs, err := s.Older(ctx, of, at, Cursor{}, 1<<30)

	return len(vs), err
}

func (s *fakeStore) Forget(ctx context.Context, keys []any) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	s.rows = slices.DeleteFunc(s.rows, func(r fakeRow) bool {
		if slices.Contains(keys, any(r.id)) {
			n++
			return true
		}

		return false
	})

	return n, nil
}

func ago(d time.Duration) time.Time { return time.Now().Add(-d) }

func newTenant() pdid.Id { return pdid.New(pdid.Domain(1)) }

// inArchive is what each namespace holds, by row identifier.
func inArchive(t *testing.T, a flob.Stores) map[string][]uuid.UUID {
	t.Helper()

	vs, err := Chunks(t.Context(), a)
	require.NoError(t, err)

	out := map[string][]uuid.UUID{}
	for _, c := range vs {
		r, _, err := a.Use(c.Namespace).Open(t.Context(), c.Digest)
		require.NoError(t, err)

		require.NoError(t, lines(r, func(doc []byte) error {
			h, err := headOf(doc)
			require.NoError(t, err)

			b, err := base64.StdEncoding.DecodeString(h.Id)
			require.NoError(t, err)

			out[c.Namespace] = append(out[c.Namespace], uuid.UUID(b))
			return nil
		}))
		r.Close()
	}

	return out
}

// TestTheMostSpecificAnswerWins.
//
// A kind before a blanket, and the tenant before the deployment. The order that
// matters is the middle one: a deployment that keeps what its machines did
// forever does not lose that to a plan that was written about people, until
// the plan names machines too.
func TestTheMostSpecificAnswerWins(t *testing.T) {
	x := require.New(t)

	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    Keep{Retain: 90 * day, Destroy: 2 * year},
		By:      map[pdid.Domain]Keep{robot: {}},
	}

	x.Equal(p.Keep, p.ForTenant(Tenant{}, person), "the zero answer is not the deployment's")
	x.Equal(Keep{}, p.ForTenant(Tenant{}, robot))

	plan := Keep{Retain: 90 * day, Destroy: 5 * year}
	x.Equal(plan, p.ForTenant(Tenant{Keep: &plan}, person))
	x.Equal(Keep{}, p.ForTenant(Tenant{Keep: &plan}, robot),
		"a tenant's blanket answer reached a kind the deployment named")

	x.Equal(plan, p.ForTenant(Tenant{By: map[pdid.Domain]Keep{robot: plan}}, robot),
		"a tenant that names the kind does not get what it named")

	t.Run("and the floor raises what comes out", func(t *testing.T) {
		x := require.New(t)

		p := p
		p.Min = Floor{Destroy: 1 * year}
		p.MinBy = map[pdid.Domain]Floor{person: {Retain: 180 * day}}

		short := Keep{Retain: 30 * day, Destroy: 60 * day}
		x.Equal(Keep{Retain: 180 * day, Destroy: 1 * year}, p.ForTenant(Tenant{Keep: &short}, person))

		x.Equal(Keep{}, p.ForTenant(Tenant{}, robot), "forever was lowered to a floor")
		x.Equal(plan, p.ForTenant(Tenant{By: map[pdid.Domain]Keep{robot: plan}}, robot),
			"an answer above the floor moved")
	})
}

// TestAFloorRaisesAnAnswerAndNeverLowersOne.
func TestAFloorRaisesAnAnswerAndNeverLowersOne(t *testing.T) {
	for _, tc := range []struct {
		name    string
		floor   Floor
		keep    Keep
		archive bool
		want    Keep
	}{
		{
			name:    "no floor is no change",
			keep:    Keep{Retain: day, Destroy: 2 * day},
			archive: true,
			want:    Keep{Retain: day, Destroy: 2 * day},
		},
		{
			name:    "forever stays forever",
			floor:   Floor{Retain: year, Destroy: 2 * year},
			keep:    Keep{},
			archive: true,
			want:    Keep{},
		},
		{
			name:    "the database half",
			floor:   Floor{Retain: 90 * day},
			keep:    Keep{Retain: day, Destroy: year},
			archive: true,
			want:    Keep{Retain: 90 * day, Destroy: year},
		},
		{
			name:    "a discard below the floor is kept in the archive for the rest of it",
			floor:   Floor{Destroy: year},
			keep:    Keep{Retain: 30 * day, Discard: true},
			archive: true,
			want:    Keep{Retain: 30 * day, Destroy: year},
		},
		{
			name:    "and in the database when there is no archive",
			floor:   Floor{Destroy: year},
			keep:    Keep{Retain: 30 * day, Discard: true},
			archive: false,
			want:    Keep{Retain: year, Discard: true},
		},
		{
			name:    "an archive window with no archive is lived out in the database",
			keep:    Keep{Retain: 30 * day, Destroy: year},
			archive: false,
			want:    Keep{Retain: year, Discard: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := tc.floor.raise(tc.keep, tc.archive)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestARowSeveralTenantsMayReadLastsAsLongAsTheLongestOfThem.
func TestARowSeveralTenantsMayReadLastsAsLongAsTheLongestOfThem(t *testing.T) {
	for _, tc := range []struct {
		name string
		ks   []Keep
		want Keep
	}{
		{
			name: "the longer of each clock",
			ks:   []Keep{{Retain: 30 * day, Destroy: year}, {Retain: 90 * day, Destroy: 2 * year}},
			want: Keep{Retain: 90 * day, Destroy: 2 * year},
		},
		{
			name: "forever beats any duration",
			ks:   []Keep{{Retain: 30 * day, Destroy: year}, {Retain: 90 * day}},
			want: Keep{Retain: 90 * day},
		},
		{
			name: "keeping beats discarding",
			ks:   []Keep{{Retain: 30 * day, Discard: true}, {Retain: 30 * day, Destroy: year}},
			want: Keep{Retain: 30 * day, Destroy: year},
		},
		{
			name: "and a discard that outlives the other's archive is a discard",
			ks:   []Keep{{Retain: 2 * year, Discard: true}, {Retain: 90 * day, Destroy: year}},
			want: Keep{Retain: 2 * year, Discard: true},
		},
		{
			name: "one that never leaves the database holds it there",
			ks:   []Keep{{}, {Retain: 30 * day, Destroy: year}},
			want: Keep{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, longest(true, tc.ks...))
			slices.Reverse(tc.ks)
			require.Equal(t, tc.want, longest(true, tc.ks...), "the order of the tenants decided it")
		})
	}
}

// TestAPolicyBelowItsOwnFloorIsRefused.
//
// A tenant's answer below the floor is raised; the deployment's own is refused,
// because it is in the same file as the floor and the person reading the error
// is the one who can fix it.
func TestAPolicyBelowItsOwnFloorIsRefused(t *testing.T) {
	x := require.New(t)

	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    Keep{Retain: 30 * day, Destroy: 180 * day},
		Min:     Floor{Destroy: year},
	}
	x.ErrorContains(p.Valid(), "audit keeps a row for 4320h0m0s in all and the floor is 8760h0m0s")

	p.Keep.Destroy = 2 * year
	x.NoError(p.Valid())

	p.MinBy = map[pdid.Domain]Floor{robot: {Retain: 90 * day}}
	x.ErrorContains(p.Valid(), "audit.retain is 720h0m0s and the floor for robot is 2160h0m0s")
}

// TestEachTenantIsSweptOnItsOwnWindow.
func TestEachTenantIsSweptOnItsOwnWindow(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	short, long := newTenant(), newTenant()

	s := &fakeStore{}
	a := s.add(fakeRow{tenant: short.Uuid(), domain: person, created: ago(60 * day)})
	b := s.add(fakeRow{tenant: long.Uuid(), domain: person, created: ago(60 * day)})
	c := s.add(fakeRow{tenant: long.Uuid(), domain: person, created: ago(200 * day)})
	nobody := s.add(fakeRow{domain: robot, created: ago(200 * day)})

	month := Keep{Retain: 30 * day, Destroy: year}
	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    Keep{Retain: 90 * day, Destroy: 2 * year},
		Tenants: func(ctx context.Context, id pdid.Id) (Tenant, error) {
			if id == short {
				return Tenant{Keep: &month}, nil
			}

			return Tenant{}, nil
		},
	}
	x.NoError(p.Valid())

	p.Pass(ctx, s)

	x.False(s.has(a.id), "the tenant on a month kept its row two")
	x.True(s.has(b.id), "the tenant on the deployment's window lost its row early")
	x.False(s.has(c.id))
	x.False(s.has(nobody.id), "the row nobody's tenant is on no window")

	held := inArchive(t, p.Archive)
	x.Equal([]uuid.UUID{a.id}, held[short.String()])
	x.Equal([]uuid.UUID{c.id}, held[long.String()])
	x.Equal([]uuid.UUID{nobody.id}, held[DeploymentNamespace])
}

// TestARowThatNamesTwoTenantsIsKeptForTheLongerOne.
//
// The transfer, and the operator of one tenant writing into another. The row is
// both of theirs to read, so neither one's contract ends it for the other.
func TestARowThatNamesTwoTenantsIsKeptForTheLongerOne(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	short, long := newTenant(), newTenant()

	s := &fakeStore{}
	both := s.add(fakeRow{tenant: short.Uuid(), actor: long.Uuid(), domain: person, created: ago(60 * day)})
	mine := s.add(fakeRow{tenant: short.Uuid(), domain: person, created: ago(60 * day)})

	month, quarter := Keep{Retain: 30 * day, Destroy: year}, Keep{Retain: 90 * day, Destroy: 2 * year}
	answers := map[pdid.Id]Keep{short: month, long: quarter}

	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    Keep{Retain: 90 * day, Destroy: 2 * year},
		Tenants: func(ctx context.Context, id pdid.Id) (Tenant, error) {
			k := answers[id]
			return Tenant{Keep: &k}, nil
		},
	}

	p.Pass(ctx, s)
	x.True(s.has(both.id), "a row the longer tenant keeps for a quarter left at a month")
	x.False(s.has(mine.id))

	t.Run("and goes to the shared namespace, saying whose", func(t *testing.T) {
		x := require.New(t)

		answers[long] = month
		p.Pass(ctx, s)
		x.False(s.has(both.id))

		cs, err := chunksIn(ctx, p.Archive, SharedNamespace)
		x.NoError(err)
		x.Len(cs, 1)

		want := []pdid.Id{short, long}
		slices.SortFunc(want, byId)
		x.Equal(want, cs[0].Tenants)
		x.Equal(1, cs[0].Rows)
	})
}

// TestATenantWithNoAnswerKeepsEverything.
//
// Falling back to the deployment's window would be a window nobody chose for
// them, and the direction it is wrong in is destruction.
func TestATenantWithNoAnswerKeepsEverything(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	broken, fine := newTenant(), newTenant()

	s := &fakeStore{}
	own := s.add(fakeRow{tenant: broken.Uuid(), domain: person, created: ago(200 * day)})
	shared := s.add(fakeRow{tenant: fine.Uuid(), actor: broken.Uuid(), domain: person, created: ago(200 * day)})
	other := s.add(fakeRow{tenant: fine.Uuid(), domain: person, created: ago(200 * day)})

	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    Keep{Retain: 90 * day, Destroy: 2 * year},
		Tenants: func(ctx context.Context, id pdid.Id) (Tenant, error) {
			if id == broken {
				return Tenant{}, errors.New("the contracts table blinked")
			}

			return Tenant{}, nil
		},
	}

	p.Pass(ctx, s)
	x.True(s.has(own.id))
	x.True(s.has(shared.id), "a row that names the tenant with no answer was moved on the other's window")
	x.False(s.has(other.id))
}

// TestTheArchiveIsDestroyedOnEachTenantsWindow.
func TestTheArchiveIsDestroyedOnEachTenantsWindow(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	short, long := newTenant(), newTenant()

	s := &fakeStore{}
	s.add(fakeRow{tenant: short.Uuid(), domain: person, created: ago(500 * day)})
	s.add(fakeRow{tenant: long.Uuid(), domain: person, created: ago(500 * day)})

	year1 := Keep{Retain: 30 * day, Destroy: year}
	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    Keep{Retain: 30 * day, Destroy: 2 * year},
		Tenants: func(ctx context.Context, id pdid.Id) (Tenant, error) {
			if id == short {
				return Tenant{Keep: &year1}, nil
			}

			return Tenant{}, nil
		},
	}

	p.Pass(ctx, s)

	held := inArchive(t, p.Archive)
	x.Empty(held[short.String()], "a tenant on a year kept a row of five hundred days")
	x.Len(held[long.String()], 1, "a tenant on two years lost a row of five hundred days")

	rs := []Receipt{}
	x.NoError(Receipts(ctx, p.Archive, func(v Receipt) error {
		rs = append(rs, v)
		return nil
	}))
	x.Len(rs, 1)
	x.Equal("destroy", rs[0].Act)
	x.Equal(short.String(), rs[0].Tenant)
	x.Equal("person", rs[0].Kind)
	x.Equal(1, rs[0].Rows)
}

// TestADiscardIsRecorded.
func TestADiscardIsRecorded(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	s := &fakeStore{}
	s.add(fakeRow{tenant: tenant.Uuid(), domain: robot, created: ago(60 * day)})

	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    Keep{Retain: 30 * day, Discard: true},
	}
	p.Pass(ctx, s)

	rs := []Receipt{}
	x.NoError(Receipts(ctx, p.Archive, func(v Receipt) error {
		rs = append(rs, v)
		return nil
	}))
	x.Len(rs, 1)
	x.Equal("discard", rs[0].Act)
	x.Equal("database", rs[0].Where)
	x.Equal(1, rs[0].Rows)
	x.False(rs[0].Before.IsZero())

	held := inArchive(t, p.Archive)
	x.Empty(held, "a discarded row was kept")
}

// TestAClosedMonthIsFoldedIntoOneChunk.
//
// A daily pass writes a chunk a day for every tenant and kind, and without this
// an archive grows a blob a day for as long as it is kept.
func TestAClosedMonthIsFoldedIntoOneChunk(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant := newTenant()
	a := flob.NewMemStores()

	// A month long enough gone that nothing more of it is in the database,
	// written by three passes; one row twice, as a crash between a write and a
	// delete leaves it.
	month := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	rows := []fakeRow{}
	for i := range 4 {
		rows = append(rows, fakeRow{
			id:      uuid.NewV7(),
			tenant:  tenant.Uuid(),
			actor:   tenant.Uuid(),
			domain:  person,
			created: month.Add(time.Duration(i) * 24 * time.Hour),
		})
	}

	ns := tenant.String()
	for i, vs := range [][]fakeRow{rows[:2], rows[1:3], rows[3:]} {
		c := Chunk{Namespace: ns, Kind: "person", Month: month, Run: "r" + string(rune('a'+i)),
			Rows: len(vs), First: vs[0].created, Last: vs[len(vs)-1].created}
		docs := [][]byte{}
		for _, r := range vs {
			docs = append(docs, r.doc())
		}

		_, err := put(ctx, a, c, docs)
		x.NoError(err)
	}

	p := Policy{Archive: a, Keep: Keep{Retain: 30 * day, Destroy: 10 * year}}
	p.Pass(ctx, &fakeStore{})

	cs, err := chunksIn(ctx, a, ns)
	x.NoError(err)
	x.Len(cs, 1, "the month was not folded")
	x.Equal(4, cs[0].Rows, "the fold kept a duplicate, or lost a row")
	x.Equal(rows[0].created.UTC(), cs[0].First.UTC())
	x.Equal(rows[3].created.UTC(), cs[0].Last.UTC())

	t.Run("and an open one is not", func(t *testing.T) {
		x := require.New(t)

		tenant := newTenant()
		ns := tenant.String()
		now := time.Now().UTC()
		for i := range 2 {
			r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), domain: person, created: now.Add(-time.Duration(i) * time.Minute)}
			_, err := put(ctx, a, Chunk{Namespace: ns, Kind: "person", Month: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
				Rows: 1, First: r.created, Last: r.created}, [][]byte{r.doc()})
			x.NoError(err)
		}

		p.Pass(ctx, &fakeStore{})

		cs, err := chunksIn(ctx, a, ns)
		x.NoError(err)
		x.Len(cs, 2, "a month still receiving rows was folded")
	})
}

// TestTheFilesOfAVersionBeforeAreAdopted.
//
// An upgrade has nothing to run: the first pass takes the directory's files in,
// by hard link, and the directory is the store's from then on.
func TestTheFilesOfAVersionBeforeAreAdopted(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	tenant := newTenant()

	r := fakeRow{id: uuid.NewV7(), tenant: tenant.Uuid(), actor: tenant.Uuid(), object: uuid.NewV7(),
		domain: person, created: time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC), value: "contents"}

	buf := &bytes.Buffer{}
	z := gzip.NewWriter(buf)
	_, err := z.Write(append(r.doc(), '\n'))
	x.NoError(err)
	x.NoError(z.Close())

	name := "audit-2020-01.person.0123456789abcdef" + Ext
	x.NoError(os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o640))
	x.NoError(os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not ours"), 0o640))

	p := Policy{Archive: flob.NewOsStores(dir)}
	p.Pass(ctx, &fakeStore{})

	_, err = os.Stat(filepath.Join(dir, name))
	x.ErrorIs(err, os.ErrNotExist, "the file was left in the directory")
	_, err = os.Stat(filepath.Join(dir, "notes.txt"))
	x.NoError(err, "a file this did not write was taken")

	cs, err := chunksIn(ctx, p.Archive, LegacyNamespace)
	x.NoError(err)
	x.Len(cs, 1)
	x.Equal("person", cs[0].Kind)
	x.Equal(name, cs[0].Name)

	seen := 0
	x.NoError(ReadTenant(ctx, p.Archive, tenant, func(doc []byte) error {
		seen++
		return nil
	}))
	x.Equal(1, seen, "the tenant's row in an adopted file was not read back")

	t.Run("and destroyed by the month in its name", func(t *testing.T) {
		x := require.New(t)

		vs, err := Doomed(ctx, p.Archive, Before(time.Date(2020, 1, 31, 0, 0, 0, 0, time.UTC)))
		x.NoError(err)
		x.Empty(vs, "January went before the month was over")

		vs, err = Purge(ctx, p.Archive, Before(time.Date(2020, 2, 1, 0, 0, 0, 0, time.UTC)))
		x.NoError(err)
		x.Len(vs, 1)
	})
}

// TestForgettingRewritesOnlyWhatItMust.
func TestForgettingRewritesOnlyWhatItMust(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant, other := newTenant(), newTenant()
	who := uuid.NewV7()

	s := &fakeStore{}
	s.add(fakeRow{tenant: tenant.Uuid(), object: who, domain: person, created: ago(60 * day), value: "a name"})
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(60 * day), value: "somebody else"})
	s.add(fakeRow{tenant: other.Uuid(), domain: person, created: ago(60 * day), value: "a third"})

	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}}
	p.Pass(ctx, s)

	before, err := chunksIn(ctx, p.Archive, other.String())
	x.NoError(err)
	x.Len(before, 1)

	n, err := Forget(ctx, p.Archive, []string{base64.StdEncoding.EncodeToString(who[:])})
	x.NoError(err)
	x.Equal(1, n)

	after, err := chunksIn(ctx, p.Archive, other.String())
	x.NoError(err)
	x.Equal(before, after, "a chunk with nothing about them in it was rewritten")

	left := 0
	x.NoError(Read(ctx, p.Archive, func(doc []byte) error {
		var v map[string]any
		x.NoError(json.Unmarshal(doc, &v))
		if v["objectId"] == base64.StdEncoding.EncodeToString(who[:]) {
			x.NotContains(v, "value")
			x.Contains(v, "action", "the event went with the contents")
			return nil
		}

		x.Contains(v, "value", "somebody else's row was blanked")
		left++
		return nil
	}))
	x.Equal(2, left)

	n, err = Forget(ctx, p.Archive, []string{base64.StdEncoding.EncodeToString(who[:])})
	x.NoError(err)
	x.Equal(1, n, "a row already blank is still one reached")
}

// TestAPreviewIsWhatAPassWouldTake.
func TestAPreviewIsWhatAPassWouldTake(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	tenant, other := newTenant(), newTenant()

	s := &fakeStore{}
	for range 3 {
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(60 * day)})
	}
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(400 * day)})
	s.add(fakeRow{tenant: tenant.Uuid(), domain: robot, created: ago(60 * day)})
	s.add(fakeRow{tenant: other.Uuid(), counterpart: z(tenant.Uuid()), domain: person, created: ago(60 * day)})
	s.add(fakeRow{tenant: other.Uuid(), domain: person, created: ago(60 * day)})

	month := Keep{Retain: 30 * day, Destroy: year}
	answers := map[pdid.Id]Tenant{other: {}}
	p := Policy{
		Archive: flob.NewMemStores(),
		Keep:    Keep{Retain: 90 * day, Destroy: 2 * year},
		By:      map[pdid.Domain]Keep{robot: {}},
		Tenants: func(ctx context.Context, id pdid.Id) (Tenant, error) { return answers[id], nil },
	}

	got, err := p.Preview(ctx, s, tenant, Tenant{Keep: &month})
	x.NoError(err)
	x.Equal(map[string]Removal{"person": {Archived: 3, Discarded: 1}}, got,
		"the robot is the deployment's forever, and the row it shares is the other's quarter")

	answers[tenant] = Tenant{Keep: &month}
	p.Pass(ctx, s)

	held := inArchive(t, p.Archive)
	x.Len(held[tenant.String()], 3, "the pass did not archive what the preview said")

	destroyed := 0
	x.NoError(Receipts(ctx, p.Archive, func(v Receipt) error {
		if v.Act == "destroy" && v.Tenant == tenant.String() {
			destroyed += v.Rows
		}

		return nil
	}))
	x.Equal(1, destroyed, "the pass did not destroy what the preview said")
}

func z[T any](v T) *T { return &v }
