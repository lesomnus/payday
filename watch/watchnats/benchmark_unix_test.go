//go:build unix && !js

package watchnats

import (
	"fmt"
	"github.com/lesomnus/payday/watch"
	"github.com/stretchr/testify/require"
	"syscall"
	"testing"
)

// Process CPU includes the embedded server and all adapter replicas. It does
// not include compilation or other processes, and is not a production capacity
// estimate. Identical encoding and confirmed publishing make fanout comparable.
func cpuTime() int64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		panic(err)
	}
	return usage.Utime.Nano() + usage.Stime.Nano()
}

func BenchmarkThreeReplicaFanout(b *testing.B) {
	for _, scoped := range []bool{false, true} {
		b.Run(fmt.Sprintf("scoped=%t", scoped), func(b *testing.B) {
			s := start(b, nil)
			p := broker(b, s, "app")
			replicas := []*Broker{broker(b, s, "app"), broker(b, s, "app"), broker(b, s, "app")}
			route := watch.RouteKey("app", "X")
			other := watch.RouteKey("app", "Y")
			subs := make([]*watch.Subscription, 3)
			for i, replica := range replicas {
				scope := watch.Interest{}
				if scoped {
					r := route
					if i == 1 {
						r = other
					}
					scope.Routes = []watch.Route{r}
				}
				subs[i] = opened(b, replica, scope)
			}
			value := change(route)
			before, _ := s.Varz(nil)
			b.ReportAllocs()
			b.ResetTimer()
			cpuStart := cpuTime()
			for range b.N {
				require.NoError(b, p.PublishConfirmed(b.Context(), value))
				next(b, subs[0])
				next(b, subs[2])
				if !scoped {
					next(b, subs[1])
				}
			}
			b.StopTimer()
			cpuUsed := cpuTime() - cpuStart
			after, _ := s.Varz(nil)
			b.ReportMetric(float64(cpuUsed)/float64(b.N), "cpu-ns/op")
			b.ReportMetric(float64(after.OutMsgs-before.OutMsgs)/float64(b.N), "replica-msg/op")
			b.ReportMetric(float64(after.OutBytes-before.OutBytes)/float64(b.N), "replica-bytes/op")
		})
	}
}
