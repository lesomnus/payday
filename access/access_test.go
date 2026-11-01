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

// TestACallAnAccountMakesAsItselfNamesTheAccount (payday#184): there is no
// holder to name, so the row's actor is the account, and its actor's tenant
// is none -- which no membership's row has, a holder being always some
// tenant's. Whose data it carried is filed under the tenant its row is in, as
// anybody's is, and what was in no tenant's row under none: the deployment's
// own record.
func TestACallAnAccountMakesAsItselfNamesTheAccount(t *testing.T) {
	x := require.New(t)

	acc, acme := pdid.New(2), pdid.New(1)
	ctx := frame.Into(t.Context(), frame.OfAccount(acc, frame.Whole(), nil))

	a, b := pdid.New(2), pdid.New(2)
	rs := access.Records(ctx, "/app.InvitationService/List", access.Read, "ok", []access.Subject{
		{Id: a, Tenant: acme},
		{Id: b},
	}, nil, time.Now())

	x.Len(rs, 2)
	x.Equal(acme, rs[0].Tenant)
	x.Equal([]pdid.Id{a}, rs[0].Subjects)
	x.True(rs[1].Tenant.IsZero(), "in no tenant's row, so in no tenant's record")
	x.Equal([]pdid.Id{b}, rs[1].Subjects)
	for _, r := range rs {
		x.Equal(acc, r.Actor)
		x.True(r.ActorTenant.IsZero())
	}

	// And a membership's call names the membership, as it always did: the
	// account it is of is not who made it.
	h, t1 := pdid.New(2), pdid.New(1)
	ctx = frame.Into(t.Context(), frame.New(h, t1, frame.Whole()).WithAccount(acc))
	rs = access.Records(ctx, "/app.RobotService/List", access.Read, "ok", []access.Subject{{Id: a}}, nil, time.Now())
	x.Len(rs, 1)
	x.Equal(h, rs[0].Actor)
	x.Equal(t1, rs[0].ActorTenant)
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

// TestACallNoFrameNamesIsNamedAsItsOperator: the deployment's own work, done
// at an operator's word -- a command at a shell, which carries no frame -- is
// a row naming the operator, a read's and a write's alike, and a call a frame
// names is named as the frame says.
func TestACallNoFrameNamesIsNamedAsItsOperator(t *testing.T) {
	x := require.New(t)
	s := &sink{}
	w := &access.Writer{Sink: s.write}
	op, owner := pdid.New(2), pdid.New(1)
	who, written := access.Subject{Id: pdid.New(2)}, access.Subject{Id: pdid.New(2)}
	c := access.Config{
		Subjects: func(proto.Message) []access.Subject { return []access.Subject{who} },
		Writer:   w,
		Operator: func(context.Context) (pdid.Id, pdid.Id) { return op, owner },
	}

	var wrote []access.Record
	_, err := c.Unary()(t.Context(), wrapperspb.String("asked"), &grpc.UnaryServerInfo{FullMethod: "/app.People/Rename"},
		func(ctx context.Context, _ any) (any, error) {
			// What the generated recorder writes at a write's commit, which
			// is told of the write and not of the frame: the call in `ctx`
			// says who made it.
			wrote = access.Records(ctx, "/app.People/Rename", access.Write, "ok", []access.Subject{written}, nil, time.Now())

			call, ok := access.Of(ctx)
			x.True(ok)
			a, held := call.Operator()
			x.Equal(op, a)
			x.Equal(owner, held)

			return wrapperspb.String("alice"), nil
		})
	x.NoError(err)
	x.NoError(w.Flush(t.Context()))

	x.Len(wrote, 1)
	x.Equal(op, wrote[0].Actor)
	x.Equal(owner, wrote[0].ActorTenant)

	rs := s.written()
	x.Len(rs, 1)
	x.Equal(access.Read, rs[0].Kind)
	x.Equal(op, rs[0].Actor, "the operator, where no frame names anybody")
	x.Equal(owner, rs[0].ActorTenant)

	t.Run("and a call a frame names is the frame's", func(t *testing.T) {
		x := require.New(t)
		s.rows = nil

		actor, held := pdid.New(2), pdid.New(1)
		ctx := frame.Into(t.Context(), frame.New(actor, held, frame.Whole()))
		_, err := handled(t, c, ctx, "/app.People/List", wrapperspb.String("alice"), nil)
		x.NoError(err)
		x.NoError(w.Flush(t.Context()))

		rs := s.written()
		x.Len(rs, 1)
		x.Equal(actor, rs[0].Actor)
		x.Equal(held, rs[0].ActorTenant)
	})

	t.Run("and with nobody to name, nobody is", func(t *testing.T) {
		x := require.New(t)
		s.rows = nil

		c := c
		c.Operator = nil
		_, err := handled(t, c, t.Context(), "/app.People/List", wrapperspb.String("alice"), nil)
		x.NoError(err)
		x.NoError(w.Flush(t.Context()))

		rs := s.written()
		x.Len(rs, 1)
		x.True(rs[0].Actor.IsZero(), "the deployment's own")
		x.True(rs[0].ActorTenant.IsZero())
	})
}

// TestAReadAtOnceIsWrittenBeforeItAnswers: a process that ends with its call
// writes a read's row before the answer leaves, as a download's is written,
// and refuses the read when it cannot be.
func TestAReadAtOnceIsWrittenBeforeItAnswers(t *testing.T) {
	x := require.New(t)
	s := &sink{}
	w := &access.Writer{Sink: s.write}
	who := access.Subject{Id: pdid.New(2)}
	c := access.Config{
		Subjects: func(proto.Message) []access.Subject { return []access.Subject{who} },
		Refs:     func(proto.Message) []access.Subject { return []access.Subject{who} },
		Writer:   w,
		AtOnce:   true,
	}

	res, err := handled(t, c, t.Context(), "/app.People/Get", wrapperspb.String("alice"), nil)
	x.NoError(err)
	x.NotNil(res)
	x.Zero(w.Pending(), "nothing waits")

	rs := s.written()
	x.Len(rs, 1, "written before it answered")
	x.Equal(access.Read, rs[0].Kind, "a read, written as a download is, and not a download")
	x.Empty(rs[0].Detail.GetReason())

	asked := &wrapperspb.StringValue{}
	x.NoError(proto.Unmarshal(rs[0].Detail.GetRequest(), asked))
	x.Equal("asked", asked.GetValue())

	t.Run("and refused when it cannot be recorded", func(t *testing.T) {
		x := require.New(t)

		s.down = true
		t.Cleanup(func() { s.down = false })

		res, err := handled(t, c, t.Context(), "/app.People/Get", wrapperspb.String("alice"), nil)
		x.Equal(codes.Unavailable, status.Code(err))
		x.ErrorContains(err, access.ErrUnrecorded.Error())
		x.Nil(res, "what could not be recorded is not answered")
		x.Zero(w.Pending(), "and is not left to be written later, as though it had been")

		// A call that was refused anyway keeps its own refusal.
		_, err = handled(t, c, t.Context(), "/app.People/Get", nil, status.Error(codes.NotFound, "nobody"))
		x.Equal(codes.NotFound, status.Code(err))
	})
}

// streamed is a server stream that keeps what it is sent.
type streamed struct {
	grpc.ServerStream
	ctx  context.Context
	sent []proto.Message
}

func (s *streamed) Context() context.Context { return s.ctx }
func (s *streamed) RecvMsg(any) error        { return nil }
func (s *streamed) SendMsg(m any) error {
	s.sent = append(s.sent, m.(proto.Message))
	return nil
}

// TestAStreamAtOnceIsWrittenBeforeEachMessage: a watch from a process that
// ends with its call says what each message carried before the message
// leaves, and sends nothing whose row could not be written.
func TestAStreamAtOnceIsWrittenBeforeEachMessage(t *testing.T) {
	x := require.New(t)
	s := &sink{}
	w := &access.Writer{Sink: s.write}
	alice, bob := access.Subject{Id: pdid.New(2)}, access.Subject{Id: pdid.New(2)}
	c := access.Config{
		Subjects: func(m proto.Message) []access.Subject {
			if m.(*wrapperspb.StringValue).GetValue() == "alice" {
				return []access.Subject{alice}
			}
			return []access.Subject{bob}
		},
		Writer: w,
		AtOnce: true,
	}

	ss := &streamed{ctx: t.Context()}
	err := c.Stream()(nil, ss, &grpc.StreamServerInfo{FullMethod: "/app.People/Watch", IsServerStream: true},
		func(_ any, stream grpc.ServerStream) error {
			x.NoError(stream.SendMsg(wrapperspb.String("alice")))
			x.Len(s.written(), 1, "alice's row, before her message left")

			s.down = true
			err := stream.SendMsg(wrapperspb.String("bob"))
			x.Equal(codes.Unavailable, status.Code(err))
			s.down = false

			return err
		})
	x.Equal(codes.Unavailable, status.Code(err))

	x.Len(ss.sent, 1, "bob's message did not leave")
	rs := s.written()
	x.NotEmpty(rs)
	x.Equal(access.Read, rs[0].Kind)
	x.Equal([]pdid.Id{alice.Id}, rs[0].Subjects)
	x.Zero(w.Pending())
}
