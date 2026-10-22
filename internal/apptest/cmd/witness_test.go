package cmd_test

import (
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	_ "github.com/lesomnus/payday/config/witnesslog"
	_ "github.com/lesomnus/payday/config/witnessstore"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestTheHeadOfTheChainIsHeardOutsideTheDatabase (payday#115).
//
// Through the generated store, on the app's own database, with the witnesses
// the configuration names: a store of its own, and the log. Every checkpoint
// is heard, and a verification says what each that answers heard last. The
// newest taken away from the checkpoint store and the database's count wound
// back is what the store heard and the checkpoint store does not keep, and no
// act takes a checkpoint under its number.
func TestTheHeadOfTheChainIsHeardOutsideTheDatabase(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	robot := func(alias string) {
		_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  alias,
		}.Build())
		x.NoError(err)
		time.Sleep(time.Millisecond)
	}

	witness := "store:" + t.TempDir()
	p, err := config.AuditConfig{
		Archive:     t.TempDir(),
		Retain:      time.Nanosecond,
		Checkpoints: config.AuditCheckpointsConfig{Key: pdtest.Key(t), Witnesses: []string{witness, "log:"}},
	}.Policy()
	x.NoError(err)
	x.Contains(p.String(), "heard by log:, "+witness)

	s := pd.TrailStore(b.Ent)
	for _, v := range []string{"one", "two"} {
		robot(v)
		p.Pass(ctx, s)
	}
	k, _, err := s.Latest(ctx)
	x.NoError(err)
	x.Equal(2, k)

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(2, v.Heard[witness].K)
	x.NotContains(v.Heard, "log:", "a witness that only tells answered")

	// The newest checkpoint taken away, and the count wound back.
	cs := p.Archive.Use(trail.CheckpointNamespace)
	w, ok := flob.AsWalker(cs)
	x.True(ok)
	gone := 0
	for info, err := range w.Walk(ctx) {
		x.NoError(err)
		l, err := info.Labels(ctx)
		x.NoError(err)
		if l.Get("K") == "2" {
			x.NoError(cs.Erase(ctx, info.Digest()))
			gone++
		}
	}
	x.Equal(3, gone, "the checkpoint, and the receipt of each witness")
	x.NoError(b.Ent.Archived.UpdateOneId(uuid.Nil()).SetSince(1).Exec(ctx))

	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Len(v.Findings, 1, "%v", v.Findings)
	x.Equal("unkept", v.Findings[0].Kind)
	x.Equal(2, v.Findings[0].Checkpoint)

	robot("three")
	p.Pass(ctx, s)
	k, _, err = s.Latest(ctx)
	x.NoError(err)
	x.Equal(1, k, "a checkpoint was taken under a number a witness heard")
}
