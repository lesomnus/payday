package resource

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// BenchmarkProject is what the server does to an event before its filter is
// asked: the event cloned, each image decoded in full, an identity projector,
// and the re-encoding. No database authorization, broker delivery or gRPC.
// The filter is BenchmarkWatchFilter in `resourcecel`; the two together are
// what that benchmark's `project_filter` stage measured until CEL moved out of
// this package (payday#163).
func BenchmarkProject(b *testing.B) {
	s, err := New(&pdpb.UnimplementedBatchServiceServer{}, nil, func(_ context.Context, m proto.Message) (proto.Message, error) { return m, nil })
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	for _, size := range []int{0, 8 << 10, 256 << 10} {
		blob, err := anypb.New(wrapperspb.Bytes(make([]byte, size)))
		if err != nil {
			b.Fatal(err)
		}
		message := pdpb.WatchEvent_builder{Id: make([]byte, 16), Method: "artifact", Value: blob}.Build()
		source, err := Event(make([]byte, 16), time.Unix(1, 0), "/app/Add", "/app/Add", pdpb.ChangeKind_CHANGE_KIND_CREATED, nil, message)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := s.public(ctx, source); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
