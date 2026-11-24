package seatlink

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/chat/standin"
	"ataniq/internal/seatapp"
)

// TestBudgetStats: the stats are, for each kind, the calls taken and
// refused in the last BudgetWindow; a challenge and its answer are each
// taken, and a wait that found no room is no refusal.
func TestBudgetStats(t *testing.T) {
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now}
	for i := 0; i < readRoom+3; i++ {
		b.take(kindRead)
	}
	tok, ok := b.reserve(kindDecide)
	if !ok {
		t.Fatal("a challenge refused")
	}
	b.bind(tok, "id", clock.now().Add(time.Hour))
	clock.advance(time.Second)
	b.answer(kindDecide, "id")
	for i := 0; i <= waitRoom; i++ {
		b.takeOrWait(kindWait)
	}
	b.take(kindRead)
	// Chat's room is its own: its calls and refusals are counted apart
	// from the reads'.
	for i := 0; i <= chatRoom; i++ {
		b.take(kindChat)
	}
	want := budgetStats{kindRead: {readRoom, 4}, kindChat: {chatRoom, 1}, kindDecide: {2, 0}, kindWait: {waitRoom, 0}}
	if got := b.stats(); got != want {
		t.Fatalf("stats %v, want %v", got, want)
	}
	// The reads of the first second leave the window first, with the
	// refusals counted with them; the calls of a second later stay.
	clock.advance(BudgetWindow - time.Second)
	want = budgetStats{kindRead: {0, 1}, kindChat: {chatRoom, 1}, kindDecide: {1, 0}, kindWait: {waitRoom, 0}}
	if got := b.stats(); got != want {
		t.Fatalf("stats a window on %v, want %v", got, want)
	}
	clock.advance(time.Second)
	if got := b.stats(); got != (budgetStats{}) {
		t.Fatalf("stats two windows on %v", got)
	}
	// Refusals in one second are one step, so what is kept is bounded.
	for i := 0; i < 2*readRoom+1000; i++ {
		b.take(kindRead)
	}
	if n := len(b.refusedAt[kindRead]); n != 1 || b.stats()[kindRead].refused != readRoom+1000 {
		t.Fatalf("%d steps, %d refused", n, b.stats()[kindRead].refused)
	}
}

// TestBudgetStatsLine: a period of StatsLogEvery that refused a call is
// told in one line of counts, at the first call after it ends, and a
// period that refused nothing is not told. The line is fixed text and
// numbers: it holds nothing of a call.
func TestBudgetStatsLine(t *testing.T) {
	var l logLines
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now, logf: l.logf}
	statsLines := func() []string {
		var out []string
		for _, line := range l.all() {
			if strings.Contains(line, "taken/refused") {
				out = append(out, line)
			}
		}
		return out
	}
	const id = "0123456789abcdef0123456789abcdef"
	// The first period: room for every call.
	for i := 0; i < 5; i++ {
		b.take(kindRead)
		clock.advance(time.Minute)
	}
	clock.advance(StatsLogEvery)
	b.take(kindRead)
	if got := statsLines(); len(got) != 0 {
		t.Fatalf("a period with no refusal told: %q", got)
	}
	// The second: a decision, a login answer with no room kept, reads
	// past the room and waits.
	tok, _ := b.reserve(kindDecide)
	b.bind(tok, id, clock.now().Add(time.Hour))
	b.answer(kindDecide, id)
	for i := 0; i < publicRoom; i++ {
		b.take(kindPublic)
	}
	b.answer(kindPublic, id)
	for i := 0; i < readRoom+2; i++ {
		b.take(kindRead)
	}
	for i := 0; i < chatRoom+1; i++ {
		b.take(kindChat)
	}
	b.takeOrWait(kindWait)
	clock.advance(StatsLogEvery - time.Second)
	b.take(kindGenesis)
	if got := statsLines(); len(got) != 0 {
		t.Fatalf("told within the period: %q", got)
	}
	clock.advance(time.Second)
	b.takeOrWait(kindWait)
	want := fmt.Sprintf("ataniq-seat: calls of the link in the last 10 minutes, taken/refused by kind: public %d/1, read %d/3, chat %d/1, decide 2/0, genesis 1/0, wait 1/0\n", publicRoom, readRoom, chatRoom)
	got := statsLines()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("lines %q, want %q", got, want)
	}
	if strings.Contains(got[0], id) {
		t.Fatal("the line holds a challenge id")
	}
	// The next period starts with the call that ended the last: at most
	// one line in StatsLogEvery.
	for i := 0; i < 3; i++ {
		clock.advance(StatsLogEvery / 2)
		for j := 0; j <= genesisRoom; j++ {
			b.take(kindGenesis)
		}
	}
	got = statsLines()
	if len(got) != 2 || !strings.Contains(got[1], "in the last 10 minutes,") || !strings.Contains(got[1], "genesis 12/1,") {
		t.Fatalf("lines %q", got)
	}
	// With no log, nothing is told and nothing breaks.
	quiet := &budget{now: clock.now}
	for i := 0; i <= readRoom; i++ {
		quiet.take(kindRead)
	}
	clock.advance(StatsLogEvery)
	quiet.take(kindRead)
}

// mixUse is a run of TestBudgetMixedUse: the Card tabs' offsets in their
// 20 seconds (none when cards is false), the seconds between two refreshes
// of a conversation (0: none), and the seconds between two visits of the
// Leads pages (0: none).
type mixUse struct {
	name   string
	tabs   [3]int
	chat   int
	visit  int
	noCard bool // no Card tab and no interview: a single browser on the Leads pages
}

// mixMeasure is what a run of TestBudgetMixedUse measured.
type mixMeasure struct {
	// cardReads and cardRefused are the Card tabs' list reads asked and
	// refused for room; projectLists and projectRefused the tabs' lists of
	// open projects sent and refused (a list the link kept is neither).
	cardReads, cardRefused, projectLists, projectRefused int
	// answers and answersRefused are the interview's answers.
	answers, answersRefused int
	// refreshes, chatShown and chatBusy are the conversation's refreshes,
	// those that showed the conversation and those that met a busy desk.
	refreshes, chatShown, chatBusy int
	// worstReads and worstChat are the most read and chat calls refused in
	// one 80-second window.
	worstReads, worstChat int
	// visits and visitsBusy are the visits of the Leads pages, and those of
	// them that met a busy desk at one of their steps.
	visits, visitsBusy int
}

// mixRun runs one use of TestBudgetMixedUse for 600 seconds, and the call
// at second 600 that ends the stats period. Each second, in order: the
// watcher's wait (one every 25 seconds), each Card tab whose reload is due
// (its card list, a read, and its list of open projects, which the link
// keeps 20 seconds and an interview answer forgets), an interview answer
// (every 12 seconds, from second 6), a conversation refresh (every
// use.chat seconds, from second 3: the directory and, when it was shown, the
// transcript page; a real host, so neither is reused), and the owner's
// login (challenge at 100, login at 104) and two decisions (challenges at
// 200 and 400, decided 6 seconds later), each of which must complete. A
// visit of the Leads pages (every use.visit seconds, from second 3) is the
// owner's ordinary path, cold: the Leads page (its directory) at the
// visit's start, the selected lead's conversation 4 seconds later (the
// directory and the page) and one refresh of it 10 seconds after that (the
// directory and the page again).
func mixRun(t *testing.T, use mixUse) (mixMeasure, []string) {
	t.Helper()
	var l logLines
	var m mixMeasure
	clock := &testClock{t: decideNow}
	d := Daemon{budget: budget{now: clock.now, logf: l.logf}, gnow: clock.now}
	peer := chatTestPeer{since: clock.now(), port: standin.New(clock.now), real: true}
	// tally is what the period's line is to say.
	var tally [numKinds]kindStats
	count := func(k kind, ok bool) {
		if ok {
			tally[k].taken++
		} else {
			tally[k].refused++
		}
	}
	genesis := func(c seatapp.GenesisCall) (sent, refused bool) {
		now := clock.now()
		gen, _, kept := d.gmem.before(c, now)
		if kept {
			return false, false
		}
		ok := d.budget.take(kindGenesis)
		count(kindGenesis, ok)
		var res *ownerwire.SeatGenesisResult
		if ok && c.Verb == seatapp.GenesisVerbList {
			res = &ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateThreads}
		}
		d.gmem.after(c, res, gen, now)
		return ok, !ok
	}
	chat := func(c seatapp.ChatCall) (seatapp.ChatView, bool) {
		v, err := d.chat(context.Background(), c, &peer)
		switch err {
		case nil:
			count(kindChat, true)
			return v, true
		case seatapp.ErrBusy:
			count(kindChat, false)
			return v, false
		}
		t.Fatalf("chat %s: %v", c.Verb, err)
		return v, false
	}
	challenge := func(k kind, id string) {
		t.Helper()
		tok, ok := d.budget.reserve(k)
		if !ok {
			t.Fatalf("%s: second %d: the %s challenge was refused", use.name, clock.now().Sub(decideNow)/time.Second, kindWords[k])
		}
		count(k, true)
		d.budget.bind(tok, id, clock.now().Add(HoldLife))
	}
	answer := func(k kind, id string) {
		t.Helper()
		if !d.budget.answer(k, id) {
			t.Fatalf("%s: second %d: the %s answer was displaced", use.name, clock.now().Sub(decideNow)/time.Second, kindWords[k])
		}
		count(k, true)
	}
	const thread = "0123456789abcdef"
	visitOK := false
	for at := 0; at <= 600; at++ {
		if at == 600 {
			// The period's last second is 599: what it says is the
			// tally so far, and the wait at 600 tells it.
			want := fmt.Sprintf("ataniq-seat: calls of the link in the last 10 minutes, taken/refused by kind: public %d/%d, read %d/%d, chat %d/%d, decide %d/%d, genesis %d/%d, wait %d/%d\n",
				tally[kindPublic].taken, tally[kindPublic].refused, tally[kindRead].taken, tally[kindRead].refused,
				tally[kindChat].taken, tally[kindChat].refused,
				tally[kindDecide].taken, tally[kindDecide].refused, tally[kindGenesis].taken, tally[kindGenesis].refused,
				tally[kindWait].taken, tally[kindWait].refused)
			d.budget.takeOrWait(kindWait)
			var lines []string
			for _, line := range l.all() {
				if strings.Contains(line, "taken/refused") {
					lines = append(lines, line)
				}
			}
			if refused := tally[kindRead].refused + tally[kindChat].refused + tally[kindGenesis].refused; refused > 0 {
				if len(lines) != 1 || lines[0] != want {
					t.Fatalf("%s: stats lines %q, want %q", use.name, lines, want)
				}
			} else if len(lines) != 0 {
				t.Fatalf("%s: stats lines of a period with no refusal: %q", use.name, lines)
			}
			return m, lines
		}
		if at%25 == 0 {
			ok, _ := d.budget.takeOrWait(kindWait)
			if !ok {
				t.Fatalf("%s: second %d: the watcher found no room", use.name, at)
			}
			count(kindWait, true)
		}
		for _, off := range use.tabs {
			if use.noCard || at < off || (at-off)%20 != 0 {
				continue
			}
			m.cardReads++
			ok := d.budget.take(kindRead)
			count(kindRead, ok)
			if !ok {
				m.cardRefused++
			}
			sent, refused := genesis(seatapp.GenesisCall{Verb: seatapp.GenesisVerbList})
			if sent {
				m.projectLists++
			}
			if refused {
				m.projectRefused++
			}
		}
		if !use.noCard && at%12 == 6 {
			m.answers++
			if _, refused := genesis(seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: thread}); refused {
				m.answersRefused++
			}
		}
		if use.chat > 0 && at%use.chat == 3 {
			m.refreshes++
			list, ok := chat(seatapp.ChatCall{Verb: seatapp.ChatLeads})
			if ok {
				head := *list.Rows[0].Head
				_, ok = chat(seatapp.ChatCall{Verb: seatapp.ChatRead, Lead: list.Rows[0].Lead, After: max(0, head-seatapp.MaxChatEntries)})
			}
			if ok {
				m.chatShown++
			} else {
				m.chatBusy++
			}
		}
		if use.visit > 0 && at >= 3 {
			switch (at - 3) % use.visit {
			case 0:
				m.visits++
				visitOK = true
				_, ok := chat(seatapp.ChatCall{Verb: seatapp.ChatLeads})
				visitOK = visitOK && ok
			case 4, 14:
				list, ok := chat(seatapp.ChatCall{Verb: seatapp.ChatLeads})
				if ok {
					head := *list.Rows[0].Head
					_, ok = chat(seatapp.ChatCall{Verb: seatapp.ChatRead, Lead: list.Rows[0].Lead, After: max(0, head-seatapp.MaxChatEntries)})
				}
				visitOK = visitOK && ok
				if (at-3)%use.visit == 14 && !visitOK {
					m.visitsBusy++
				}
			}
		}
		switch at {
		case 100:
			challenge(kindPublic, "login")
		case 104:
			answer(kindPublic, "login")
		case 200:
			challenge(kindDecide, "decision-1")
		case 206:
			answer(kindDecide, "decision-1")
		case 400:
			challenge(kindDecide, "decision-2")
		case 406:
			answer(kindDecide, "decision-2")
		}
		st := d.budget.stats()
		m.worstReads = max(m.worstReads, st[kindRead].refused)
		m.worstChat = max(m.worstChat, st[kindChat].refused)
		clock.advance(time.Second)
	}
	panic("unreachable")
}

// TestBudgetMixedUse measures the seat link's rooms (budget.go) under a
// mixed use, with the rooms as they are (pings 6, decisions 8, anyone's 10,
// reads 12, chat 6, waits 6, genesis 12): an interview answer every 12
// seconds, three Card tabs reloading every 20 seconds, in step or
// staggered, a conversation of a real lead refreshed by hand every 10
// seconds (two chat calls a refresh, the directory and the page, neither
// reused), every 30 seconds, or not at all, the owner's ordinary visit of
// the Leads pages (the Leads page, the selected lead's conversation and one
// refresh of it, five chat calls cold) every 80 or 20 seconds, alone on one
// browser or beside the tabs, the watcher, one login and two decisions,
// over 600 seconds (521 whole 80-second windows). The login and the
// decisions always complete: their rooms are their own. Chat has a room of
// its own, so a run with chat refuses no Card list read, no list of open
// projects and no interview answer that the same tabs without chat do not:
// chat's load causes no Card refusal; only chat's own calls meet busy. What
// it measured is pinned below and recorded in the design
// (docs/design/seat-gate.md, "Reading chat on the seat"); a change of a room
// or of the link's reuse changes it, and is to be measured and recorded
// again:
//
//   - No Card list read is refused, with chat or without: three tabs take
//     the 12 reads.
//   - A visit of the Leads pages every 80 seconds never meets busy, on one
//     browser or beside the tabs: its five calls fit chat's 6. (With chat's
//     room 2, all 8 visits met busy at their first selection.) Visits every
//     20 seconds meet busy at 22 of 30.
//   - A conversation refreshed every 10 seconds shows 24 of its 60
//     refreshes and meets busy at 36, with up to 5 chat calls refused in
//     one window. Every 30 seconds, all 20 show. (When a refresh's
//     directory is sent and its page then meets busy, the directory's call
//     was spent for nothing; that is chat's own cost.)
//   - With the tabs staggered, the interview's room is short, with chat or
//     without: 7 of the 50 answers, and 20 of the 65 lists of open projects
//     the link did not hold, are refused, since each answer makes the next
//     tab's list a call of the gate. In step, none is. (Before chat had its
//     room, a refresh every 10 seconds had 42 of the 90 Card list reads
//     refused, and every 30 seconds 9.)
func TestBudgetMixedUse(t *testing.T) {
	steps := [3]int{0, 0, 0}
	staggered := [3]int{0, 7, 14}
	baseline := map[[3]int]mixMeasure{}
	for _, c := range []struct {
		use  mixUse
		want mixMeasure
	}{
		{mixUse{name: "tabs in step, no chat", tabs: steps, chat: 0}, mixMeasure{cardReads: 90, projectLists: 30, answers: 50}},
		{mixUse{name: "tabs staggered, no chat", tabs: staggered, chat: 0}, mixMeasure{cardReads: 90, projectLists: 45, projectRefused: 20, answers: 50, answersRefused: 7}},
		{mixUse{name: "tabs in step, chat every 10 s", tabs: steps, chat: 10}, mixMeasure{cardReads: 90, projectLists: 30, answers: 50, refreshes: 60, chatShown: 24, chatBusy: 36, worstChat: 5}},
		{mixUse{name: "tabs staggered, chat every 10 s", tabs: staggered, chat: 10}, mixMeasure{cardReads: 90, projectLists: 45, projectRefused: 20, answers: 50, answersRefused: 7, refreshes: 60, chatShown: 24, chatBusy: 36, worstChat: 5}},
		{mixUse{name: "tabs in step, chat every 30 s", tabs: steps, chat: 30}, mixMeasure{cardReads: 90, projectLists: 30, answers: 50, refreshes: 20, chatShown: 20}},
		{mixUse{name: "tabs staggered, chat every 30 s", tabs: staggered, chat: 30}, mixMeasure{cardReads: 90, projectLists: 45, projectRefused: 20, answers: 50, answersRefused: 7, refreshes: 20, chatShown: 20}},
		{mixUse{name: "one browser, Leads visited every 80 s", visit: 80, noCard: true}, mixMeasure{visits: 8}},
		{mixUse{name: "tabs in step, Leads visited every 80 s", tabs: steps, visit: 80}, mixMeasure{cardReads: 90, projectLists: 30, answers: 50, visits: 8}},
		{mixUse{name: "tabs staggered, Leads visited every 80 s", tabs: staggered, visit: 80}, mixMeasure{cardReads: 90, projectLists: 45, projectRefused: 20, answers: 50, answersRefused: 7, visits: 8}},
		{mixUse{name: "tabs in step, Leads visited every 20 s", tabs: steps, visit: 20}, mixMeasure{cardReads: 90, projectLists: 30, answers: 50, worstChat: 8, visits: 30, visitsBusy: 22}},
	} {
		got, _ := mixRun(t, c.use)
		t.Logf("%s: %+v", c.use.name, got)
		if got != c.want {
			t.Errorf("%s: measured %+v, recorded %+v", c.use.name, got, c.want)
		}
		if c.use.visit > 0 && time.Duration(c.use.visit)*time.Second >= BudgetWindow && got.visitsBusy != 0 {
			t.Errorf("%s: %d of %d visits of the Leads pages met a busy desk", c.use.name, got.visitsBusy, got.visits)
		}
		if c.use.noCard {
			continue
		}
		if c.use.chat == 0 && c.use.visit == 0 {
			baseline[c.use.tabs] = got
			continue
		}
		// Chat's load is no worse for the Cards and the interview than
		// the same tabs without chat: no refusal is chat's doing.
		base, ok := baseline[c.use.tabs]
		if !ok {
			t.Fatalf("%s: no run without chat to compare with", c.use.name)
		}
		if got.cardRefused > base.cardRefused || got.worstReads > base.worstReads ||
			got.projectRefused > base.projectRefused || got.answersRefused > base.answersRefused {
			t.Errorf("%s: chat cost the Cards or the interview: %+v, without chat %+v", c.use.name, got, base)
		}
		if c.use.chat > 0 && (got.chatShown+got.chatBusy != got.refreshes || got.chatShown == 0) {
			t.Errorf("%s: of %d refreshes %d shown and %d busy", c.use.name, got.refreshes, got.chatShown, got.chatBusy)
		}
	}
}
