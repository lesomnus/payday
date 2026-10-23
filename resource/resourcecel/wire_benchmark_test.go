package resourcecel

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/resource"
)

// The native environment reproduces the previous adapter. Both programs retain
// the serving path's CEL cost/interrupt settings. This is the filter alone; what
// the server does to an event before its filter is asked -- cloning, the full
// decode, the projector and the re-encoding -- is BenchmarkProject in
// `resource`, and was this benchmark's `project_filter` stage until CEL moved
// out of that package (payday#163).
func BenchmarkWatchFilter(b *testing.B) {
	pwr, err := language([]protoreflect.MessageDescriptor{(&pdpb.WatchEvent{}).ProtoReflect().Descriptor()})
	if err != nil {
		b.Fatal(err)
	}
	env, err := cel.NewEnv(cel.Types(&pdpb.WatchEvent{}), cel.Variable("event", cel.ObjectType("payday.WatchEvent")), cel.ParserExpressionSizeLimit(resource.MaxExpression), cel.ParserRecursionLimit(MaxDepth))
	if err != nil {
		b.Fatal(err)
	}
	native := filters{env}
	ctx := context.Background()
	for _, size := range []int{0, 8 << 10, 256 << 10} {
		blob, err := anypb.New(wrapperspb.Bytes(make([]byte, size)))
		if err != nil {
			b.Fatal(err)
		}
		message := pdpb.WatchEvent_builder{Id: make([]byte, 16), Method: "artifact", Value: blob}.Build()
		event, err := resource.Event(make([]byte, 16), time.Unix(1, 0), "/app/Add", "/app/Add", pdpb.ChangeKind_CHANGE_KIND_CREATED, nil, message)
		if err != nil {
			b.Fatal(err)
		}
		for _, filter := range []struct{ name, expr string }{
			{"type", `type(event.value) == payday.WatchEvent`},
			{"field", `has(event.value) && type(event.value) == payday.WatchEvent && event.value.method == "artifact"`},
		} {
			for _, impl := range []struct {
				name    string
				filters resource.Filters
			}{{"native", native}, {"pwr", pwr}} {
				p, err := impl.filters.Compile(filter.expr)
				if err != nil {
					b.Fatal(err)
				}
				b.Run(fmt.Sprintf("bytes=%d/%s/%s/filter", size, filter.name, impl.name), func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						v, _, err := p.(program).ContextEval(ctx, map[string]any{"event": event})
						if err != nil || v != types.True {
							b.Fatal(v, err)
						}
					}
				})
			}
		}
	}
}
