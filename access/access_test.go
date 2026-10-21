package access_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// sink is a [access.Writer.Sink] that keeps what it is handed, and refuses
// while it is told to.
type sink struct {
	mu   sync.Mutex
	rows []access.Record
	down bool
}

func (s *sink) write(_ context.Context, rs []access.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return errors.New("the database is not there")
	}

	s.rows = append(s.rows, rs...)
	return nil
}

func (s *sink) written() []access.Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]access.Record{}, s.rows...)
}

func TestARowIsEachTenantsWhoseDataTheCallCarried(t *testing.T) {
	x := require.New(t)

	actor, held, other := pdid.New(2), pdid.New(1), pdid.New(1)
	ctx := frame.Into(t.Context(), frame.New(actor, held, frame.Whole()))

	a, b, c := pdid.New(2), pdid.New(2), pdid.New(2)
	rs := access.Records(ctx, "/app.People/List", access.Read, "ok", []access.Subject{
		{Id: a},
		{Id: b, Tenant: other},
		{Id: c, Tenant: held},
	}, nil, time.Now())

	x.Len(rs, 2)
	x.Equal(held, rs[0].Tenant, "a subject that named no tenant is the call's")
	x.Equal([]pdid.Id{a, c}, rs[0].Subjects)
	x.Equal(other, rs[1].Tenant)
	x.Equal([]pdid.Id{b}, rs[1].Subjects)
	for _, r := range rs {
		x.Equal(actor, r.Actor)
		x.Equal(held, r.ActorTenant)
	}
}

func TestARowNamesAThousandAndCountsTheRest(t *testing.T) {
	x := require.New(t)

	vs := make([]access.Subject, access.MaxSubjects+5)
	for i := range vs {
		vs[i] = access.Subject{Id: pdid.New(2)}
	}

	rs := access.Records(t.Context(), "/app.People/Export", access.Download, "ok", vs, nil, time.Now())
	x.Len(rs, 1)
	x.Len(rs[0].Subjects, access.MaxSubjects)
	x.Equal(access.MaxSubjects+5, rs[0].Count)
}

func TestAReadWaitsAndIsWrittenWithTheOthers(t *testing.T) {
	x := require.New(t)
	s := &sink{}
	w := &access.Writer{Sink: s.write, Max: 3}

	r := access.Record{Id: pdid.New(6)}
	w.Queue(r, r)
	x.Equal(2, w.Pending())
	x.Empty(s.written())

	// A sink that cannot be written keeps what waits for the next try.
	s.down = true
	x.Error(w.Flush(t.Context()))
	x.Equal(2, w.Pending())

	// And past Max, the oldest go: loudly, in the log.
	w.Queue(r, r)
	x.Equal(3, w.Pending())

	s.down = false
	x.NoError(w.Flush(t.Context()))
	x.Zero(w.Pending())
	x.Len(s.written(), 3)
}

func TestWhatWaitsIsWrittenAsTheProcessStops(t *testing.T) {
	x := require.New(t)
	s := &sink{}

	// A clock that never comes round: only the stop writes.
	w := &access.Writer{Sink: s.write, Every: time.Hour}
	w.Queue(access.Record{Id: pdid.New(6)})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.Spin(ctx) }()

	cancel()
	x.NoError(<-done)
	x.Len(s.written(), 1)
	x.Zero(w.Pending())
}

// handled is a unary call through the interceptor, answering `res`.
func handled(t *testing.T, c access.Config, ctx context.Context, method string, res proto.Message, herr error) (any, error) {
	t.Helper()

	return c.Unary()(ctx, wrapperspb.String("asked"), &grpc.UnaryServerInfo{FullMethod: method},
		func(context.Context, any) (any, error) { return res, herr })
}

func TestADownloadIsRefusedWithoutAReason(t *testing.T) {
	x := require.New(t)
	s := &sink{}
	who := access.Subject{Id: pdid.New(2)}
	c := access.Config{
		Subjects:  func(proto.Message) []access.Subject { return []access.Subject{who} },
		Downloads: []string{"/app.People/Export"},
		Writer:    &access.Writer{Sink: s.write},
	}

	_, err := handled(t, c, t.Context(), "/app.People/Export", wrapperspb.String("everyone"), nil)
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Empty(s.written())

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(access.ReasonKey, "an audit"))
	res, err := handled(t, c, ctx, "/app.People/Export", wrapperspb.String("everyone"), nil)
	x.NoError(err)
	x.NotNil(res)

	rs := s.written()
	x.Len(rs, 1, "written before it answered")
	x.Equal(access.Download, rs[0].Kind)
	x.Equal("an audit", rs[0].Detail.GetReason())

	asked := &wrapperspb.StringValue{}
	x.NoError(proto.Unmarshal(rs[0].Detail.GetRequest(), asked))
	x.Equal("asked", asked.GetValue())

	t.Run("and refused when it cannot be recorded", func(t *testing.T) {
		x := require.New(t)

		s.down = true
		t.Cleanup(func() { s.down = false })

		res, err := handled(t, c, ctx, "/app.People/Export", wrapperspb.String("everyone"), nil)
		x.Equal(codes.Unavailable, status.Code(err))
		x.Nil(res)
	})
}

func TestWhatTheAppSawIsRecordedWithTheAnswer(t *testing.T) {
	x := require.New(t)
	s := &sink{}
	w := &access.Writer{Sink: s.write}
	c := access.Config{Writer: w}

	who := access.Subject{Id: pdid.New(2)}
	_, err := c.Unary()(t.Context(), wrapperspb.String("asked"), &grpc.UnaryServerInfo{FullMethod: "/app.History/Read"},
		func(ctx context.Context, _ any) (any, error) {
			// A name put into a sentence, which no declaration sees.
			access.Saw(ctx, who, who)
			return wrapperspb.String("alice renamed the robot"), nil
		})
	x.NoError(err)
	x.NoError(w.Flush(t.Context()))

	rs := s.written()
	x.Len(rs, 1)
	x.Equal(access.Read, rs[0].Kind)
	x.Equal([]pdid.Id{who.Id}, rs[0].Subjects)
}

func TestACallThatCarriedNobodyLeavesNothing(t *testing.T) {
	x := require.New(t)
	s := &sink{}
	w := &access.Writer{Sink: s.write}
	c := access.Config{
		Subjects: func(proto.Message) []access.Subject { return nil },
		Writer:   w,
	}

	_, err := handled(t, c, t.Context(), "/app.Robots/List", wrapperspb.String("a robot"), nil)
	x.NoError(err)
	x.Zero(w.Pending())

	// And outside a call, what the app says goes nowhere.
	access.Saw(t.Context(), access.Subject{Id: pdid.New(2)})
	_, ok := access.Of(t.Context())
	x.False(ok)
}

func TestACallRecordsWhatItWroteOnce(t *testing.T) {
	x := require.New(t)

	who, other := access.Subject{Id: pdid.New(2)}, access.Subject{Id: pdid.New(2)}
	ctx, call := access.Into(t.Context(), "/app.People/Rename")
	call.Wrote(who)
	call.Wrote(who)
	access.Saw(ctx, who, other)

	// Somebody it wrote is a row of the write, though it showed them too.
	wrote, others := call.Unsaid()
	x.Equal([]access.Subject{who}, wrote)
	x.Equal([]access.Subject{other}, others)

	// What a row said, it does not say again.
	call.Said(true, wrote...)
	wrote, others = call.Unsaid()
	x.Empty(wrote)
	x.Equal([]access.Subject{other}, others)

	// And somebody it showed and then wrote is a row of each.
	call.Said(false, other)
	call.Wrote(other)
	wrote, others = call.Unsaid()
	x.Equal([]access.Subject{other}, wrote)
	x.Empty(others)
}

// logged is a log that keeps what it is told.
type logged struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logged) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(p)
}

func (l *logged) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.String()
}

// TestAWriterSaysWhatItLostInTheProcesssLog: what is lost is lost loudly, in
// the log the process was handed -- the one a context with nothing in it
// reaches is the global, and nothing installs that.
func TestAWriterSaysWhatItLostInTheProcesssLog(t *testing.T) {
	x := require.New(t)

	l := &logged{}
	ctx, cancel := context.WithCancel(log.Into(t.Context(), slog.New(slog.NewTextHandler(l, nil))))
	defer cancel()

	s := &sink{down: true}
	w := &access.Writer{Sink: s.write, Max: 1, Every: time.Hour}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Spin(ctx)
	}()

	r := access.Record{Id: pdid.New(1), Kind: access.Read}
	x.Eventually(func() bool {
		w.Queue(r, r)
		return strings.Contains(l.String(), "were dropped")
	}, 5*time.Second, 10*time.Millisecond)

	cancel()
	<-done
}
