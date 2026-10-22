// Package witnesslog is a witness that tells (payday#115; see trail.Witness):
// a line of the log for every checkpoint the trail takes, and a gauge of its
// number, `payday.trail.checkpoint`. It answers nothing -- trail.ErrDeaf --
// and the monitoring outside is the witness: what reads the logs and the
// metrics is somewhere whoever writes the database and the archive does not
// reach, and an alert there on the number going down, or on one number told
// under two digests, is what holds the head of the chain to what was said.
// payday's runtime docs give the alert.
package witnesslog

import (
	"context"
	"fmt"

	"github.com/lesomnus/otx"
	"github.com/lesomnus/otx/log"
	"go.opentelemetry.io/otel/metric"

	"github.com/lesomnus/payday/trail"
)

// Scheme is what `audit.checkpoints.witnesses` names one by: `log:`.
const Scheme = "log"

// Gauge is the number of the newest checkpoint the trail took, as a gauge.
const Gauge = "payday.trail.checkpoint"

// Witness tells what it hears to the log and the metrics of the context it
// is handed.
type Witness struct{}

var _ trail.Witness = Witness{}

// New is a witness that tells.
func New() Witness { return Witness{} }

// Open is the witness `log:` names, for config's `RegisterWitness`.
func Open(v string) (Witness, error) {
	if v != Scheme+":" {
		return Witness{}, fmt.Errorf("%s: a %s witness is named `%s:`, and takes nothing after it", v, Scheme, Scheme)
	}

	return New(), nil
}

// Hear writes the line and records the number. A tenant's chain (Chain) is
// told in the log and not in the gauge: a series per tenant is the
// monitoring's cost, and the tenant's own witness is what a tenant has.
func (Witness) Hear(ctx context.Context, s trail.Signed) (trail.Witnessed, error) {
	log.From(ctx).InfoContext(ctx, "trail: a checkpoint, heard",
		"chain", s.Chain, "k", s.K, "digest", string(s.Digest), "prev", string(s.Prev))

	if s.Chain == "" {
		otx.Int64Gauge(ctx, Gauge,
			metric.WithDescription("the number of the newest checkpoint the trail took; it never goes down"),
		).Record(ctx, int64(s.K))
	}

	return trail.Witnessed{}, nil
}

// Latest is [trail.ErrDeaf]: what was told is the monitoring's to hold.
func (Witness) Latest(context.Context, string) (trail.Heard, error) {
	return trail.Heard{}, trail.ErrDeaf
}
