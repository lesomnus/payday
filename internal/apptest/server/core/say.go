package core

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
)

// Sentences is what this app says of its events, in English and in Korean
// (payday#151): English to a reader of any language it says nothing in.
//
// An event keeps no sentence. Each of these is made from the message an event
// holds as it is read, in the reader's language and zone. A person is held by
// identifier and named as they are called now, with `r.Name` -- the holder
// who did it, the holder who serviced a machine -- which also puts them on the
// access record of the call that read the sentence. A thing held by value, a
// part's serial, is said as it was written: that is a record, and nobody's.
// A robot, a machine and a cell are held by identifier too, and named now:
// none of their names is anybody's data, so naming one records nothing.
//
// What reads them is the app's to say, and this one has no RPC that answers
// with a sentence, so its tests read them; an app that answers its events in
// a list builds this once where it starts and says each event of the list
// with the reading it makes for that answer.
//
// `events` is what a row from before this app's events were the past's is
// read through: such a row keeps the sentence it was written with in the
// overlay's `desc`, which the past neither writes nor reads.
func Sentences(events app.EventServiceServer) *past.Sentences {
	s := past.NewSentences("en")
	s.TimeLayout("en", "Jan 2 15:04")
	s.TimeLayout("ko", "1월 2일 15:04")

	past.Say(s, "en", func(r *past.Reading, ev past.EventRow, m *app.RobotMoved) string {
		who, robot := actor(r, ev, "The deployment"), name(r, m.GetRobot())
		from, to := name(r, m.GetFrom()), name(r, m.GetTo())
		switch {
		case from != "" && to != "":
			return fmt.Sprintf("%s moved %s from %s to %s", who, robot, from, to)
		case to != "":
			return fmt.Sprintf("%s put %s in %s", who, robot, to)
		case from != "":
			return fmt.Sprintf("%s took %s out of %s", who, robot, from)
		}
		return fmt.Sprintf("%s moved %s", who, robot)
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.RobotMoved) string {
		who, robot := actor(r, ev, "시스템"), name(r, m.GetRobot())
		from, to := name(r, m.GetFrom()), name(r, m.GetTo())
		switch {
		case from != "" && to != "":
			return fmt.Sprintf("%s: %s을(를) %s에서 %s(으)로 옮김", who, robot, from, to)
		case to != "":
			return fmt.Sprintf("%s: %s을(를) %s에 넣음", who, robot, to)
		case from != "":
			return fmt.Sprintf("%s: %s을(를) %s에서 뺌", who, robot, from)
		}
		return fmt.Sprintf("%s: %s을(를) 옮김", who, robot)
	})

	past.Say(s, "en", func(r *past.Reading, ev past.EventRow, m *app.MachineServiced) string {
		vs := []string{fmt.Sprintf("%s serviced %s", name(r, m.GetBy()), name(r, m.GetMachine()))}
		if ps := m.GetParts(); len(ps) > 0 {
			parts := make([]string, len(ps))
			for i, p := range ps {
				parts[i] = fmt.Sprintf("%d × %s", p.GetCount(), p.GetSerial())
			}
			vs = append(vs, "put in "+strings.Join(parts, ", "))
		}
		for _, k := range slices.Sorted(maps.Keys(m.GetSwapped())) {
			vs = append(vs, fmt.Sprintf("swapped %s for %s", k, m.GetSwapped()[k].GetSerial()))
		}
		if v := modeEn[m.GetMode()]; v != "" {
			vs = append(vs, "left it "+v)
		}
		if m.HasDue() {
			vs = append(vs, "due "+r.Time(m.GetDue().AsTime()))
		}
		return strings.Join(vs, "; ")
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.MachineServiced) string {
		vs := []string{fmt.Sprintf("%s: %s 정비", name(r, m.GetBy()), name(r, m.GetMachine()))}
		if ps := m.GetParts(); len(ps) > 0 {
			parts := make([]string, len(ps))
			for i, p := range ps {
				parts[i] = fmt.Sprintf("%s %d개", p.GetSerial(), p.GetCount())
			}
			vs = append(vs, strings.Join(parts, ", ")+" 넣음")
		}
		for _, k := range slices.Sorted(maps.Keys(m.GetSwapped())) {
			vs = append(vs, fmt.Sprintf("%s 자리를 %s(으)로 바꿈", k, m.GetSwapped()[k].GetSerial()))
		}
		if v := modeKo[m.GetMode()]; v != "" {
			vs = append(vs, v+"(으)로 둠")
		}
		if m.HasDue() {
			vs = append(vs, "다음 정비 "+r.Time(m.GetDue().AsTime()))
		}
		return strings.Join(vs, "; ")
	})

	past.Say(s, "en", func(r *past.Reading, ev past.EventRow, m *app.MachineStarted) string {
		return fmt.Sprintf("%s started %s", actor(r, ev, "The deployment"), name(r, m.GetMachine()))
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.MachineStarted) string {
		return fmt.Sprintf("%s: %s 가동", actor(r, ev, "시스템"), name(r, m.GetMachine()))
	})

	// A correction and a retraction hold one message, and each is said in a
	// sentence of its own, reading what of it its kind uses: a correction the
	// moment the event happened at instead, and a retraction nothing but which
	// event, the event never having happened. So each is said with
	// `past.SayKind`; `past.Say` of a message of several kinds is refused.
	//
	// A retraction was a `correct` holding no moment until `retract` was a
	// kind, and rows of it say so still: a message that takes a kind leaves
	// the rows of its others as they were written.
	past.SayKind(s, "correct", "en", func(r *past.Reading, ev past.EventRow, m *app.Corrected) string {
		if !m.HasAt() {
			return fmt.Sprintf("%s said an earlier event never happened", actor(r, ev, "The deployment"))
		}
		return fmt.Sprintf("%s said an earlier event happened on %s", actor(r, ev, "The deployment"), r.Time(m.GetAt().AsTime()))
	})
	past.SayKind(s, "correct", "ko", func(r *past.Reading, ev past.EventRow, m *app.Corrected) string {
		if !m.HasAt() {
			return fmt.Sprintf("%s: 이전 기록을 없던 일로 함", actor(r, ev, "시스템"))
		}
		return fmt.Sprintf("%s: 이전 기록을 %s의 일로 고침", actor(r, ev, "시스템"), r.Time(m.GetAt().AsTime()))
	})
	past.SayKind(s, "retract", "en", func(r *past.Reading, ev past.EventRow, m *app.Corrected) string {
		return fmt.Sprintf("%s took back an earlier event, which never happened", actor(r, ev, "The deployment"))
	})
	past.SayKind(s, "retract", "ko", func(r *past.Reading, ev past.EventRow, m *app.Corrected) string {
		return fmt.Sprintf("%s: 이전 기록을 없던 일로 되돌림", actor(r, ev, "시스템"))
	})

	// The receipt of an expiry: how many of what went, and from before when.
	// It names nobody and says nothing of what went, which is gone.
	past.Say(s, "en", func(r *past.Reading, ev past.EventRow, m *app.PastExpired) string {
		return fmt.Sprintf("%s let go of what was over before %s (facts: %d, events: %d, stints: %d)",
			actor(r, ev, "The deployment"), r.Time(m.GetBefore().AsTime()), m.GetFacts(), m.GetEvents(), m.GetStints())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PastExpired) string {
		return fmt.Sprintf("%s: %s 이전에 끝난 기록 정리 (사실 %d건, 이벤트 %d건, 배치 %d건)",
			actor(r, ev, "시스템"), r.Time(m.GetBefore().AsTime()), m.GetFacts(), m.GetEvents(), m.GetStints())
	})

	// A row from before: the sentence it was written with, which marks who
	// did it as `{actor}` -- named now, as every sentence names a person -- in
	// whatever language it was written in.
	s.Legacy(func(r *past.Reading, ev past.EventRow) string {
		v, err := events.Get(r.Context(), app.EventGetRequest_builder{
			Ref: app.EventRef_builder{Id: ev.Id.Bytes()}.Build(),
		}.Build())
		if err != nil {
			r.Fail(err)
			return ""
		}
		if !strings.Contains(v.GetDesc(), "{actor}") {
			return v.GetDesc()
		}

		return strings.ReplaceAll(v.GetDesc(), "{actor}", actor(r, ev, "?"))
	})

	return s
}

// actor is who did `ev`, as they are called now, and `system` for the
// deployment's own work: no identifier, which is nobody to name.
func actor(r *past.Reading, ev past.EventRow, system string) string {
	if ev.Actor.IsZero() {
		return system
	}

	return r.Name(ev.Actor)
}

// name is what the row an event message holds the identifier of is called
// now, and nothing for none: a robot in no cell.
func name(r *past.Reading, id []byte) string {
	v, err := pdid.From(id)
	if err != nil {
		return ""
	}

	return r.Name(v)
}

var (
	modeEn = map[app.Mode]string{app.Mode_MODE_IDLE: "idle", app.Mode_MODE_RUN: "running", app.Mode_MODE_SERVICE: "in service"}
	modeKo = map[app.Mode]string{app.Mode_MODE_IDLE: "대기", app.Mode_MODE_RUN: "가동", app.Mode_MODE_SERVICE: "정비 중"}
)
