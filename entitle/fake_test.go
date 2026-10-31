package entitle_test

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/entitle"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// driver is a driver that only ever says whether it is a transaction.
type driver struct{ tx bool }

func (driver) Exec(context.Context, string, any, any) error  { return errors.New("not a database") }
func (driver) Query(context.Context, string, any, any) error { return errors.New("not a database") }
func (driver) Tx(context.Context) (dialect.Tx, error)        { return nil, errors.New("not a database") }
func (driver) Close() error                                  { return nil }
func (driver) Dialect() string                               { return dialect.SQLite }

// txDriver is [driver] as a transaction, which [dialect.InTx] asks of it.
type txDriver struct{ driver }

func (txDriver) InTx() bool { return true }

func (d driver) or() dialect.Driver {
	if d.tx {
		return txDriver{d}
	}

	return d
}

// act is one act a store put on the trail.
type act struct {
	action string
	tenant pdid.Id
	id     pdid.Id
}

// memCopy is a [entitle.Copy] in memory: one transaction at a time, as SQLite
// takes them, each on a copy of the rows that replaces them when it commits.
type memCopy struct {
	mu      sync.Mutex
	rows    map[pdid.Id]entitle.Row
	tenants map[pdid.Id]bool
	trail   []act

	// fail, when set, is what the next transaction answers, once.
	fail error
}

func newCopy(tenants ...pdid.Id) *memCopy {
	c := &memCopy{rows: map[pdid.Id]entitle.Row{}, tenants: map[pdid.Id]bool{}}
	for _, t := range tenants {
		c.tenants[t] = true
	}

	return c
}

func (c *memCopy) In(ctx context.Context, tenant pdid.Id, f func(ctx context.Context, s entitle.Store) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.fail; err != nil {
		c.fail = nil
		return err
	}

	s := &memStore{c: c, tenant: tenant, rows: map[pdid.Id]entitle.Row{}}
	for k, v := range c.rows {
		s.rows[k] = v
	}

	ctx = frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Grant{}).WithScope(frame.Only(tenant)))
	if err := f(ctx, s); err != nil {
		return err
	}

	c.rows = s.rows
	c.trail = append(c.trail, s.trail...)

	return nil
}

func (c *memCopy) Tenants(_ context.Context, after pdid.Id, limit int) ([]pdid.Id, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	seen := map[pdid.Id]bool{}
	for _, v := range c.rows {
		seen[v.Tenant] = true
	}
	vs := []pdid.Id{}
	for t := range seen {
		if t.Uuid().Compare(after.Uuid()) > 0 {
			vs = append(vs, t)
		}
	}
	slices.SortFunc(vs, func(a, b pdid.Id) int { return a.Uuid().Compare(b.Uuid()) })

	return vs[:min(len(vs), limit)], nil
}

// of is the rows `tenant` holds, by grant.
func (c *memCopy) of(tenant pdid.Id) map[string]entitle.Row {
	c.mu.Lock()
	defer c.mu.Unlock()

	vs := map[string]entitle.Row{}
	for _, v := range c.rows {
		if v.Tenant == tenant {
			vs[string(v.Grant)] = v
		}
	}

	return vs
}

func (c *memCopy) acts() []act {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.trail)
}

type memStore struct {
	c      *memCopy
	tenant pdid.Id
	rows   map[pdid.Id]entitle.Row
	trail  []act
}

func (s *memStore) Driver() dialect.Driver { return txDriver{} }

func (s *memStore) Rows(_ context.Context, tenant pdid.Id, key string, _ bool) ([]entitle.Row, error) {
	vs := []entitle.Row{}
	for _, v := range s.rows {
		if v.Tenant == tenant && (key == "" || v.Key == key) {
			vs = append(vs, v)
		}
	}
	slices.SortFunc(vs, func(a, b entitle.Row) int { return a.Id.Uuid().Compare(b.Id.Uuid()) })

	return vs, nil
}

func (s *memStore) Grant(_ context.Context, grant []byte) (entitle.Row, bool, error) {
	for _, v := range s.rows {
		if string(v.Grant) == string(grant) {
			return v, true, nil
		}
	}

	return entitle.Row{}, false, nil
}

func (s *memStore) Tenant(_ context.Context, tenant pdid.Id) (bool, error) {
	return s.c.tenants[tenant], nil
}

func (s *memStore) Put(_ context.Context, r entitle.Row) (pdid.Id, error) {
	if r.Id.IsZero() {
		for _, v := range s.rows {
			if string(v.Grant) == string(r.Grant) {
				return pdid.Nil, errors.New("unique: grant")
			}
		}
		r.Id = pdid.New(43)
	} else {
		was, ok := s.rows[r.Id]
		if !ok {
			return pdid.Nil, errors.New("no such row")
		}
		r.Tenant, r.Key, r.Grant = was.Tenant, was.Key, was.Grant
	}
	s.rows[r.Id] = r
	s.trail = append(s.trail, act{entitle.Applied, s.tenant, r.Id})

	return r.Id, nil
}

func (s *memStore) Forget(_ context.Context, ids []pdid.Id) error {
	for _, id := range ids {
		if _, ok := s.rows[id]; !ok {
			return errors.New("no such row")
		}
		delete(s.rows, id)
		s.trail = append(s.trail, act{entitle.Forgot, s.tenant, id})
	}

	return nil
}

// source is a `payday.GrantService` in memory: the grants it holds, and the
// streams it has open, each of which hears every grant `say` is given.
type source struct {
	pdpb.UnimplementedGrantServiceServer

	mu      sync.Mutex
	grants  map[string]*pdpb.EntitlementGrant
	streams []chan *pdpb.GrantWatchResponse
	watched chan struct{}

	// failList, when set, is what List answers from the page of that cursor.
	failList string
	noReady  bool
	page     int
}

func newSource() *source {
	return &source{grants: map[string]*pdpb.EntitlementGrant{}, watched: make(chan struct{}, 16), page: 2}
}

func (s *source) hold(gs ...*pdpb.EntitlementGrant) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, g := range gs {
		s.grants[string(g.GetId())] = g
	}
}

// say holds `g` and tells every open stream of it.
func (s *source) say(g *pdpb.EntitlementGrant) {
	s.hold(g)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.streams {
		c <- pdpb.GrantWatchResponse_builder{Grant: g}.Build()
	}
}

// drop ends every open stream.
func (s *source) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range s.streams {
		close(c)
	}
	s.streams = nil
}

func (s *source) Watch(_ *pdpb.GrantWatchRequest, out grpc.ServerStreamingServer[pdpb.GrantWatchResponse]) error {
	c := make(chan *pdpb.GrantWatchResponse, 64)
	s.mu.Lock()
	s.streams = append(s.streams, c)
	noReady := s.noReady
	s.mu.Unlock()

	if !noReady {
		if err := out.Send(pdpb.GrantWatchResponse_builder{Ready: &pdpb.GrantWatchReady{}}.Build()); err != nil {
			return err
		}
	}
	s.watched <- struct{}{}

	for {
		select {
		case <-out.Context().Done():
			return out.Context().Err()
		case m, ok := <-c:
			if !ok {
				return nil
			}
			if err := out.Send(m); err != nil {
				return err
			}
		}
	}
}

func (s *source) List(_ context.Context, req *pdpb.GrantListRequest) (*pdpb.GrantListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failList != "" && req.GetCursor() == s.failList {
		return nil, errors.New("the source fell over")
	}

	ids := []string{}
	for id, g := range s.grants {
		if len(req.GetTenantId()) == 0 || string(g.GetTenantId()) == string(req.GetTenantId()) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)

	at := 0
	if c := req.GetCursor(); c != "" {
		at, _ = strconv.Atoi(c)
	}
	end := min(at+s.page, len(ids))
	res := &pdpb.GrantListResponse{}
	for _, id := range ids[at:end] {
		res.SetItems(append(res.GetItems(), proto.Clone(s.grants[id]).(*pdpb.EntitlementGrant)))
	}
	if end < len(ids) {
		res.SetNext(strconv.Itoa(end))
	}

	return res, nil
}

// served is `s` over a connection of its own.
func served(t *testing.T, s pdpb.GrantServiceServer) pdpb.GrantServiceClient {
	t.Helper()

	l := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	pdpb.RegisterGrantServiceServer(g, s)
	go g.Serve(l)
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///source",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return l.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return pdpb.NewGrantServiceClient(conn)
}

// grant is a grant of `key` to `tenant`, in force for a day either side of
// now unless `window` says otherwise.
func grant(id string, tenant pdid.Id, key string, version uint64, v entitle.Value, window ...time.Time) *pdpb.EntitlementGrant {
	from, until := time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour)
	if len(window) == 2 {
		from, until = window[0], window[1]
	}

	b := pdpb.EntitlementGrant_builder{
		Id:         []byte(id),
		TenantId:   tenant.Bytes(),
		Key:        key,
		Version:    version,
		ValidFrom:  timestamppb.New(from),
		ValidUntil: timestamppb.New(until),
	}
	switch v.Kind() {
	case entitle.KindOn:
		b.On = proto.Bool(true)
	case entitle.KindOff:
		b.On = proto.Bool(false)
	case entitle.KindLimit:
		n, _ := v.Number()
		b.Limit = proto.Int64(n)
	case entitle.KindUnlimited:
		b.Unlimited = &pdpb.EntitlementGrant_Unlimited{}
	}

	return b.Build()
}
