package gate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
)

type parties struct {
	t               *testing.T
	caller, subject pdid.Id
	deny            pdid.Id
	seen            []pdid.Id
}

func (p *parties) May(ctx context.Context, c gate.Call) error {
	f, _ := frame.From(ctx)
	require.Equal(p.t, c.Actor, f.Actor)
	require.Equal(p.t, p.caller, c.Caller.Actor)
	require.Equal(p.t, p.subject, c.Subject.Actor)
	require.Equal(p.t, "session-a", c.Presence)
	p.seen = append(p.seen, c.Actor)
	if c.Actor == p.deny {
		return status.Error(codes.PermissionDenied, "not qualified")
	}
	return nil
}
func (p *parties) Where(_ context.Context, c gate.Call) (frame.Tenants, error) {
	if c.Actor == p.caller {
		return frame.Only(acme, hooli), nil
	}
	return frame.Only(acme, actor), nil
}
func TestRepresentationMeetsBothPartiesAndTheCredential(t *testing.T) {
	device := pdid.New(domain)
	f := frame.New(actor, acme, frame.Whole().In(acme, hooli, actor))
	f.Caller = &frame.Principal{Actor: device, Tenant: hooli}
	f.Presence = "session-a"
	ctx := frame.Into(t.Context(), f)
	p := &parties{t: t, caller: device, subject: actor}
	got, err := gate.Decide(ctx, p, "/app.RobotService/Add")
	require.NoError(t, err)
	v, _ := frame.From(got)
	require.Equal(t, []pdid.Id{acme}, v.Scope.Ids())
	require.Equal(t, []pdid.Id{actor, device}, p.seen)
	narrowed := *f
	narrowed.Grant = frame.Whole().In(actor)
	limited, err := gate.Decide(frame.Into(t.Context(), &narrowed), p, "/app.RobotService/Add")
	require.NoError(t, err)
	narrowFrame, _ := frame.From(limited)
	require.Empty(t, narrowFrame.Scope.Ids(), "the device grant still narrows the pair")
	for _, deny := range []pdid.Id{actor, device} {
		p.deny = deny
		_, err = gate.Decide(ctx, p, "/app.RobotService/Add")
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	}
	_, err = gate.Decide(ctx, nil, "/app.RobotService/Add")
	require.NoError(t, err)
	own, err := gate.Decide(ctx, nil, "/app.RobotService/Add")
	require.NoError(t, err)
	v, _ = frame.From(own)
	require.Empty(t, v.Scope.Ids(), "disjoint tenants give no scope")
}
func TestRepresentationAlsoMeetsSetMembership(t *testing.T) {
	device, one, two, three := pdid.New(domain), pdid.New(domain), pdid.New(domain), pdid.New(domain)
	f := frame.New(actor, acme, frame.Whole())
	f.Caller = &frame.Principal{Actor: device, Tenant: acme}
	ids, all, err := frame.NarrowSet(frame.Into(t.Context(), f), func(ctx context.Context) ([]uuid.UUID, bool, error) {
		v, _ := frame.From(ctx)
		if v.Actor == device {
			return []uuid.UUID{two.Uuid(), three.Uuid()}, false, nil
		}
		return []uuid.UUID{one.Uuid(), two.Uuid()}, false, nil
	})
	require.NoError(t, err)
	require.False(t, all)
	require.Equal(t, []uuid.UUID{two.Uuid()}, ids)
}
