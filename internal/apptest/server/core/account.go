package core

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/holder"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/predicate"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/tenant"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

type coreHolder struct {
	Core
	app.HolderServiceServer
}

func (s Core) Holder() app.HolderServiceServer {
	return coreHolder{s, s.Next().Holder()}
}

// Memberships answers the holders of the calling account, one for each tenant
// it is in, each with its tenant (payday#184).
//
// # What it reads, said rather than assumed
//
// It is served as the account, which is in no tenant, so the wall has nothing
// to narrow by and answers every read with nothing until this says what the
// method reads: the holders that name this account, and then the tenants those
// are in. Both through the servers below, so what reads them is the wall
// reading what was declared -- and the erased ones, the secrets and the access
// record are what they are for any other read.
//
// The declaration is the whole of what it may see. Nothing in it says "every
// holder" or "every tenant", which is the one answer a method an account calls
// as itself must never give, and the gate refuses such a call every method but
// this one -- so a page can ask which tenants it may choose before it chooses,
// and learns nothing of anybody else's.
func (s coreHolder) Memberships(ctx context.Context, req *app.HolderMembershipsRequest) (*app.HolderMembershipsResponse, error) {
	if _, ok := frame.AccountOf(ctx); !ok {
		// Asked as a membership, which sees its own tenant's holders and would
		// answer for that tenant alone -- which is not the question.
		return nil, status.Error(codes.FailedPrecondition, "an account asks this as itself, and this call is served as one of its memberships")
	}

	reads := pd.Reads{
		Holder: func(_ context.Context, account pdid.Id) (predicate.Holder, error) {
			return holder.Account(account.Uuid()), nil
		},
	}
	at, err := pd.AccountReads(ctx, app.HolderService_Memberships_FullMethodName, reads)
	if err != nil {
		return nil, err
	}

	var hs []*app.Holder
	for after := ""; ; {
		vs, err := s.Next().Holder().List(at, app.HolderListRequest_builder{After: after}.Build())
		if err != nil {
			return nil, err
		}

		hs = append(hs, vs.GetItems()...)
		if after = vs.GetNext(); after == "" {
			break
		}
	}
	if len(hs) == 0 {
		return app.HolderMembershipsResponse_builder{}.Build(), nil
	}

	// And the tenants they are in, which the account may read because it is a
	// member of them -- by the identifiers the holders it read hold, and not
	// by anything the request said.
	ids := make([]pdid.Id, 0, len(hs))
	for _, h := range hs {
		if t, err := pdid.From(h.GetTenant().GetId()); err == nil {
			ids = append(ids, t)
		}
	}
	reads.Tenant = func(context.Context, pdid.Id) (predicate.Tenant, error) {
		vs := make([]uuid.UUID, 0, len(ids))
		for _, v := range ids {
			vs = append(vs, v.Uuid())
		}

		return tenant.IdIn(vs...), nil
	}
	if at, err = pd.AccountReads(ctx, app.HolderService_Memberships_FullMethodName, reads); err != nil {
		return nil, err
	}

	ts := map[pdid.Id]*app.Tenant{}
	for after := ""; ; {
		vs, err := s.Next().Tenant().List(at, app.TenantListRequest_builder{After: after}.Build())
		if err != nil {
			return nil, err
		}
		for _, t := range vs.GetItems() {
			if id, err := pdid.From(t.GetId()); err == nil {
				ts[id] = t
			}
		}

		if after = vs.GetNext(); after == "" {
			break
		}
	}

	for _, h := range hs {
		if id, err := pdid.From(h.GetTenant().GetId()); err == nil && ts[id] != nil {
			h.SetTenant(ts[id])
		}
	}

	return app.HolderMembershipsResponse_builder{Items: hs}.Build(), nil
}
