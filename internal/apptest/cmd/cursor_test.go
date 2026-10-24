package cmd_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"uuid"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/internal/ent"
	entaccess "github.com/lesomnus/payday/internal/apptest/internal/ent/access"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	entowed "github.com/lesomnus/payday/internal/apptest/internal/ent/owed"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The reads that carry on from a cursor -- a check of the rows reading the
// trail the other way round, a pass over the trail and over the access record,
// the erasures owed -- read a page at a time, and each page has to start at its
// cursor. One that starts anywhere earlier reads what every page before it
// read and throws it away, so a read of the whole is quadratic: a check of the
// rows over a deployment's million lines of the trail took three minutes of
// PostgreSQL's that way, nearly all of it in the one query that read a kind's
// lines, from the kind's first line every time.
//
// So each read is paged through here over pages that end inside an instant,
// where a cursor of the time alone would repeat a row or skip one, and must
// answer every row once and in order. On PostgreSQL each page is then run
// again under EXPLAIN ANALYZE, and must have thrown away less than a page.

// sent is a query as a read sent it, with what it was sent with.
type sent struct {
	query string
	args  []any
}

// sending is a driver that keeps the queries sent through it outside a
// transaction, so that a test can ask the database how it ran them.
//
// Not the ones inside one: a check of the rows reads each page of rows in a
// snapshot of its own, and the reads this is about are not those.
type sending struct {
	dialect.Driver

	mu   sync.Mutex
	sent []sent
}

func (d *sending) Query(ctx context.Context, query string, args, v any) error {
	if vs, ok := args.([]any); ok {
		d.mu.Lock()
		d.sent = append(d.sent, sent{query: query, args: vs})
		d.mu.Unlock()
	}

	return d.Driver.Query(ctx, query, args, v)
}

func (d *sending) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	return d.Driver.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	}).BeginTx(ctx, opts)
}

// pages is the queries of `table` that read a page -- in an order, so many at
// most -- sent since it was last asked, and none of the others. The table as
// either database quotes it.
func (d *sending) pages(table string) []sent {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := []sent{}
	for _, v := range d.sent {
		from := strings.Contains(v.query, `FROM "`+table+`"`) || strings.Contains(v.query, "FROM `"+table+"`")
		if from && strings.Contains(v.query, "ORDER BY") && strings.Contains(v.query, "LIMIT") {
			out = append(out, v)
		}
	}
	d.sent = nil

	return out
}

// paging is the app, a client of it that keeps what it sends, and the
// database under both, to ask about what it sent.
type paging struct {
	*built

	db   *ent.Client
	sent *sending
	raw  *sql.DB
}

func pagingOf(t *testing.T, c config.AuditConfig) (*paging, context.Context) {
	t.Helper()

	db := dbOf(t)
	b, ctx := buildFrom(t, cmd.Config{
		Db:    db,
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Audit: c,
	})

	raw, err := sql.Open(db.Driver, db.Dsn)
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })

	s := &sending{Driver: b.Ent.Driver()}
	return &paging{built: b, db: b.Ent.WithDriver(s), sent: s, raw: raw}, ctx
}

// instants answers when the `i`th of a test's rows was written, `per` of them
// to an instant: from an hour ago, a microsecond apart, which is as fine as
// PostgreSQL keeps a time.
func instants(per int) func(i int) time.Time {
	from := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	return func(i int) time.Time {
		return from.Add(time.Duration(i/per) * time.Microsecond)
	}
}

// lines writes `n` lines of the trail filed under the app's tenant, about
// things of kind `d` that are no row, `per` of them to an instant.
func (p *paging) lines(t *testing.T, ctx context.Context, d pdid.Domain, n, per int) {
	t.Helper()

	p.spread(t, ctx, []uuid.UUID{p.Tenant.Uuid()}, func(int) pdid.Domain { return d }, n, per)
}

// spread writes `n` lines of the trail, `per` of them to an instant, filed
// under each of `tenants` in turn and about things of kind `kind(i)`: a
// deployment's trail, which a pass that names no tenant reads across.
func (p *paging) spread(t *testing.T, ctx context.Context, tenants []uuid.UUID, kind func(i int) pdid.Domain, n, per int) {
	t.Helper()

	at := instants(per)
	qs := make([]*ent.AuditCreate, n)
	for i := range qs {
		tenant, d := tenants[i%len(tenants)], kind(i)
		qs[i] = p.Ent.Audit.Create().
			SetId(uuid.NewV7()).
			SetTenantId(tenant).
			SetActorTenantId(tenant).
			SetActorId(uuid.Nil()).
			SetTraceId([]byte{}).
			SetAction("/test/Write").
			SetObjectId(pdid.New(d).Uuid()).
			SetDomain(uint32(d)).
			SetPatch([]byte{}).
			SetValue([]byte{}).
			SetDateCreated(at(i))
	}
	for i := 0; i < n; i += 500 {
		require.NoError(t, p.Ent.Audit.CreateBulk(qs[i:min(i+500, n)]...).Exec(ctx))
	}
}

// accesses writes `n` rows of the access record, `per` of them to an instant,
// filed under each of `tenants` in turn.
func (p *paging) accesses(t *testing.T, ctx context.Context, tenants []uuid.UUID, n, per int) {
	t.Helper()

	at := instants(per)
	qs := make([]*ent.AccessCreate, n)
	for i := range qs {
		tenant := tenants[i%len(tenants)]
		qs[i] = p.Ent.Access.Create().
			SetId(pdid.New(pd.AccessDomain).Uuid()).
			SetTenantId(tenant).
			SetActorId(p.Holder.Uuid()).
			SetActorTenantId(tenant).
			SetTraceId([]byte{}).
			SetAction("/test/Read").
			SetKind("read").
			SetResult("ok").
			SetDateCreated(at(i)).
			SetValue([]byte{}).
			SetDomain(uint32(pd.AccessDomain)).
			SetSubjects([][]byte{}).
			SetSubjectCount(0)
	}
	for i := 0; i < n; i += 500 {
		require.NoError(t, p.Ent.Access.CreateBulk(qs[i:min(i+500, n)]...).Exec(ctx))
	}
}

// heads pages through what a pass reads of a scope, `limit` rows at a time,
// and answers the rows' keys in the order they came.
func (p *paging) heads(t *testing.T, ctx context.Context, of trail.Scope, limit int) []uuid.UUID {
	t.Helper()

	s := pd.TrailStore(p.db)
	at := trail.Cursor{}
	out := []uuid.UUID{}
	for {
		vs, err := s.Heads(ctx, of, time.Now(), at, limit)
		require.NoError(t, err)
		for _, v := range vs {
			out = append(out, keyOf(t, v.Key))
		}
		if len(vs) < limit {
			return out
		}

		last := vs[len(vs)-1]
		at = trail.Cursor{Created: last.Created, Key: last.Key}
	}
}

// keyOf is a key the trail store gave out, as the identifier it is: the
// access record's are a type of their own, so that what deletes by one
// deletes from the table it is of.
func keyOf(t *testing.T, k any) uuid.UUID {
	t.Helper()

	v := reflect.ValueOf(k)
	to := reflect.TypeFor[uuid.UUID]()
	require.Truef(t, v.Type().ConvertibleTo(to), "%T is no identifier", k)

	return v.Convert(to).Interface().(uuid.UUID)
}

// startsAtItsCursor runs a page again under EXPLAIN ANALYZE, on PostgreSQL,
// and fails one that threw away `limit` rows of `table` or more: a page that
// started before its cursor. What a page that starts at it throws away is the
// rows written at its cursor's instant that were on the page before.
//
// And one that answered twice a page or more: a page that did not stop at its
// end, but read every row past its cursor out of an index that is not in the
// page's order and sorted them. Such a scan throws nothing away -- the rows of
// a whole index it skips by a condition on a column after its first are not
// counted -- and reads the table. What a page that stops answers past its end
// is the rows written at its last instant, which are sorted by their keys.
//
// With sequential scans, bitmap scans and sorts of a whole input off, which is
// the plan a deployment's trail gets and not the one a test's few hundred rows
// do: a table this small is read as fast whole, or sorted as fast as read in
// order, and what is asked here is whether an index can start the read at the
// cursor and end it at the page at all.
func (p *paging) startsAtItsCursor(t *testing.T, ctx context.Context, q sent, table string, limit int) {
	t.Helper()

	tx, err := p.raw.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer tx.Rollback()

	for _, v := range []string{"SET LOCAL enable_seqscan = off", "SET LOCAL enable_bitmapscan = off", "SET LOCAL enable_sort = off"} {
		_, err := tx.ExecContext(ctx, v)
		require.NoError(t, err)
	}

	var doc []byte
	require.NoError(t, tx.QueryRowContext(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+q.query, q.args...).Scan(&doc))

	var plans []struct {
		Plan planned `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal(doc, &plans))
	require.Len(t, plans, 1)

	scans := plans[0].Plan.of(table)
	require.Len(t, scans, 1, "%s", doc)

	v := scans[0]
	require.Lessf(t, v.Removed, float64(limit),
		"a page of %d read %v rows of %s by %s %s (%s) and threw %v of them away (%s): it read from before its cursor\n\n%s",
		limit, v.Rows+v.Removed, table, v.Node, v.Index, v.Cond, v.Removed, v.Filter, q.query)
	require.Lessf(t, v.Rows, float64(2*limit),
		"a page of %d read %v rows of %s by %s %s (%s) and sorted them: it read past the end of its page\n\n%s",
		limit, v.Rows, table, v.Node, v.Index, v.Cond, q.query)
}

// planned is a node of a plan as PostgreSQL explains one in JSON, as much of
// it as says what a scan read.
type planned struct {
	Node    string    `json:"Node Type"`
	Table   string    `json:"Relation Name"`
	Index   string    `json:"Index Name"`
	Cond    string    `json:"Index Cond"`
	Filter  string    `json:"Filter"`
	Rows    float64   `json:"Actual Rows"`
	Removed float64   `json:"Rows Removed by Filter"`
	Plans   []planned `json:"Plans"`
}

// of is the nodes that read `table`.
func (v planned) of(table string) []planned {
	out := []planned{}
	if v.Table == table {
		out = append(out, v)
	}
	for _, w := range v.Plans {
		out = append(out, w.of(table)...)
	}

	return out
}

// explains is whether the database is one a test can ask for its plans.
func explains() bool {
	return os.Getenv(pdtest.Postgres) != ""
}

// TestACheckOfTheRowsReadsTheTrailFromWhereItLeftOff: the trail read the
// other way round, a kind's lines a page at a time, which is how a check finds
// the things the trail says are rows and are not. Each of these lines is about
// one, so every line read is a finding, and a line a page skipped is one
// missing from them.
func TestACheckOfTheRowsReadsTheTrailFromWhereItLeftOff(t *testing.T) {
	x := require.New(t)
	key := config.AuditConfig{Digest: config.AuditDigestConfig{Key: digestKey}}
	p, ctx := pagingOf(t, key)

	n := 3*audit.RowsPage + 100
	p.lines(t, ctx, pd.RobotDomain, n, 3)

	cs, err := key.Contents()
	x.NoError(err)

	got, err := pd.Digests(ctx, p.db, cs.Digest, trail.Policy{})
	x.NoError(err)

	vs, err := p.Ent.Audit.Query().Where(entaudit.DomainEQ(uint32(pd.RobotDomain))).All(ctx)
	x.NoError(err)
	want := []pdid.Id{}
	for _, v := range vs {
		want = append(want, pdid.Id(v.ObjectId))
	}
	x.Len(want, n)
	x.ElementsMatch(want, got.Missing, "every line about a thing that is no row, once")

	pages := p.sent.pages("audit")
	x.NotEmpty(pages)
	if !explains() {
		return
	}
	for _, v := range pages {
		p.startsAtItsCursor(t, ctx, v, "audit", audit.RowsPage)
	}
}

// TestAPassReadsTheTrailFromWhereItLeftOff: a scope's rows of the trail older
// than a cutoff, a batch at a time, as every pass over the trail reads them --
// and a pass that keeps some of them reads past those on every pass after.
func TestAPassReadsTheTrailFromWhereItLeftOff(t *testing.T) {
	x := require.New(t)
	p, ctx := pagingOf(t, config.AuditConfig{})

	n, limit := 600, 50
	p.lines(t, ctx, pd.RobotDomain, n, 3)

	got := p.heads(t, ctx, trail.Scope{Kinds: trail.Only(pd.RobotDomain), Whose: trail.FiledUnder, Tenant: p.Tenant}, limit)

	vs, err := p.Ent.Audit.Query().
		Where(entaudit.DomainEQ(uint32(pd.RobotDomain))).
		Order(ent.Asc(entaudit.FieldDateCreated, entaudit.FieldId)).
		All(ctx)
	x.NoError(err)
	want := []uuid.UUID{}
	for _, v := range vs {
		want = append(want, v.Id)
	}
	x.Len(want, n)
	x.Equal(want, got, "every row once, in the order the database keeps them in")

	pages := p.sent.pages("audit")
	x.Len(pages, n/limit+1)
	if !explains() {
		return
	}
	for _, v := range pages {
		p.startsAtItsCursor(t, ctx, v, "audit", limit)
	}
}

// TestAPassReadsTheAccessRecordFromWhereItLeftOff: the access record's rows
// are the trail store's as well (payday#111), read beside the trail's by the
// same cursor.
func TestAPassReadsTheAccessRecordFromWhereItLeftOff(t *testing.T) {
	x := require.New(t)
	p, ctx := pagingOf(t, config.AuditConfig{})

	n, limit := 600, 50
	p.accesses(t, ctx, []uuid.UUID{p.Tenant.Uuid()}, n, 3)

	got := p.heads(t, ctx, trail.Scope{Kinds: trail.Only(pd.AccessDomain), Whose: trail.FiledUnder, Tenant: p.Tenant}, limit)

	vs, err := p.Ent.Access.Query().
		Order(ent.Asc(entaccess.FieldDateCreated, entaccess.FieldId)).
		All(ctx)
	x.NoError(err)
	want := []uuid.UUID{}
	for _, v := range vs {
		want = append(want, v.Id)
	}
	x.Len(want, n)
	x.Equal(want, got, "every row once, in the order the database keeps them in")

	pages := p.sent.pages("access")
	x.Len(pages, n/limit+1)
	if !explains() {
		return
	}
	for _, v := range pages {
		p.startsAtItsCursor(t, ctx, v, "access", limit)
	}
}

// TestAPassOverTheDeploymentReadsTheTrailFromWhereItLeftOff: a scope that
// names no tenant, and no kind to read, is the trail read by time alone -- the
// default pass of a policy with no `Tenants`, every kind but the named ones,
// and `trail.Archive` and `Policy.Collect` over every kind. Every other index
// of the trail begins with what a read is narrowed by, so these had none to
// start at, and each batch read the table (payday#157).
func TestAPassOverTheDeploymentReadsTheTrailFromWhereItLeftOff(t *testing.T) {
	p, ctx := pagingOf(t, config.AuditConfig{})

	// Three tenants' lines, the deployment's own among them, in turn. Every
	// sixteenth is a robot's, which the first scope leaves out: forty of
	// them, fewer than a page, so that a page which starts at its cursor
	// throws away fewer than a page whichever of them it reads past.
	//
	// The lines the app wrote as it was set up, of its tenant and its holder,
	// are the deployment's too, and none of them is a robot's.
	setup, err := p.Ent.Audit.Query().Where(entaudit.DomainNEQ(uint32(pd.RobotDomain))).Count(ctx)
	require.NoError(t, err)

	n, limit := 640, 50
	tenants := []uuid.UUID{p.Tenant.Uuid(), uuid.NewV7(), uuid.Nil()}
	p.spread(t, ctx, tenants, func(i int) pdid.Domain {
		if i%16 == 0 {
			return pd.RobotDomain
		}
		return pd.HolderDomain
	}, n, 3)

	for _, c := range []struct {
		name string
		of   trail.Scope
		want int
	}{
		{"every kind but the named", trail.Scope{Kinds: trail.Except(pd.RobotDomain)}, setup + n - n/16},
		{"every kind", trail.Scope{}, setup + n},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)
			got := p.heads(t, ctx, c.of, limit)

			q := p.Ent.Audit.Query().Order(ent.Asc(entaudit.FieldDateCreated, entaudit.FieldId))
			if len(c.of.Except) > 0 {
				q = q.Where(entaudit.DomainNEQ(uint32(pd.RobotDomain)))
			}
			want, err := q.Ids(ctx)
			x.NoError(err)
			x.Len(want, c.want)
			x.Equal(want, got, "every row once, in the order the database keeps them in")

			pages := p.sent.pages("audit")
			x.Len(pages, c.want/limit+1)
			if !explains() {
				return
			}
			for _, v := range pages {
				p.startsAtItsCursor(t, ctx, v, "audit", limit)
			}
		})
	}
}

// TestAPassOverTheDeploymentReadsTheAccessRecordFromWhereItLeftOff: the
// access record read by time alone, as a pass that names no tenant reads it
// beside the trail (payday#157).
func TestAPassOverTheDeploymentReadsTheAccessRecordFromWhereItLeftOff(t *testing.T) {
	x := require.New(t)
	p, ctx := pagingOf(t, config.AuditConfig{})

	n, limit := 600, 50
	p.accesses(t, ctx, []uuid.UUID{p.Tenant.Uuid(), uuid.NewV7(), uuid.Nil()}, n, 3)

	got := p.heads(t, ctx, trail.Scope{Kinds: trail.Only(pd.AccessDomain)}, limit)

	want, err := p.Ent.Access.Query().
		Order(ent.Asc(entaccess.FieldDateCreated, entaccess.FieldId)).
		Ids(ctx)
	x.NoError(err)
	x.Len(want, n)
	x.Equal(want, got, "every row once, in the order the database keeps them in")

	pages := p.sent.pages("access")
	x.Len(pages, n/limit+1)
	if !explains() {
		return
	}
	for _, v := range pages {
		p.startsAtItsCursor(t, ctx, v, "access", limit)
	}
}

// TestTheErasuresOwedAreReadFromWhereTheyLeftOff: one `Owe` writes every
// object it owes at one instant, so the queue is runs of rows that share one
// and a page of it ends inside a run as a rule -- and every pass reads all of
// it, past the debts a legal hold keeps.
func TestTheErasuresOwedAreReadFromWhereTheyLeftOff(t *testing.T) {
	x := require.New(t)
	p, ctx := pagingOf(t, config.AuditConfig{})

	s := pd.TrailStore(p.db)
	n, per, limit := 600, 120, 50
	for range n / per {
		objects := make([]pdid.Id, per)
		for i := range objects {
			objects[i] = pdid.New(pd.RobotDomain)
		}
		x.NoError(s.Owe(ctx, objects))
	}

	got := []uuid.UUID{}
	at := trail.Cursor{}
	for {
		vs, err := s.Owed(ctx, at, limit)
		x.NoError(err)
		for _, v := range vs {
			got = append(got, keyOf(t, v.Key))
		}
		if len(vs) < limit {
			break
		}

		last := vs[len(vs)-1]
		at = trail.Cursor{Created: last.Created, Key: last.Key}
	}

	vs, err := p.Ent.Owed.Query().Order(ent.Asc(entowed.FieldDateCreated, entowed.FieldId)).All(ctx)
	x.NoError(err)
	want := []uuid.UUID{}
	for _, v := range vs {
		want = append(want, v.Id)
	}
	x.Len(want, n)
	x.Equal(want, got, "every debt once, oldest first")

	pages := p.sent.pages("owed")
	x.Len(pages, n/limit+1)
	if !explains() {
		return
	}
	for _, v := range pages {
		p.startsAtItsCursor(t, ctx, v, "owed", limit)
	}
}
