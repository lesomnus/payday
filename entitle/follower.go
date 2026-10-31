package entitle

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/spin"
)

// Store is the copy as a [Follower] writes it, in one transaction of one
// tenant's: what [Copy.In] hands the function it runs.
type Store interface {
	Reader

	// Grant answers the row that copies `grant`, whichever tenant's it is,
	// locked until the transaction ends; false when the copy has none.
	Grant(ctx context.Context, grant []byte) (Row, bool, error)

	// Tenant answers whether `tenant` is one of the app's tenants: one a
	// grant can be copied for.
	Tenant(ctx context.Context, tenant pdid.Id) (bool, error)

	// Put writes `r` and answers its identifier: a new row when its Id is
	// zero, and otherwise the value, the window and the version of the row of
	// that Id, whose tenant, key and grant stay its own. Either is put on the
	// trail as [Applied], in the same transaction.
	Put(ctx context.Context, r Row) (pdid.Id, error)

	// Forget takes the rows `ids` out, and puts each on the trail as gone, as
	// [Forgot], in the same transaction.
	Forget(ctx context.Context, ids []pdid.Id) error
}

// Copy is the copy as a [Follower] keeps it: `pd.EntitlementCopy(drv,
// content)`, on the app's database.
type Copy interface {
	// In runs `f` in a transaction of its own, on a [Store] of it, the
	// context framed as the deployment's work in `tenant`; it commits when `f`
	// answers nil and rolls back otherwise.
	In(ctx context.Context, tenant pdid.Id, f func(ctx context.Context, s Store) error) error

	// Tenants is every tenant the copy holds a row of, a page of at most
	// `limit` after `after` in the order of their identifiers.
	Tenants(ctx context.Context, after pdid.Id, limit int) ([]pdid.Id, error)
}

// Follower keeps the copy what the source says: a [spin.Spinner] called
// `payday.entitle.follow`, which a lease runs in one process of the
// deployment.
//
//	s.Spin = append(s.Spin, entitle.Follower{
//		Source: pdpb.NewGrantServiceClient(conn), // the billing service
//		Copy:   pd.EntitlementCopy(drv, content),
//	})
//
// It watches the source's stream, applying each grant as it is heard of, and
// once the stream says it is ready compares the copy with the whole of the
// source's list ([Follower.Reconcile]) -- at once, and then every
// [Follower.Every] for as long as the stream stays open, for whatever the
// stream missed without ending. A grant is applied by its identifier and
// version: one the copy does not have is written, a newer version of one it
// has is written over it, and one at a version it already holds, or below,
// changes nothing. So a grant heard twice -- from the stream and the list, or
// from two processes around a failover -- is applied once, and the order they
// are heard in does not matter.
//
// # What fails
//
// A stream that ends, or cannot be opened, is opened again, a second later and
// longer each time up to a minute -- and a second later again once one has
// stayed open for a minute -- and begins with a comparison; a grant heard that
// could not be written ends the stream for the same reason, since a stream
// that went on would be one that had forgotten it. Every opening reads the
// whole of the source's list, so an app dials its source with keepalive, and
// a proxy's idle timeout does not end a stream that is only quiet. A comparison that
// fails is tried again, a second later and longer each time, while the stream
// carries on. Through all of it the copy answers as it was: nothing is taken
// out of it for want of hearing from the source.
//
// A grant that is no grant -- no end, a key that is not one, a tenant or key
// moved -- is said in the log and left out, and a grant of a tenant the app
// does not have is applied to nothing. Neither stops anything.
//
// A follower missing what it is made of gives up as it starts, which ends the
// process: a copy nothing keeps is one that answers what it last heard for as
// long as its grants last, and nothing else would say so.
type Follower struct {
	// Source is the source's `payday.GrantService`.
	Source pdpb.GrantServiceClient

	// Copy is the app's copy: `pd.EntitlementCopy(drv, content)`.
	Copy Copy

	// Every is how often the copy is compared with the whole of the source's
	// list while the stream is open: an hour when zero.
	Every time.Duration
}

var (
	_ spin.Spinner = Follower{}
	_ spin.Named   = Follower{}
)

// FollowerName is what a [Follower] is called, under a lease too.
const FollowerName = "payday.entitle.follow"

func (f Follower) SpinName() string { return FollowerName }

const (
	// reconnectFirst and reconnectMost are how long a follower waits before it
	// opens a stream again, at first and at most, doubling from the one to
	// the other: a source restarting costs a second, and one down for the day
	// is asked once a minute.
	reconnectFirst = time.Second
	reconnectMost  = time.Minute

	// listPage is how many grants a comparison asks for at a time, and
	// tenantsPage how many of the copy's tenants.
	listPage    = 500
	tenantsPage = 100
)

// Spin follows the source until the context is done; see [Follower].
func (f Follower) Spin(ctx context.Context) error {
	if err := f.check(); err != nil {
		return err
	}

	wait := reconnectFirst
	for {
		began := time.Now()
		ready, err := f.follow(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if ready && time.Since(began) >= reconnectMost {
			// A stream that stayed open is a source that is there: the next
			// one is opened at once. One that keeps ending as soon as it is
			// ready is waited on longer each time, since every opening reads
			// the whole of the source's list again.
			wait = reconnectFirst
		}

		log.From(ctx).WarnContext(ctx, "entitle: the source's stream ended, and the copy answers as it was until it is followed again",
			"err", err, "again_in", wait.String())
		if !sleep(ctx, time.Now().Add(wait)) {
			return nil
		}
		wait = min(2*wait, reconnectMost)
	}
}

func (f Follower) check() error {
	switch {
	case f.Source == nil:
		return errors.New("entitle: a follower has no Source to follow")
	case f.Copy == nil:
		return errors.New("entitle: a follower has no Copy to keep")
	case f.Every < 0:
		return fmt.Errorf("entitle: a follower is told to compare every %s", f.Every)
	}

	return nil
}

func (f Follower) every() time.Duration {
	if f.Every > 0 {
		return f.Every
	}

	return time.Hour
}

// follow is one stream, from its opening to its end: what it carries applied
// as it comes, and the copy compared with the whole of the source's list once
// it is ready and every so often after. It answers whether the stream got as
// far as being ready, and why it ended.
func (f Follower) follow(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := f.Source.Watch(ctx, &pdpb.GrantWatchRequest{})
	if err != nil {
		return false, fmt.Errorf("entitle: watch the source: %w", err)
	}
	m, err := stream.Recv()
	if err != nil {
		return false, fmt.Errorf("entitle: watch the source: %w", err)
	}
	if !m.HasReady() {
		return false, errors.New("entitle: the source's stream began with something other than ready, " +
			"so what it carries cannot be told from what it missed")
	}

	// Everything changed from here on is on the stream, so a comparison
	// begun after this misses nothing between the two.
	ended := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			m, err := stream.Recv()
			if err != nil {
				ended <- err
				return
			}

			g := m.GetGrant()
			if g == nil {
				// A second ready, or something a newer source says that this
				// build does not know: nothing to apply.
				continue
			}
			if err := f.apply(ctx, g); err != nil {
				// A grant heard and not kept leaves the copy behind the
				// source, and a stream that went on would be one that had
				// forgotten it. Ended, so that the next begins with a
				// comparison.
				ended <- err
				return
			}
		}
	}()
	// The stream's applies are done before this answers, so that nothing of
	// the follower's is writing once it has stopped.
	defer wg.Wait()
	defer cancel()

	wait := reconnectFirst
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case err := <-ended:
			return true, err
		case <-ctx.Done():
			return true, nil
		case <-t.C:
		}

		x, err := f.Reconcile(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.From(ctx).WarnContext(ctx, "entitle: the copy could not be compared with the source, and is tried again",
					"err", err, "again_in", wait.String())
			}
			t.Reset(wait)
			wait = min(2*wait, reconnectMost)
			continue
		}

		x.log(ctx, "entitle: compared with the source")
		wait = reconnectFirst
		t.Reset(f.every())
	}
}

// Synced is what a comparison of the copy with the source did, or the
// application of one grant.
type Synced struct {
	// Listed is how many grants the source listed.
	Listed int

	// Applied is the grants written: ones the copy did not have, and newer
	// versions of ones it did. Heard is the ones it held at that version or a
	// newer, which changed nothing.
	Applied int
	Heard   int

	// Forgotten is the rows taken out: grants that have ended and that the
	// source lists no more. Kept is the rows the source lists no more that
	// have not ended, which the copy keeps until they do.
	Forgotten int
	Kept      int

	// Unknown is the grants of tenants the app does not have, applied to
	// nothing, and Refused the grants that are no grant.
	Unknown int
	Refused int
}

func (x *Synced) add(y Synced) {
	x.Listed += y.Listed
	x.Applied += y.Applied
	x.Heard += y.Heard
	x.Forgotten += y.Forgotten
	x.Kept += y.Kept
	x.Unknown += y.Unknown
	x.Refused += y.Refused
}

func (x Synced) log(ctx context.Context, msg string) {
	args := []any{
		"listed", x.Listed, "applied", x.Applied, "heard", x.Heard, "forgotten", x.Forgotten,
		"kept", x.Kept, "unknown", x.Unknown, "refused", x.Refused,
	}
	level := slog.LevelInfo
	if x.Kept > 0 || x.Refused > 0 {
		// Kept is a source that stopped listing grants that have not ended,
		// which is a source taking them away without saying so: the copy
		// keeps them until they end, and somebody should know.
		level = slog.LevelWarn
	}
	log.From(ctx).Log(ctx, level, msg, args...)
}

// Reconcile compares the copy with the whole of the source's list, now: each
// grant listed is applied as one heard on the stream is, and a row of a grant
// the source lists no more is taken out once that grant has ended -- and kept
// until then, since a source that means to take a grant away ends it.
//
// The whole of the list is read before the copy is touched, so that a list
// the source could not finish takes nothing out. Then a tenant at a time,
// each in a transaction of its own: one that fails is said in the answer's
// error, and the others are done.
func (f Follower) Reconcile(ctx context.Context) (Synced, error) {
	if err := f.check(); err != nil {
		return Synced{}, err
	}

	return f.pass(ctx, pdid.Nil)
}

// Sync is [Follower.Reconcile] of one tenant: what an app calls when it has
// just made a tenant the source has grants of, rather than waiting for the
// next comparison. It may be called from any process, beside a follower
// running in another: what it writes is held to the grants' versions as
// everything a follower writes is.
func (f Follower) Sync(ctx context.Context, tenant pdid.Id) (Synced, error) {
	if err := f.check(); err != nil {
		return Synced{}, err
	}
	if tenant.IsZero() {
		return Synced{}, errors.New("entitle: sync names no tenant")
	}

	return f.pass(ctx, tenant)
}

// pass is a comparison of every tenant's, when `only` is zero, and of `only`'s
// otherwise.
func (f Follower) pass(ctx context.Context, only pdid.Id) (Synced, error) {
	var x Synced

	byTenant := map[pdid.Id][]Row{}
	listed := map[string]bool{}
	cursor := ""
	for {
		req := pdpb.GrantListRequest_builder{Size: listPage, Cursor: cursor}.Build()
		if !only.IsZero() {
			req.SetTenantId(only.Bytes())
		}

		res, err := f.Source.List(ctx, req)
		if err != nil {
			return x, fmt.Errorf("entitle: list the source's grants: %w", err)
		}
		for _, g := range res.GetItems() {
			x.Listed++
			listed[string(g.GetId())] = true

			v, err := grantOf(g)
			switch {
			case err != nil:
				x.Refused++
				log.From(ctx).WarnContext(ctx, "entitle: the source listed a grant that is not one, and it is not applied",
					"grant", hex.EncodeToString(g.GetId()), "err", err)
				continue
			case !only.IsZero() && v.Tenant != only:
				x.Refused++
				log.From(ctx).WarnContext(ctx, "entitle: the source listed another tenant's grant when asked for one tenant's",
					"grant", hex.EncodeToString(g.GetId()), "tenant", v.Tenant, "asked", only)
				continue
			}
			byTenant[v.Tenant] = append(byTenant[v.Tenant], v)
		}

		next := res.GetNext()
		switch {
		case next == "":
		case next == cursor:
			return x, errors.New("entitle: the source answered a page with the cursor it was asked for, so its list does not end")
		default:
			cursor = next
			continue
		}
		break
	}

	// Every tenant either side has: the list's, and the copy's, whose rows
	// the list may no longer name.
	tenants := map[pdid.Id]bool{}
	for t := range byTenant {
		tenants[t] = true
	}
	if only.IsZero() {
		after := pdid.Nil
		for {
			vs, err := f.Copy.Tenants(ctx, after, tenantsPage)
			if err != nil {
				return x, fmt.Errorf("entitle: the copy's tenants: %w", err)
			}
			for _, t := range vs {
				tenants[t] = true
			}
			if len(vs) < tenantsPage {
				break
			}
			after = vs[len(vs)-1]
		}
	} else {
		tenants[only] = true
	}

	now := time.Now()
	var errs []error
	for _, t := range slices.SortedFunc(func(yield func(pdid.Id) bool) {
		for t := range tenants {
			if !yield(t) {
				return
			}
		}
	}, func(a, b pdid.Id) int { return a.Uuid().Compare(b.Uuid()) }) {
		if ctx.Err() != nil {
			return x, ctx.Err()
		}

		y, err := f.tenant(ctx, t, newest(byTenant[t]), listed, now)
		x.add(y)
		if err != nil {
			errs = append(errs, fmt.Errorf("entitle: tenant %s: %w", t, err))
		}
	}

	return x, errors.Join(errs...)
}

// tenant compares one tenant's rows with `grants`, the source's grants of it,
// in one transaction: each applied, and each row of a grant `listed` does not
// name taken out if it has ended by `now`.
func (f Follower) tenant(ctx context.Context, tenant pdid.Id, grants []Row, listed map[string]bool, now time.Time) (Synced, error) {
	var x Synced
	err := f.in(ctx, tenant, func(ctx context.Context, s Store) error {
		x = Synced{}

		ok, err := s.Tenant(ctx, tenant)
		if err != nil {
			return err
		}
		if !ok {
			x.Unknown += len(grants)
			return nil
		}

		// Every row of the tenant's, locked, in the order of their
		// identifiers -- the order a limit's read locks them in, so that the
		// two never wait on each other the other way round.
		rows, err := s.Rows(ctx, tenant, "", true)
		if err != nil {
			return err
		}
		byGrant := map[string]*Row{}
		for i := range rows {
			byGrant[string(rows[i].Grant)] = &rows[i]
		}

		for _, g := range grants {
			at := byGrant[string(g.Grant)]
			if at == nil {
				// Another tenant's, or none: a grant moved is refused by put.
				v, found, err := s.Grant(ctx, g.Grant)
				if err != nil {
					return err
				}
				if found {
					at = &v
				}
			}
			if err := x.put(ctx, s, g, at); err != nil {
				return err
			}
		}

		gone := []pdid.Id{}
		for _, r := range rows {
			switch {
			case listed[string(r.Grant)]:
			case r.Ended(now):
				gone = append(gone, r.Id)
			default:
				x.Kept++
			}
		}
		if len(gone) == 0 {
			return nil
		}
		if err := s.Forget(ctx, gone); err != nil {
			return err
		}
		x.Forgotten += len(gone)

		return nil
	})

	return x, err
}

// apply writes one grant heard on the stream.
func (f Follower) apply(ctx context.Context, g *pdpb.EntitlementGrant) error {
	v, err := grantOf(g)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "entitle: the source sent a grant that is not one, and it is not applied",
			"grant", hex.EncodeToString(g.GetId()), "err", err)
		return nil
	}

	var x Synced
	err = f.in(ctx, v.Tenant, func(ctx context.Context, s Store) error {
		x = Synced{}

		ok, err := s.Tenant(ctx, v.Tenant)
		if err != nil {
			return err
		}
		if !ok {
			x.Unknown++
			return nil
		}

		row, found, err := s.Grant(ctx, v.Grant)
		if err != nil {
			return err
		}
		var at *Row
		if found {
			at = &row
		}

		return x.put(ctx, s, v, at)
	})
	if err != nil {
		return fmt.Errorf("entitle: apply %s of %s: %w", v.Key, v.Tenant, err)
	}

	log.From(ctx).DebugContext(ctx, "entitle: heard",
		"grant", hex.EncodeToString(v.Grant), "tenant", v.Tenant, "key", v.Key, "version", v.Version,
		"applied", x.Applied > 0, "unknown", x.Unknown > 0)

	return nil
}

// in is [Copy.In], once more when it fails: two writers of one grant new to
// the copy -- the stream and a comparison -- can each find no row and each add
// one, and the second is refused by the index the first has filled. Again, it
// finds the first's row, and the versions decide.
func (f Follower) in(ctx context.Context, tenant pdid.Id, fn func(ctx context.Context, s Store) error) error {
	err := f.Copy.In(ctx, tenant, fn)
	if err == nil || ctx.Err() != nil {
		return err
	}

	return f.Copy.In(ctx, tenant, fn)
}

// put writes `g` over `at`, the row that copies its grant -- nil for none --
// and counts what it did.
func (x *Synced) put(ctx context.Context, s Store, g Row, at *Row) error {
	switch {
	case at == nil:
	case at.Tenant != g.Tenant || at.Key != g.Key:
		x.Refused++
		log.From(ctx).ErrorContext(ctx, "entitle: the source moved a grant to another tenant or key, which a grant never is; "+
			"the copy keeps it as it was until it ends",
			"grant", hex.EncodeToString(g.Grant), "tenant", at.Tenant, "key", at.Key, "to_tenant", g.Tenant, "to_key", g.Key)
		return nil
	case at.Version >= g.Version:
		x.Heard++
		return nil
	default:
		g.Id = at.Id
	}

	if _, err := s.Put(ctx, g); err != nil {
		return err
	}
	x.Applied++

	return nil
}

// newest is `vs` with one row for each grant, the one of the greatest
// version, in the order of their grants' identifiers: a list whose pages
// moved under it can name a grant twice.
func newest(vs []Row) []Row {
	slices.SortStableFunc(vs, func(a, b Row) int {
		if c := cmp.Compare(string(a.Grant), string(b.Grant)); c != 0 {
			return c
		}

		return cmp.Compare(b.Version, a.Version)
	})

	return slices.CompactFunc(vs, func(a, b Row) bool { return string(a.Grant) == string(b.Grant) })
}

// grantOf is a grant as a row of the copy holds it, and refuses one that is
// not a grant: no identifier, no tenant, a key that is not one, no version, no
// value, a negative limit, or no end.
func grantOf(g *pdpb.EntitlementGrant) (Row, error) {
	r := Row{Grant: g.GetId(), Key: g.GetKey(), Version: g.GetVersion()}

	switch n := len(r.Grant); {
	case n == 0:
		return r, errors.New("names no grant")
	case n > 64:
		return r, fmt.Errorf("names its grant in %d bytes, and an identifier is 64 at most", n)
	}

	tenant, err := pdid.From(g.GetTenantId())
	if err != nil {
		return r, fmt.Errorf("its tenant: %w", err)
	}
	if tenant.IsZero() {
		return r, errors.New("names no tenant")
	}
	r.Tenant = tenant

	if err := CheckKey(r.Key); err != nil {
		return r, err
	}
	if r.Version == 0 {
		return r, errors.New("says no version, and a copy could not tell it from any other state of it")
	}

	switch g.WhichValue() {
	case pdpb.EntitlementGrant_On_case:
		r.Value = Off
		if g.GetOn() {
			r.Value = On
		}
	case pdpb.EntitlementGrant_Limit_case:
		if n := g.GetLimit(); n < 0 {
			return r, fmt.Errorf("gives a limit of %d, and a limit is zero or more", n)
		}
		r.Value = Limited(g.GetLimit())
	case pdpb.EntitlementGrant_Unlimited_case:
		r.Value = Unlimited
	default:
		return r, errors.New("gives nothing: it says none of on, limit and unlimited")
	}

	if !g.HasValidUntil() {
		return r, errors.New("says no end, and a grant that stops being heard of would then be kept forever")
	}
	if err := g.GetValidUntil().CheckValid(); err != nil {
		return r, fmt.Errorf("valid_until: %w", err)
	}
	r.Until = g.GetValidUntil().AsTime()
	if g.HasValidFrom() {
		if err := g.GetValidFrom().CheckValid(); err != nil {
			return r, fmt.Errorf("valid_from: %w", err)
		}
		r.From = g.GetValidFrom().AsTime()
	}

	return r, nil
}

// sleep waits until `at`, and answers false if the context ended first.
func sleep(ctx context.Context, at time.Time) bool {
	t := time.NewTimer(time.Until(at))
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
