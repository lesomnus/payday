package cmd_test

import (
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	_ "github.com/lesomnus/payday/config/witnessrekor"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/trail/witnessrekor/rekortest"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestTheHeadOfTheChainIsLoggedToRekor (payday#115).
//
// Through the generated store and the configuration, with the deployment's
// Ed25519 key: every checkpoint's digest is logged, signed once more as
// Ed25519ph, and a verification holds the chain to the log -- on a v1 log,
// the newest checkpoint taken away with its receipts and the count wound back
// is an entry the deployment's key signed that no checkpoint the store keeps
// is; on a v2 log, the receipts.
func TestTheHeadOfTheChainIsLoggedToRekor(t *testing.T) {
	for _, v := range []string{"v1", "v2"} {
		t.Run(v, func(t *testing.T) {
			x := require.New(t)
			b, ctx := build(t)

			l := rekortest.NewV1(t)
			witness := "rekor:" + l.URL + "?key=" + l.KeyFile
			if v == "v2" {
				l = rekortest.NewV2(t)
				witness = "rekor:" + l.URL + "?key=" + l.KeyFile + "&v=2"
			}
			p, err := config.AuditConfig{
				Archive:     t.TempDir(),
				Retain:      time.Nanosecond,
				Checkpoints: config.AuditCheckpointsConfig{Key: pdtest.Key(t), Witnesses: []string{witness}},
			}.Policy()
			x.NoError(err)

			s := pd.TrailStore(b.Ent)
			for _, alias := range []string{"one", "two"} {
				_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
					Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
					Alias:  alias,
				}.Build())
				x.NoError(err)
				p.Pass(ctx, s)
			}
			x.Equal(2, l.Len(), "every checkpoint was not logged")

			v1, err := p.Verify(ctx, s, true)
			x.NoError(err)
			x.True(v1.Ok(), "%v", v1.Findings)

			if v == "v2" {
				return
			}

			// The newest taken away with its receipt, and the count wound back.
			cs := p.Archive.Use(trail.CheckpointNamespace)
			w, ok := flob.AsWalker(cs)
			x.True(ok)
			for info, err := range w.Walk(ctx) {
				x.NoError(err)
				labels, err := info.Labels(ctx)
				x.NoError(err)
				if labels.Get("K") == "2" {
					x.NoError(cs.Erase(ctx, info.Digest()))
				}
			}
			x.NoError(b.Ent.Archived.UpdateOneId(uuid.Nil()).SetSince(1).Exec(ctx))

			after, err := p.Verify(ctx, s, false)
			x.NoError(err)
			x.Len(after.Findings, 1, "%v", after.Findings)
			x.Equal("unkept", after.Findings[0].Kind)
			x.Contains(after.Findings[0].Note, "no checkpoint the store keeps")
		})
	}
}
