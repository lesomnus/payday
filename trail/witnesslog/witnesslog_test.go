package witnesslog_test

import (
	"testing"

	"github.com/lesomnus/otx/otxtest"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/trail/witnesslog"
)

// gauge is the newest value the gauge was recorded with, and whether it was.
func gauge(t *testing.T, h *otxtest.Harness) (int64, bool) {
	rm := h.Collect(t.Context())
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != witnesslog.Gauge {
				continue
			}

			g, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "%s is no gauge of whole numbers", m.Name)
			require.Len(t, g.DataPoints, 1)

			return g.DataPoints[0].Value, true
		}
	}

	return 0, false
}

// TestAWitnessThatTells (payday#115): a line of the log for every checkpoint,
// and the gauge at its number -- what the monitoring outside alerts on -- and
// nothing to answer a verification with. A tenant's chain is told in the log,
// and not in the gauge.
func TestAWitnessThatTells(t *testing.T) {
	x := require.New(t)
	h := otxtest.New(t)
	ctx := h.Into(t.Context())
	w := witnesslog.New()

	_, err := w.Hear(ctx, trail.Signed{K: 7, Digest: "sha256:aaaa", Prev: "sha256:bbbb"})
	x.NoError(err)

	n, ok := gauge(t, h)
	x.True(ok, "the gauge was not recorded")
	x.EqualValues(7, n)

	rs := h.Records()
	x.Len(rs, 1)
	x.Equal("trail: a checkpoint, heard", rs[0].Body().AsString())
	said := map[string]string{}
	rs[0].WalkAttributes(func(kv attribute.KeyValue) bool {
		said[string(kv.Key)] = kv.Value.Emit()
		return true
	})
	x.Equal("7", said["k"])
	x.Equal("sha256:aaaa", said["digest"])
	x.Equal("sha256:bbbb", said["prev"])

	_, err = w.Hear(ctx, trail.Signed{Chain: "a tenant", K: 9, Digest: "sha256:cccc"})
	x.NoError(err)
	n, _ = gauge(t, h)
	x.EqualValues(7, n, "a tenant's chain moved the deployment's gauge")
	x.Len(h.Records(), 2)

	_, err = w.Latest(ctx, "")
	x.ErrorIs(err, trail.ErrDeaf)

	_, err = witnesslog.Open("log:")
	x.NoError(err)
	_, err = witnesslog.Open("log:/var/log")
	x.Error(err)
}
