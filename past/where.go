package past

import (
	"context"
	"slices"
	"time"
	"uuid"

	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// The columns of payday's Event and Fact, which are payday's: the templates
// in `schema/past/` declare them, and an overlay may add to the tables but not
// rename these. So the predicates a read is made of are built here, by name,
// and the store applies them without knowing what they say.
const (
	colId       = "id"
	colTenant   = "tenant_id"
	colSubject  = "subject_id"
	colRecorded = "date_created"

	colCause = "cause_id"

	colField        = "field_number"
	colKey          = "key"
	colFrom         = "valid_from"
	colEvent        = "event_id"
	colSuperseded   = "superseded_at"
	colSupersededBy = "superseded_by"
)

// page is how many values one predicate of a read names: few enough for any
// engine's statement to hold, as the trail reads its rows a page at a time
// (`audit.RowsPage`).
const page = 500

// pages is `ids` without the zero identifier or a second of any, a page at a
// time.
func pages(ids []pdid.Id) [][]pdid.Id {
	seen := map[pdid.Id]bool{}
	vs := make([]pdid.Id, 0, len(ids))
	for _, v := range ids {
		if v.IsZero() || seen[v] {
			continue
		}
		seen[v] = true
		vs = append(vs, v)
	}

	return slices.Collect(slices.Chunk(vs, page))
}

// walled is the predicate that narrows a read to the tenants the caller of
// `ctx` may see, as the generated wall narrows every read of a server: none
// for a caller who may see every tenant, and one that matches nothing for a
// caller who may see none. A read with no frame is refused.
func walled(ctx context.Context) ([]func(*sql.Selector), error) {
	vs, all, err := frame.Narrow(ctx)
	if err != nil {
		return nil, err
	}
	if all {
		return nil, nil
	}

	return []func(*sql.Selector){sql.FieldIn(colTenant, vs...)}, nil
}

// in is a column one of `ids`.
func in(col string, ids []pdid.Id) func(*sql.Selector) {
	return sql.FieldIn(col, uuids(ids)...)
}

// is is a column `id`.
func is(col string, id pdid.Id) func(*sql.Selector) {
	return sql.FieldEQ(col, id.Uuid())
}

// standing is a fact no correction or later fact replaced.
func standing() func(*sql.Selector) { return sql.FieldIsNull(colSuperseded) }

// known is the facts a read takes ([Known]): standing now, standing at a
// moment by what had been written down by then, or all of them.
func known(k Known) []func(*sql.Selector) {
	switch {
	case k.All:
		return nil
	case k.At != nil:
		at := *k.At
		return []func(*sql.Selector){
			sql.FieldLTE(colRecorded, at),
			sql.OrPredicates(sql.FieldIsNull(colSuperseded), sql.FieldGT(colSuperseded, at)),
		}
	default:
		return []func(*sql.Selector){standing()}
	}
}

// visible says `r` is one of the facts `k` takes: what [known] asks of the
// database, asked again of a row in hand.
func visible(r FactRow, k Known) bool {
	switch {
	case k.All:
		return true
	case k.At != nil:
		at := *k.At
		return !r.Recorded.After(at) && (r.Superseded == nil || r.Superseded.After(at))
	default:
		return r.Superseded == nil
	}
}

// order is the columns to read in, each ascending.
func order(cols ...string) []func(*sql.Selector) {
	vs := make([]func(*sql.Selector), len(cols))
	for i, c := range cols {
		vs[i] = sql.OrderByField(c).ToFunc()
	}

	return vs
}

func uuids(ids []pdid.Id) []uuid.UUID {
	vs := make([]uuid.UUID, len(ids))
	for i, v := range ids {
		vs[i] = v.Uuid()
	}

	return vs
}

func times(vs []time.Time) []time.Time {
	out := make([]time.Time, 0, len(vs))
	for _, v := range vs {
		if !slices.ContainsFunc(out, v.Equal) {
			out = append(out, v)
		}
	}

	return out
}
