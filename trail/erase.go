package trail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lesomnus/payday/pdid"
)

// The acts that make something of the trail stop existing other than a pass
// on its clock: removing rows by hand, destroying chunks by hand, erasing one
// subject, and a tenant leaving.
//
// They are methods of the policy rather than functions beside it because a
// legal hold is the policy's -- [Tenant.Hold], from the app's own answer -- and
// an operator at a shell, a person asking to be forgotten and a tenant's
// offboarding are none of them an exception to one. A function that took a
// cutoff and nothing else would be the way round it, and the reason these
// changed shape from the functions they replace is so that a caller still
// written against those does not compile.

// ErrHeld is the answer of an act that refuses outright under a legal hold,
// which is [Policy.PurgeTenant]'s: it has nothing to do but what the hold
// forbids. The others leave what is held and answer with it.
var ErrHeld = errors.New("under a legal hold")

// Held is what a destructive act left as it was because a legal hold is on it,
// and whose holds those are.
type Held struct {
	// Rows and Chunks are how much was kept: rows one by one, and chunks
	// whole.
	Rows   int
	Chunks int

	// By is the tenants whose holds kept it.
	By []pdid.Id

	// Unanswered is the tenants the app gave no answer for. What is theirs is
	// kept for the reason a pass keeps it -- nothing is destroyed on a guess --
	// and asking again is how it goes.
	Unanswered []pdid.Id
}

// Any answers whether anything was kept.
func (h Held) Any() bool { return h.Rows > 0 || h.Chunks > 0 }

func (h *Held) keep(rows, chunks int) {
	if h == nil {
		return
	}

	h.Rows += rows
	h.Chunks += chunks
}

func (h *Held) by(id pdid.Id) {
	if h == nil || slices.Contains(h.By, id) {
		return
	}

	h.By = append(h.By, id)
}

func (h *Held) unanswered(id pdid.Id) {
	if h == nil || slices.Contains(h.Unanswered, id) {
		return
	}

	h.Unanswered = append(h.Unanswered, id)
}

// Collect removes rows older than `before` and keeps no copy, except what a
// legal hold is on. It answers how many went and what was kept.
//
// Separate from [Archive] rather than the same call with no archive, because
// the two are different acts and one of them is irreversible. A deployment
// that means it says so.
//
// It writes a [Receipt] when there is an archive to keep one in.
func (p Policy) Collect(ctx context.Context, s Store, of Kinds, before time.Time) (int, Held, error) {
	h := Held{}

	x, err := p.start(s)
	if err != nil {
		return 0, h, err
	}

	_, gone, err := drain(ctx, s, Scope{Kinds: of}, before, nil, "", func(v Row) fate {
		if x.held(ctx, v.Tenants, v.Domain, true, &h) {
			h.keep(1, 0)
			return stays
		}

		return discarded
	})

	if gone > 0 {
		x.r.add(Receipt{Act: "discard", Where: "database", Kind: kindsOf(of), Before: before.UTC(), Rows: gone, Held: h.Rows})
	}
	if werr := x.r.write(ctx, x.p.Archive); werr != nil && err == nil {
		err = werr
	}

	return gone, h, err
}

// kindsOf is a receipt's kind for a set of them: the one when there is one,
// nothing for all of them, and `*` for what is left once some are excepted.
func kindsOf(k Kinds) string {
	switch {
	case k.All():
		return ""
	case len(k.Only) == 1:
		return nameOf(k.Only[0])
	default:
		return "*"
	}
}

// Doomed is what [Policy.Purge] would destroy, and destroys nothing: the chunks
// entirely older than the cutoff, less what a hold keeps -- which it answers as
// well.
//
// Its own method rather than a flag on [Policy.Purge], so that the list a dry
// run prints is the list the real one acts on -- two passes that agree today
// are two passes.
//
// `cut` is asked per kind, because the second clock is per kind: an operating
// record and a person's are in one archive and are not destroyed on the same
// day. A kind it declines is left alone. A chunk goes when **every** row in it
// is older than the cutoff; see [Chunk.Before].
func (p Policy) Doomed(ctx context.Context, cut func(kind string) (time.Time, bool)) ([]Chunk, Held, error) {
	return p.purge(ctx, nil, cut, true)
}

// Purge destroys the chunks that are entirely older than the cutoff, in every
// namespace, except what a legal hold is on, and answers with what it
// destroyed and what it kept.
//
// This is the end of the line and there is nothing after it. It is the act an
// operator takes by hand, with a cutoff of their own, and it answers to nobody's
// window: [Policy.Pass] is what applies the deployment's and the tenants'. It
// answers to the holds, all the same. A hold on one tenant leaves the rest of
// the purge to go ahead, because refusing it would keep every other tenant's
// trail past what they are owed.
//
// A file of everybody's from before the archive was a flob store loses what is
// not held and keeps what is. It writes a [Receipt] of what went, including
// when it stopped part of the way.
//
// It goes through the archive's manifest like every other act, which is why it
// takes the store: the manifest is in the database; see [Archived].
func (p Policy) Purge(ctx context.Context, s Store, cut func(kind string) (time.Time, bool)) ([]Chunk, Held, error) {
	if s == nil {
		return nil, Held{}, errors.New("no store to keep the manifest in")
	}

	return p.purge(ctx, s, cut, false)
}

func (p Policy) purge(ctx context.Context, s Store, cut func(kind string) (time.Time, bool), dry bool) ([]Chunk, Held, error) {
	h := Held{}
	if p.Archive == nil {
		return nil, h, errors.New("no archive to destroy from")
	}

	x, err := p.start(s)
	if err != nil {
		return nil, h, err
	}

	vs, err := Chunks(ctx, x.p.Archive)
	if err != nil {
		return nil, h, err
	}

	out := []Chunk{}
	judges := map[string]func(Chunk, *Held) (Keep, bool, bool){}
	for _, c := range vs {
		if err = ctx.Err(); err != nil {
			break
		}

		before, ok := cut(c.Kind)
		if !ok || !c.Before(before) {
			continue
		}

		judge, ok := judges[c.Namespace]
		if !ok {
			judge = x.judge(ctx, c.Namespace)
			judges[c.Namespace] = judge
		}
		if judge == nil {
			continue
		}

		_, held, _ := judge(c, &h)

		var n int
		if n, err = x.spend(ctx, c, held, before, "purge", dry, &h); err != nil {
			break
		}
		if n > 0 {
			out = append(out, c)
		}
	}

	if !dry {
		if werr := x.r.write(ctx, x.p.Archive); werr != nil && err == nil {
			err = werr
		}
	}

	return out, h, err
}

// Forgotten is what erasing a subject reached, and what a hold kept as it was.
type Forgotten struct {
	// Rows is the rows about them in the database, and Archived the rows about
	// them in the archive, whose contents are gone. Counted whether or not
	// they had contents left to lose: what is answered is *how many rows about
	// this person were reached*, and a row already blank is still one of them.
	Rows     int
	Archived int

	// Held is the rows about them a legal hold kept as they were. They are to
	// be erased again when it lifts, and this is the list of whose holds to
	// watch for.
	Held Held
}

// Forget blanks the contents of every row about these objects, in the database
// and in the archive, except the ones a legal hold is on, and answers what it
// reached and what it kept.
//
// # What it blanks, and what it deliberately does not
//
// `value` and `patch`, which are the two columns that hold contents.
// Everything else -- who acted, what they did, which object, when -- stays,
// and stays on purpose: that is the record the trail exists to be, and it is
// what a legal-obligation exemption is an exemption *for*. What is destroyed
// is what the row said about somebody; what survives is that it happened.
//
// The actor is not touched. It is an identifier, and it is personal data only
// because it **resolves** -- which is a property of the row it points at rather
// than of this one. A caller that has destroyed the person's own record has
// already made it a pseudonym that reaches nothing, and blanking it here would
// destroy *who did this*, which is the whole of what a trail is for.
//
// # Why both places in one call
//
// It used to be two -- one for the database, one for the archive -- and a
// caller that kept an archive had to remember the second. A mechanism that
// stopped at the database destroys the copy an operator can see and leaves the
// copy in the archive beside it, which is an answer wrong in the direction that
// matters.
//
// # Which rows, and when
//
// Is the app's: what it owes a person and under what regime is not a thing a
// framework can know. This is the half with no judgement in it but one -- a
// row a hold is on stays as it was, because a legal claim overrides an
// erasure request; see [Hold]. The caller hears which rows and whose holds,
// and erases again when they lift.
//
// A chunk is written again only when something in it changes, and it goes
// round again until a round finds nothing left to blank, because a pass
// folding chunks at the same moment can have copied a row out of the old one
// before it was rewritten.
func (p Policy) Forget(ctx context.Context, s Store, objects []pdid.Id) (Forgotten, error) {
	out := Forgotten{}
	if len(objects) == 0 {
		return out, nil
	}

	x, err := p.start(s)
	if err != nil {
		return out, err
	}

	// The database.
	at := Cursor{}
	for {
		vs, err := s.Heads(ctx, Scope{Objects: objects}, forever, at, Batch)
		if err != nil {
			return out, err
		}

		keys := []any{}
		for _, v := range vs {
			at = Cursor{Created: v.Created, Key: v.Key}

			if x.held(ctx, v.Tenants, v.Domain, true, &out.Held) {
				out.Held.keep(1, 0)
				continue
			}

			keys = append(keys, v.Key)
		}

		n, err := blank(ctx, s, keys)
		out.Rows += n
		if err != nil {
			return out, err
		}
		if len(vs) < Batch {
			break
		}
	}

	held := out.Held.Rows
	x.r.add(Receipt{Act: "forget", Where: "database", Blanked: out.Rows, Objects: len(objects), Held: held})

	// The archive.
	if p.Archive != nil {
		n, k, err := x.forget(ctx, objects, &out.Held)
		out.Archived = n
		x.r.add(Receipt{Act: "forget", Where: "archive", Blanked: n, Chunks: k, Objects: len(objects), Held: out.Held.Rows - held})
		if err != nil {
			return out, err
		}
	}

	return out, x.r.write(ctx, x.p.Archive)
}

// forget is the archive's half of [Policy.Forget], and answers how many rows
// about them it reached and how many chunks it wrote again.
func (x *pass) forget(ctx context.Context, objects []pdid.Id, h *Held) (int, int, error) {
	of := make(map[string]bool, len(objects))
	marks := make([][]byte, 0, len(objects))
	for _, v := range objects {
		e := encoded(v)
		of[e] = true
		marks = append(marks, []byte(`"`+e+`"`))
	}

	// The cheap half of the question: whether the line has one of them in it
	// anywhere, before it is parsed to ask whether as its object.
	pre := func(line []byte) bool {
		for _, v := range marks {
			if bytes.Contains(line, v) {
				return true
			}
		}

		return false
	}

	n, rewritten := 0, 0
	for round := 0; round < 3; round++ {
		vs, err := Chunks(ctx, x.p.Archive)
		if err != nil {
			return n, rewritten, err
		}

		k := 0
		for _, c := range vs {
			v, err := edit(ctx, x.p.Archive, c, false, pre, func(line []byte, v head) ([]byte, mark, error) {
				if !of[v.Object] {
					return line, kept, nil
				}
				if x.held(ctx, v.tenants(), pdid.Domain(v.Domain), true, h) {
					return line, heldBack, nil
				}
				if !v.contents() {
					return line, same, nil
				}

				out, err := blanked(line)
				return out, changed, err
			})
			if err != nil {
				return n, rewritten, err
			}

			if round == 0 {
				n += v.changed + v.same
				h.keep(v.held, 0)
			}
			if v.changed > 0 {
				k++
			}
		}

		rewritten += k
		if k == 0 {
			break
		}
	}

	return n, rewritten, nil
}

// TenantPurge is what removing one tenant's trail did, or would do.
type TenantPurge struct {
	// Removed is the rows of the database that were the tenant's alone, and
	// Blanked the rows filed under it that name another tenant as well, whose
	// contents went and whose events stayed.
	Removed int
	Blanked int

	// Chunks is the chunks of its namespace erased, and Rows the rows in
	// them and the rows of its taken out of the files of everybody's.
	Chunks int
	Rows   int

	// Rewritten is the chunks of other namespaces written again without what
	// was its, and Edited the rows in them whose contents went.
	Rewritten int
	Edited    int

	// Held is what another tenant's hold kept as it was: a row this tenant
	// shares with one under a hold.
	Held Held
}

// PurgeTenant removes one tenant's trail: its rows in the database, and its
// namespace in the archive.
//
// A function and not a schedule. **When** is the app's -- at once, on a
// request or a legal demand, or after the grace an offboarding gives -- and
// roster's `forget` and `restore` are the model: two triggers, one act.
//
// # What it keeps
//
// A row that also names another tenant is not removed. Its contents go and its
// event stays, because it is the other tenant's evidence as much as this
// one's: a transfer, an operator of one tenant writing into another. And a row
// it shares with a tenant under a hold stays exactly as it is.
//
// What is filed under another tenant and only names this one -- the other
// side's record of a transfer, a write this tenant's operator made into
// somebody else's rows -- is the other tenant's, and is not touched.
//
// # What it refuses
//
// A tenant under a hold, which is [ErrHeld], and a tenant the app has no
// answer for, since whether a hold is on it is then not known.
//
// It goes round again until a round finds nothing, because a pass moving the
// tenant's rows at the same moment can write a chunk after its namespace was
// erased. It writes a [Receipt]. [Policy.PlanTenantPurge] is the same, counted
// and not done.
func (p Policy) PurgeTenant(ctx context.Context, s Store, tenant pdid.Id) (TenantPurge, error) {
	return p.purgeTenant(ctx, s, tenant, false)
}

// PlanTenantPurge is what [Policy.PurgeTenant] would do, and does nothing.
func (p Policy) PlanTenantPurge(ctx context.Context, s Store, tenant pdid.Id) (TenantPurge, error) {
	return p.purgeTenant(ctx, s, tenant, true)
}

func (p Policy) purgeTenant(ctx context.Context, s Store, tenant pdid.Id, dry bool) (TenantPurge, error) {
	out := TenantPurge{}
	if tenant.IsZero() {
		return out, errors.New("no tenant")
	}

	x, err := p.start(s)
	if err != nil {
		return out, err
	}

	if p.Tenants != nil {
		t, err := p.Tenants(ctx, tenant)
		if err != nil {
			return out, fmt.Errorf("tenant %s: no answer, so whether a hold is on it is not known: %w", tenant, err)
		}
		if t.Hold != nil {
			return out, fmt.Errorf("tenant %s: %w: %s", tenant, ErrHeld, t.Hold.Why)
		}

		x.answers[tenant] = answer{t: t}
	}

	for round := 0; round < 3; round++ {
		n, err := x.purgeDatabase(ctx, tenant, dry, round == 0, &out)
		if err != nil {
			return out, err
		}

		m, err := x.purgeArchive(ctx, tenant, dry, round == 0, &out)
		if err != nil {
			return out, err
		}
		if dry || n+m == 0 {
			break
		}
	}

	if dry {
		return out, nil
	}

	x.r.add(Receipt{
		Act: "purge-tenant", Where: "database", Tenant: tenant.String(),
		Rows: out.Removed, Blanked: out.Blanked, Held: out.Held.Rows,
	})
	if p.Archive != nil {
		x.r.add(Receipt{
			Act: "purge-tenant", Where: "archive", Tenant: tenant.String(),
			Rows: out.Rows, Chunks: out.Chunks, Blanked: out.Edited,
		})
	}

	return out, x.r.write(ctx, x.p.Archive)
}

// purgeDatabase is the database's half of a tenant leaving, and answers how
// many rows it reached this round.
func (x *pass) purgeDatabase(ctx context.Context, tenant pdid.Id, dry bool, first bool, out *TenantPurge) (int, error) {
	s := x.s
	touched := 0

	// What was its alone.
	alone := Scope{Whose: Alone, Tenant: tenant}
	if dry {
		n, err := s.Count(ctx, alone, forever)
		if err != nil {
			return 0, err
		}

		out.Removed += n
		touched += n
	}
	for !dry {
		vs, err := s.Heads(ctx, alone, forever, Cursor{}, Batch)
		if err != nil {
			return touched, err
		}
		if len(vs) == 0 {
			break
		}

		keys := make([]any, len(vs))
		for i, v := range vs {
			keys[i] = v.Key
		}

		n, err := forget(ctx, s, keys)
		out.Removed += n
		touched += n
		if err != nil {
			return touched, err
		}
		if n == 0 {
			// Read and not removed: another writer has them, and going round
			// again would read the same ones for ever.
			break
		}
	}

	// What it shares, from its side: the event stays. Once, since nothing
	// puts contents back into a row.
	if !first {
		return touched, nil
	}

	at := Cursor{}
	for {
		vs, err := s.Heads(ctx, Scope{Whose: Sharing, Tenant: tenant}, forever, at, Batch)
		if err != nil {
			return touched, err
		}

		keys := []any{}
		for _, v := range vs {
			at = Cursor{Created: v.Created, Key: v.Key}

			if x.held(ctx, others(v.Tenants, tenant), v.Domain, true, &out.Held) {
				out.Held.keep(1, 0)
				continue
			}

			keys = append(keys, v.Key)
		}

		n := len(keys)
		if !dry {
			if n, err = blank(ctx, s, keys); err != nil {
				return touched, err
			}
		}

		out.Blanked += n
		touched += n
		if len(vs) < Batch {
			break
		}
	}

	return touched, nil
}

// purgeArchive is the archive's half, and answers how many chunks it reached
// this round.
func (x *pass) purgeArchive(ctx context.Context, tenant pdid.Id, dry bool, first bool, out *TenantPurge) (int, error) {
	a := x.p.Archive
	if a == nil {
		return 0, nil
	}

	touched := 0

	// Its own, whole.
	cs, err := chunksIn(ctx, a, tenant.String())
	if err != nil {
		return touched, err
	}
	for _, c := range cs {
		if !dry {
			if err := a.Use(c.Namespace).Erase(ctx, c.Digest); err != nil {
				return touched, err
			}
		}

		out.Chunks++
		out.Rows += c.Rows
		touched++
	}

	// What it shares, and the files of everybody's from before.
	named := []byte(`"` + encoded(tenant) + `"`)
	pre := func(line []byte) bool { return bytes.Contains(line, named) }
	for _, ns := range []string{SharedNamespace, LegacyNamespace} {
		cs, err := chunksIn(ctx, a, ns)
		if err != nil {
			return touched, err
		}

		for _, c := range cs {
			if ns == SharedNamespace && !slices.Contains(c.Tenants, tenant) {
				continue
			}

			v, err := edit(ctx, a, c, dry, pre, func(line []byte, v head) ([]byte, mark, error) {
				if !v.filed(tenant) {
					return line, kept, nil
				}

				rest := others(v.tenants(), tenant)
				if len(rest) == 0 {
					return nil, dropped, nil
				}
				if x.held(ctx, rest, pdid.Domain(v.Domain), true, &out.Held) {
					return line, heldBack, nil
				}
				if !v.contents() {
					return line, same, nil
				}

				b, err := blanked(line)
				return b, changed, err
			})
			if err != nil {
				return touched, err
			}

			if first {
				out.Held.keep(v.held, 0)
			}
			if v.changed+v.dropped > 0 {
				out.Rewritten++
				out.Edited += v.changed
				out.Rows += v.dropped
				touched++
			}
		}
	}

	return touched, nil
}

// others is the tenants a row names besides this one.
func others(ids []pdid.Id, tenant pdid.Id) []pdid.Id {
	out := make([]pdid.Id, 0, len(ids))
	for _, v := range ids {
		if v != tenant {
			out = append(out, v)
		}
	}

	return out
}
