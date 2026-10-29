package notify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"
)

// Service binds an app's generated persistence to its policy and live broker.
// It holds no per-request reader identity or durable subscription state.
type Service struct {
	store     Store
	watch     *watch.Watch
	policy    Policy
	source    Policy
	key       []byte
	clock     func() time.Time
	global    bool
	namespace string
}

func New(store Store, w *watch.Watch, policy Policy, opts Options) (*Service, error) {
	if store == nil || store.Driver() == nil || policy == nil {
		return nil, errors.New("notify: store, driver and policy are required")
	}
	if len(opts.CursorKey) < 32 {
		return nil, errors.New("notify: CursorKey must contain at least 32 bytes")
	}
	bound, err := bindPolicy(policy, store.Driver())
	if err != nil {
		return nil, err
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{store: store, watch: w, policy: bound, source: policy, key: slices.Clone(opts.CursorKey), clock: clock, global: opts.Global, namespace: opts.Namespace}, nil
}

func bindPolicy(policy Policy, drv dialect.Driver) (Policy, error) {
	if binder, ok := policy.(PolicyBinder); ok {
		bound, err := binder.WithDriver(drv)
		if err != nil {
			return nil, err
		}
		if bound == nil {
			return nil, errors.New("notify: policy binder returned no policy")
		}
		return bound, nil
	}
	return policy, nil
}

// Resource names an app resource as a topic without prescribing its readers.
func Resource(id pdid.Id) Topic { return Topic{Resource: id} }

// Named names an interest scoped to a tenant, without creating a database row.
func Named(name string) Topic { return Topic{Name: name} }

// Key is the stable, indexed representation. It never conflates a named topic
// with a resource topic, including a name that itself looks like a UUID.
func (t Topic) Key() string {
	if !t.Resource.IsZero() {
		return "r:" + t.Resource.String()
	}
	return "n:" + t.Name
}

// ParseTopicKey reads the indexed representation produced by Topic.Key.
func ParseTopicKey(key string) (Topic, error) {
	if strings.HasPrefix(key, "r:") {
		id, err := pdid.Parse(key[2:])
		if err != nil || id.IsZero() || "r:"+id.String() != key {
			return Topic{}, invalid("topic resource is not a canonical payday ID")
		}
		return Resource(id), nil
	}
	if strings.HasPrefix(key, "n:") && text(key[2:], 256) {
		return Named(key[2:]), nil
	}
	return Topic{}, invalid("invalid topic key")
}

// Add publishes once within the server-established producer's idempotency
// scope. Repeating the identity with different content returns AlreadyExists.
func (s *Service) Add(ctx context.Context, req AddRequest) (Notification, error) {
	if err := s.tenant(ctx, req.Tenant); err != nil {
		return Notification{}, err
	}
	if err := topic(req.Topic); err != nil {
		return Notification{}, err
	}
	if !text(req.Kind, 128) || !text(req.IdempotencyKey, 256) {
		return Notification{}, invalid("kind and idempotency key must be nonempty UTF-8 values within their limits")
	}
	if !req.Source.IsZero() {
		if err := identifier(req.Source); err != nil {
			return Notification{}, invalid("source is not a payday ID")
		}
	}
	if req.Payload != nil && (!utf8.ValidString(req.Payload.GetTypeUrl()) || strings.ContainsRune(req.Payload.GetTypeUrl(), 0)) {
		return Notification{}, invalid("payload type URL must be UTF-8 without NUL")
	}
	producer, err := s.policy.Publisher(ctx, req.Tenant, req.Topic)
	if err != nil {
		return Notification{}, policyError(err)
	}
	if !text(producer, 256) {
		return Notification{}, status.Error(codes.FailedPrecondition, "publisher policy did not establish a stable producer")
	}
	want := Notification{Tenant: req.Tenant, Topic: req.Topic, Kind: req.Kind, Source: req.Source,
		Payload: cloneAny(req.Payload), Producer: producer, IdempotencyKey: req.IdempotencyKey}
	var result Notification
	err = s.transaction(ctx, "Add", func(ctx context.Context, at *Service) error {
		st := at.store
		if err := lock(ctx, st.Driver(), "publication", at.namespace, req.Tenant.String(), producer, req.IdempotencyKey); err != nil {
			return err
		}
		// A concurrent copy may have held the lock while authorization was
		// revoked. Retry permission is current permission, not the first copy's.
		current, err := at.policy.Publisher(ctx, req.Tenant, req.Topic)
		if err != nil {
			return policyError(err)
		}
		if current != producer {
			return status.Error(codes.PermissionDenied, "notification producer changed during publication")
		}
		existing, found, err := st.FindPublication(ctx, req.Tenant, producer, req.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !same(existing, want) {
				return status.Error(codes.AlreadyExists, "idempotency key identifies different notification content")
			}
			result = clone(existing)
			return nil
		}
		want.DateCreated = s.clock().UTC().Truncate(time.Microsecond)
		want.DateUpdated = want.DateCreated
		result, err = st.Insert(ctx, want)
		return err
	})
	return clone(result), err
}

// Get returns the shared notification and answers NotFound about both absent
// and inaccessible rows. Read state belongs to Read and Mark, not this entity.
func (s *Service) Get(ctx context.Context, id pdid.Id) (Notification, error) {
	v, _, err := s.get(ctx, s.store, id)
	return clone(v), err
}

func (s *Service) get(ctx context.Context, st Store, id pdid.Id) (Notification, pdid.Id, error) {
	if _, ok := frame.From(ctx); !ok {
		return Notification{}, pdid.Nil, status.Error(codes.Unauthenticated, "who is asking?")
	}
	if err := identifier(id); err != nil {
		return Notification{}, pdid.Nil, err
	}
	v, err := st.Find(ctx, id)
	if err != nil {
		return Notification{}, pdid.Nil, err
	}
	if err := s.tenant(ctx, v.Tenant); err != nil {
		return Notification{}, pdid.Nil, notFound()
	}
	reader, err := s.reader(ctx, v.Tenant)
	if err != nil {
		if status.Code(err) == codes.PermissionDenied || status.Code(err) == codes.NotFound {
			return Notification{}, pdid.Nil, notFound()
		}
		return Notification{}, pdid.Nil, err
	}
	if err := s.policy.Read(ctx, v.Tenant, reader, v.Topic); err != nil {
		err = policyError(err)
		if status.Code(err) == codes.PermissionDenied || status.Code(err) == codes.NotFound {
			return Notification{}, pdid.Nil, notFound()
		}
		return Notification{}, pdid.Nil, err
	}
	return v, reader, nil
}

// List pages explicitly selected topics after authorizing their complete set.
// An empty selection never means all topics the caller might be able to read.
func (s *Service) List(ctx context.Context, req Query) (Page, error) {
	page, _, err := s.page(ctx, "List", req)
	return page, err
}

// Read projects List's notifications with only the resolved reader's receipts;
// absence means unread and creates nothing.
func (s *Service) Read(ctx context.Context, req Query) (ReadPage, error) {
	page, reader, err := s.page(ctx, "Read", req)
	if err != nil {
		return ReadPage{}, err
	}
	ids := make([]pdid.Id, len(page.Items))
	for i := range page.Items {
		ids[i] = page.Items[i].ID
	}
	var receipts []Receipt
	if len(ids) > 0 {
		receipts, err = s.store.Receipts(ctx, req.Tenant, reader, ids)
		if err != nil {
			return ReadPage{}, err
		}
	}
	byID := make(map[pdid.Id]Receipt, len(receipts))
	for _, v := range receipts {
		if v.Reader != reader || v.Tenant != req.Tenant || !slices.Contains(ids, v.Notification) {
			return ReadPage{}, status.Error(codes.Internal, "receipt store returned a different read scope")
		}
		byID[v.Notification] = v
	}
	result := ReadPage{Next: page.Next, Reader: reader, Items: make([]ReadItem, len(page.Items))}
	for i, v := range page.Items {
		result.Items[i].Notification = v
		if r, ok := byID[v.ID]; ok {
			result.Items[i].Receipt = &r
		}
	}
	return result, nil
}

func (s *Service) page(ctx context.Context, method string, req Query) (Page, pdid.Id, error) {
	topics, reader, err := s.scope(ctx, req.Tenant, req.Topics)
	if err != nil {
		return Page{}, pdid.Nil, err
	}
	if req.Size < 0 {
		return Page{}, pdid.Nil, invalid("size cannot be negative")
	}
	size := req.Size
	if size == 0 {
		size = 50
	}
	size = min(size, 100)
	filter, err := normalizeFilter(req.Filter)
	if err != nil {
		return Page{}, pdid.Nil, err
	}
	if method == "List" && (len(filter.Kinds) > 0 || len(filter.Sources) > 0 || filter.UnreadOnly) {
		return Page{}, pdid.Nil, invalid("reader filters belong to Read")
	}
	selection := Selection{Tenant: req.Tenant, Topics: topics, Limit: size + 1, Reader: reader, Filter: filter}
	binding := s.scopeKey(ctx, method, req.Tenant, reader, topics, filter)
	if req.Cursor != "" {
		selection.Position, err = s.decode(req.Cursor, binding)
		if err != nil {
			return Page{}, pdid.Nil, err
		}
	}
	items, err := s.store.Select(ctx, selection)
	if err != nil {
		return Page{}, pdid.Nil, err
	}
	if len(items) > size+1 {
		return Page{}, pdid.Nil, status.Error(codes.Internal, "notification store exceeded the page limit")
	}
	for _, v := range items {
		if v.Tenant != req.Tenant || !slices.Contains(topics, v.Topic) {
			return Page{}, pdid.Nil, status.Error(codes.Internal, "notification store returned a different topic scope")
		}
	}
	page := Page{}
	if len(items) > size {
		items = items[:size]
		last := items[len(items)-1]
		page.Next, err = s.encode(Position{DateCreated: last.DateCreated, ID: last.ID}, binding)
		if err != nil {
			return Page{}, pdid.Nil, err
		}
	}
	page.Items = make([]Notification, len(items))
	for i := range items {
		page.Items[i] = clone(items[i])
	}
	return page, reader, nil
}

// Mark lazily records the server-resolved reader's first read. Retries retain
// the existing receipt and time rather than turning a read into another write.
func (s *Service) Mark(ctx context.Context, req MarkRequest) (Marked, error) {
	if err := s.tenant(ctx, req.Tenant); err != nil {
		return Marked{}, err
	}
	if len(req.Refs) == 0 || len(req.Refs) > 100 {
		return Marked{}, invalid("Mark requires 1 to 100 distinct refs")
	}
	seen := make(map[pdid.Id]bool, len(req.Refs))
	for _, id := range req.Refs {
		if err := identifier(id); err != nil {
			return Marked{}, err
		}
		if seen[id] {
			return Marked{}, invalid("duplicate refs are invalid")
		}
		seen[id] = true
	}
	var result Marked
	err := s.transaction(ctx, "Mark", func(ctx context.Context, at *Service) error {
		reader, err := at.reader(ctx, req.Tenant)
		if err != nil {
			return err
		}
		// Lock in ID order so overlapping batches cannot deadlock each other.
		ordered := slices.Clone(req.Refs)
		slices.SortFunc(ordered, func(a, b pdid.Id) int { return bytes.Compare(a.Bytes(), b.Bytes()) })
		for _, id := range ordered {
			if err := lock(ctx, at.store.Driver(), "receipt", at.namespace, id.String(), reader.String()); err != nil {
				return err
			}
		}
		values := make([]Notification, len(req.Refs))
		for i, id := range req.Refs {
			v, current, err := at.get(ctx, at.store, id)
			if err != nil {
				return err
			}
			if v.Tenant != req.Tenant {
				return notFound()
			}
			if current != reader {
				return status.Error(codes.PermissionDenied, "notification reader changed during marking")
			}
			values[i] = v
		}
		rs, err := at.store.Receipts(ctx, req.Tenant, reader, req.Refs)
		if err != nil {
			return err
		}
		byID := make(map[pdid.Id]Receipt, len(rs))
		for _, r := range rs {
			if r.Tenant != req.Tenant || r.Reader != reader || !seen[r.Notification] {
				return status.Error(codes.Internal, "receipt store returned a different read scope")
			}
			if _, duplicate := byID[r.Notification]; duplicate {
				return status.Error(codes.Internal, "duplicate receipt")
			}
			byID[r.Notification] = r
		}
		now := at.clock().UTC().Truncate(time.Microsecond)
		result = Marked{Reader: reader, Items: make([]Receipt, len(values))}
		for i, v := range values {
			r, ok := byID[v.ID]
			if !ok {
				r, err = at.store.InsertReceipt(ctx, Receipt{Tenant: req.Tenant, Notification: v.ID, Reader: reader, DataRead: now, DateCreated: now, DateUpdated: now})
				if err != nil {
					return err
				}
			}
			result.Items[i] = r
		}
		return nil
	})
	if err != nil {
		return Marked{}, err
	}
	return result, nil
}

// Count evaluates the complete reader selection in the store's single aggregate
// statement, so per-topic and distinct-source totals share one snapshot.
func (s *Service) Count(ctx context.Context, req CountRequest) (Counts, error) {
	topics, reader, err := s.scope(ctx, req.Tenant, req.Topics)
	if err != nil {
		return Counts{}, err
	}
	filter, err := normalizeFilter(req.Filter)
	if err != nil {
		return Counts{}, err
	}
	result, err := s.store.Count(ctx, Selection{Tenant: req.Tenant, Topics: topics, Reader: reader, Filter: filter})
	if err != nil {
		return Counts{}, err
	}
	byTopic := make(map[Topic]TopicCount, len(result.Topics))
	for _, v := range result.Topics {
		if !slices.Contains(topics, v.Topic) {
			return Counts{}, status.Error(codes.Internal, "count store returned another topic")
		}
		if _, duplicate := byTopic[v.Topic]; duplicate {
			return Counts{}, status.Error(codes.Internal, "duplicate topic count")
		}
		byTopic[v.Topic] = v
	}
	result.Reader = reader
	result.Topics = make([]TopicCount, len(req.Topics))
	for i, t := range req.Topics {
		result.Topics[i] = byTopic[t]
		result.Topics[i].Topic = t
	}
	return result, nil
}

func normalizeFilter(f ReadFilter) (ReadFilter, error) {
	if len(f.Kinds) > 100 || len(f.Sources) > 100 {
		return ReadFilter{}, invalid("at most 100 kinds and sources may be requested")
	}
	f.Kinds = slices.Clone(f.Kinds)
	f.Sources = slices.Clone(f.Sources)
	if len(f.Kinds) == 0 {
		f.Kinds = nil
	}
	if len(f.Sources) == 0 {
		f.Sources = nil
	}
	slices.Sort(f.Kinds)
	for i, k := range f.Kinds {
		if !text(k, 128) {
			return ReadFilter{}, invalid("invalid kind filter")
		}
		if i > 0 && k == f.Kinds[i-1] {
			return ReadFilter{}, invalid("duplicate kinds are invalid")
		}
	}
	slices.SortFunc(f.Sources, func(a, b pdid.Id) int { return bytes.Compare(a.Bytes(), b.Bytes()) })
	for i, id := range f.Sources {
		if err := identifier(id); err != nil {
			return ReadFilter{}, err
		}
		if i > 0 && id == f.Sources[i-1] {
			return ReadFilter{}, invalid("duplicate sources are invalid")
		}
	}
	return f, nil
}

func (s *Service) scope(ctx context.Context, id pdid.Id, requested []Topic) ([]Topic, pdid.Id, error) {
	if err := s.tenant(ctx, id); err != nil {
		return nil, pdid.Nil, err
	}
	ts, err := normalize(requested)
	if err != nil {
		return nil, pdid.Nil, err
	}
	reader, err := s.reader(ctx, id)
	if err != nil {
		return nil, pdid.Nil, err
	}
	for _, t := range ts {
		if err := s.policy.Read(ctx, id, reader, t); err != nil {
			return nil, pdid.Nil, policyError(err)
		}
	}
	return ts, reader, nil
}

func (s *Service) reader(ctx context.Context, id pdid.Id) (pdid.Id, error) {
	r, err := s.policy.Reader(ctx, id)
	if err != nil {
		return pdid.Nil, policyError(err)
	}
	if identifier(r) != nil {
		return pdid.Nil, status.Error(codes.FailedPrecondition, "reader policy did not establish a persistent payday identity")
	}
	return r, nil
}

func (s *Service) transaction(ctx context.Context, method string, run func(context.Context, *Service) error) error {
	joined := dialect.InTx(s.store.Driver())
	work := func(ctx context.Context) error {
		drv, tx, err := dialect.BeginTx(ctx, s.store.Driver())
		if err != nil {
			return err
		}
		defer tx.Rollback()
		st, err := s.store.Rebind(drv)
		var policy Policy
		if err == nil {
			policy, err = bindPolicy(s.source, drv)
		}
		if err == nil {
			at := *s
			at.store, at.policy = st, policy
			err = run(ctx, &at)
		}
		if err != nil {
			if joined {
				if keep, ok := dialect.KeepOf(drv); ok {
					keep.Refuse(err)
				}
			}
			return err
		}
		return tx.Commit()
	}
	// A joined Commit is a no-op. Publishing through a newly collected worker
	// context there would precede the real commit; the outer call or durable
	// outbox owns publication for that transaction.
	if s.watch == nil || joined {
		return work(ctx)
	}
	origin, ok := grpc.Method(ctx)
	if !ok {
		origin = "notify." + method
	}
	return s.watch.Transaction(ctx, origin, work)
}

func (s *Service) tenant(ctx context.Context, id pdid.Id) error {
	f, ok := frame.From(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "who is asking?")
	}
	if s.global {
		if !id.IsZero() {
			return invalid("global notifications do not accept a tenant")
		}
		return nil
	}
	if err := identifier(id); err != nil {
		return invalid("tenant is not a payday ID")
	}
	if d, ok := pdid.TenantDomain(); ok && id.Domain() != d {
		return invalid("tenant ID identifies another resource kind")
	}
	if !f.Scope.All() && !slices.Contains(f.Scope.Ids(), id) {
		return status.Error(codes.PermissionDenied, "tenant is outside the caller's scope")
	}
	return nil
}

func topic(t Topic) error {
	if t.Resource.IsZero() == (t.Name == "") {
		return invalid("topic requires exactly one resource ID or name")
	}
	if !t.Resource.IsZero() {
		if err := identifier(t.Resource); err != nil {
			return invalid("topic resource is not a payday ID")
		}
		if _, ok := pdid.Domains()[t.Resource.Domain()]; !ok {
			return invalid("topic resource domain is not registered")
		}
	} else if !text(t.Name, 256) {
		return invalid("topic name must contain 1 to 256 UTF-8 bytes")
	}
	return nil
}

func normalize(ts []Topic) ([]Topic, error) {
	if len(ts) == 0 {
		return nil, invalid("at least one topic is required")
	}
	if len(ts) > 100 {
		return nil, status.Error(codes.ResourceExhausted, "at most 100 topics may be requested")
	}
	out := slices.Clone(ts)
	seen := make(map[Topic]bool, len(out))
	for _, t := range out {
		if err := topic(t); err != nil {
			return nil, err
		}
		if seen[t] {
			return nil, invalid("duplicate topics are invalid")
		}
		seen[t] = true
	}
	slices.SortFunc(out, func(a, b Topic) int { return strings.Compare(a.Key(), b.Key()) })
	return out, nil
}

func identifier(id pdid.Id) error {
	if id.IsZero() || id.Domain() == pdid.Unknown {
		return invalid("a nonzero payday ID is required")
	}
	_, err := pdid.From(id.Bytes())
	if err != nil {
		return invalid("invalid payday ID")
	}
	return nil
}

func text(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func invalid(s string) error { return status.Error(codes.InvalidArgument, s) }
func notFound() error        { return status.Error(codes.NotFound, "notification not found") }

func policyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.PermissionDenied, "notification policy refused access")
}

func cloneAny(v *anypb.Any) *anypb.Any {
	if v == nil {
		return nil
	}
	return proto.Clone(v).(*anypb.Any)
}

func clone(v Notification) Notification {
	v.Payload = cloneAny(v.Payload)
	return v
}

func same(a, b Notification) bool {
	return a.Tenant == b.Tenant && a.Topic == b.Topic && a.Kind == b.Kind && a.Source == b.Source &&
		(a.Payload == nil) == (b.Payload == nil) && a.Payload.GetTypeUrl() == b.Payload.GetTypeUrl() && bytes.Equal(a.Payload.GetValue(), b.Payload.GetValue())
}

func lock(ctx context.Context, drv dialect.Driver, parts ...string) error {
	if drv.Dialect() != dialect.Postgres {
		return nil
	}
	h := sha256.New()
	for _, p := range append([]string{"payday.notify"}, parts...) {
		_ = binary.Write(h, binary.BigEndian, uint64(len(p)))
		_, _ = h.Write([]byte(p))
	}
	key := int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]))
	return drv.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", []any{key}, nil)
}
