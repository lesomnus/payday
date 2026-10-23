package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/core"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The sentences of the past: an event said when it is read, in the reader's
// language and zone, with the people in it named as they are called then --
// each of them on the access record of the call that said it (payday#151).

// serviced records that `by` serviced `m`, as a caller of the tenant does,
// and answers the event.
func (b *built) serviced(t *testing.T, ctx context.Context, m *app.Machine, by pdid.Id) pdid.Id {
	t.Helper()

	var id pdid.Id
	require.NoError(t, b.operate(t, ctx, func(ctx context.Context, s past.Store) error {
		_, op, err := past.Begin(ctx, s, past.Event{
			Subject: idOf(t, m.GetId()),
			Value: app.MachineServiced_builder{
				Machine: m.GetId(),
				By:      by.Bytes(),
				Mode:    app.Mode_MODE_SERVICE,
				Parts:   []*app.MachineServiced_Part{app.MachineServiced_Part_builder{Serial: "P-1", Count: 2}.Build()},
				Swapped: map[string]*app.MachineServiced_Part{"left": app.MachineServiced_Part_builder{Serial: "P-0", Count: 1}.Build()},
				Due:     timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
			}.Build(),
		})
		if err != nil {
			return err
		}
		id = op.Id()

		return nil
	}))

	return id
}

// said is the event `id` as `s` says it to a reader of `locale` in `zone`,
// read as a page of events is: the row through the past, its names through
// the app's `pd.PastNames`, in a reading of its own.
func (b *built) said(t *testing.T, ctx context.Context, s *past.Sentences, locale string, zone *time.Location, id pdid.Id) string {
	t.Helper()

	vs, err := past.Events(ctx, b.pastOf(t, b.Drv), past.EventQuery{Ids: []pdid.Id{id}})
	require.NoError(t, err)
	require.Len(t, vs, 1)

	r := s.Read(ctx, pd.PastNames(b.Drv), locale, zone)
	v := r.Say(vs[0])
	require.NoError(t, r.Err())

	return v
}

// rename is the holder `id` called `name` from now on.
func (b *built) rename(t *testing.T, ctx context.Context, id pdid.Id, name string) {
	t.Helper()

	_, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:  app.HolderRef_builder{Id: id.Bytes()}.Build(),
		Name: z.Ptr(name),

		DateUpdatedForce: z.Ptr(true),
	}.Build())
	require.NoError(t, err)
}

// TestAnEventNamesPeopleAsTheyAreCalledNow: an event holds whoever is in it
// by identifier, and a sentence of it says them by the name they have when it
// is read -- renamed, the new one -- and the things it holds by value as it
// holds them, each moment in the reader's zone.
func TestAnEventNamesPeopleAsTheyAreCalledNow(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)
	seoul, err := time.LoadLocation("Asia/Seoul")
	x.NoError(err)

	m := machine(t, b, as)
	alice := b.person(t, ctx, "alice")
	ev := b.serviced(t, as, m, alice)
	s := core.Sentences(b.Walled.Event())

	x.Equal("alice serviced m-01; put in 2 × P-1; swapped left for P-0; left it in service; due Nov 1 00:00",
		b.said(t, as, s, "en", nil, ev))

	b.rename(t, ctx, alice, "Alicia")
	x.Equal("Alicia serviced m-01; put in 2 × P-1; swapped left for P-0; left it in service; due Nov 1 00:00",
		b.said(t, as, s, "en", nil, ev), "by the name she has now")
	x.Equal("Alicia: m-01 정비; P-1 2개 넣음; left 자리를 P-0(으)로 바꿈; 정비 중(으)로 둠; 다음 정비 11월 1일 09:00",
		b.said(t, as, s, "ko", seoul, ev), "in Korean, in Seoul")

	// The names are read through the wall: an event may hold anybody's
	// identifier, and a reader is told the name of nobody they could not
	// read for themselves.
	t.Run("and nobody the reader may not see", func(t *testing.T) {
		x := require.New(t)

		other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
		x.NoError(err)
		v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
			Alias:  "mallory",
		}.Build())
		x.NoError(err)
		mallory := idOf(t, v.GetId())

		ctx, call := access.Into(as, "/test.Past/Say")
		x.Equal(mallory.String()+" serviced m-01; put in 2 × P-1; swapped left for P-0; left it in service; due Nov 1 00:00",
			b.said(t, ctx, s, "en", nil, b.serviced(t, as, m, mallory)))

		_, saw := call.Unsaid()
		x.Empty(saw, "and nobody's data shown")
	})
}

// TestWhomASentenceNamesIsOnTheAccessRecord: a name a sentence says is that
// person's data shown, where no declaration of the answer can see it -- so a
// call that says events is a row of the access record naming each person
// they named, and nobody they did not: not a robot, a cell or a machine,
// whose names are nobody's, and not a person the events do not name.
func TestWhomASentenceNamesIsOnTheAccessRecord(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)

	alice := b.person(t, ctx, "alice")
	b.person(t, ctx, "bob")
	m := machine(t, b, as)
	serviced := b.serviced(t, as, m, alice)

	cell, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "floor-1",
	}.Build())
	x.NoError(err)
	r := b.arm(t, ctx, "arm-01")
	_, err = b.Walled.Robot().Move(as, app.RobotMoveRequest_builder{
		Ref: app.RobotRef_builder{Id: r.GetId()}.Build(),
		To:  app.CellRef_builder{Id: cell.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	st := b.pastOf(t, b.Drv)
	moves, err := past.Events(as, st, past.EventQuery{Subjects: []pdid.Id{idOf(t, r.GetId())}})
	x.NoError(err)
	x.Len(moves, 1)

	s := core.Sentences(b.Walled.Event())
	var said []string
	c := access.Config{Subjects: pd.Subjects, Refs: pd.SubjectRefs, Writer: b.Access}
	_, err = c.Unary()(as, &emptypb.Empty{}, &grpc.UnaryServerInfo{FullMethod: "/test.Past/Say"},
		func(ctx context.Context, _ any) (any, error) {
			vs, err := past.Events(ctx, st, past.EventQuery{Ids: []pdid.Id{moves[0].Id, serviced}})
			if err != nil {
				return nil, err
			}

			r := s.Read(ctx, pd.PastNames(b.Drv), "en", nil)
			for _, v := range vs {
				said = append(said, r.Say(v))
			}
			return &emptypb.Empty{}, r.Err()
		})
	x.NoError(err)
	x.Equal([]string{
		"alice serviced m-01; put in 2 × P-1; swapped left for P-0; left it in service; due Nov 1 00:00",
		"admin put arm-01 in floor-1",
	}, said)

	rs := b.accessed(t, ctx)
	x.Len(rs, 1)
	x.Equal("read", rs[0].Kind)
	x.Equal("/test.Past/Say", rs[0].Action)
	x.ElementsMatch(subjects(b.Holder, alice), rs[0].Subjects, "who moved the robot, and who serviced the machine")
}

// TestAnEventIsSaidInTheLanguageNearestTheReaders: in the reader's locale, or
// its language, or the app's default; and where nothing says it at all, as
// its kind and what it holds -- each identifier as itself, which names
// nobody, so that no event is said as nothing.
func TestAnEventIsSaidInTheLanguageNearestTheReaders(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)

	m := machine(t, b, as)
	alice := b.person(t, ctx, "alice")
	ev := b.serviced(t, as, m, alice)
	s := core.Sentences(b.Walled.Event())

	en := "alice serviced m-01; put in 2 × P-1; swapped left for P-0; left it in service; due Nov 1 00:00"
	ko := "alice: m-01 정비; P-1 2개 넣음; left 자리를 P-0(으)로 바꿈; 정비 중(으)로 둠; 다음 정비 11월 1일 00:00"
	for locale, want := range map[string]string{
		"en":    en,
		"en-GB": en,
		"ko":    ko,
		"ko-KR": ko,
		"ko_kr": ko,
		"fr":    en,
		"":      en,
	} {
		x.Equal(want, b.said(t, as, s, locale, nil, ev), "a reader of %q", locale)
	}

	t.Run("and nothing says it", func(t *testing.T) {
		x := require.New(t)

		// An app that says only how a robot moves.
		s := past.NewSentences("en")
		past.Say(s, "en", func(*past.Reading, past.EventRow, *app.RobotMoved) string { return "moved" })

		vs, err := past.Events(as, b.pastOf(t, b.Drv), past.EventQuery{Ids: []pdid.Id{ev}})
		x.NoError(err)

		ctx, call := access.Into(as, "/test.Past/Say")
		r := s.Read(ctx, pd.PastNames(b.Drv), "ko", nil)
		x.Equal("machine.service {"+
			"machine: "+idOf(t, m.GetId()).String()+", "+
			"by: "+alice.String()+", "+
			"mode: MODE_SERVICE, "+
			`parts: [{serial: "P-1", count: 2}], `+
			`swapped: {"left": {serial: "P-0", count: 1}}, `+
			"due: 2026-11-01 00:00"+
			"}", r.Say(vs[0]))
		x.NoError(r.Err())

		_, saw := call.Unsaid()
		x.Empty(saw, "an identifier said as itself shows nobody's data")
	})
}

// TestEveryKindIsSaidInEveryLanguage: each kind of event this app declares
// is said in English and in Korean -- each in its own language, not in the
// default -- which is the test an app adds so that a kind it declares later
// is not read as its fields.
func TestEveryKindIsSaidInEveryLanguage(t *testing.T) {
	x := require.New(t)
	s := core.Sentences(app.UnimplementedEventServiceServer{})

	x.Len(pd.EventKinds, 4, "each of this app's kinds, so that a test of none is not a test of all")
	for _, l := range []string{"en", "ko"} {
		x.Empty(past.Unsaid(s, l, pd.EventKinds), "unsaid in %s", l)
	}
	x.Equal([]string{"app.MachineStarted", "correct", "machine.service", "robot.move"}, past.Unsaid(s, "fr", pd.EventKinds),
		"and a language it says nothing in says none of them")
}

// TestAnEventFromBeforeThePastIsSaidAsItWasWritten: a row an app adopting its
// own events into payday's table already had holds no message, and keeps the
// sentence it was written with in the app's own field -- which is said in its
// own words, whatever the reader's language, with whoever it marks named as
// they are called now.
func TestAnEventFromBeforeThePastIsSaidAsItWasWritten(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.as(ctx)
	alice := b.person(t, ctx, "alice")
	s := core.Sentences(b.Walled.Event())

	// As it was written: a row of the app's, before the past wrote any.
	before := func(desc string) pdid.Id {
		v, err := b.Ungated.Event().Add(ctx, app.EventAddRequest_builder{
			Tenant:     app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Kind:       "robot.oil",
			ActorId:    alice.Bytes(),
			OccurredAt: timestamppb.New(time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)),
			Desc:       desc,
		}.Build())
		x.NoError(err)

		return idOf(t, v.GetId())
	}
	ev := before("{actor} oiled arm-01")

	x.Equal("alice oiled arm-01", b.said(t, as, s, "en", nil, ev))
	x.Equal("alice oiled arm-01", b.said(t, as, s, "ko", nil, ev), "in the words it was written in, to a reader of any language")
	b.rename(t, ctx, alice, "Alicia")
	x.Equal("Alicia oiled arm-01", b.said(t, as, s, "en", nil, ev), "and whoever it marks, as they are called now")

	x.Equal("robot.oil", b.said(t, as, s, "en", nil, before("")), "and one that kept no sentence, as its kind")
}
