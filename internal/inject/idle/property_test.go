package idle

import (
	"math/rand"
	"testing"

	"ataniq/internal/inject/osc7501"
)

// checkProperty holds one classification to the rule that matters most: idle
// only when every required signal says so. It is used by the sweep, the random
// test and the fuzz test.
func checkProperty(t testing.TB, runtime string, in Input, cl Classification, where string) {
	t.Helper()
	if cl.ComposerEmpty != cl.Screen.ComposerEmpty {
		t.Errorf("%s: ComposerEmpty %v is not the screen's %v", where, cl.ComposerEmpty, cl.Screen.ComposerEmpty)
	}
	if cl.State == StateEnded && !in.Ended {
		t.Errorf("%s: ended without Ended", where)
	}
	if in.Ended && cl.State != StateEnded {
		t.Errorf("%s: Ended gave %q", where, cl.State)
	}
	if cl.ReportOnly && cl.State != StateIdle {
		t.Errorf("%s: ReportOnly with state %q", where, cl.State)
	}
	if cl.State != StateIdle {
		return
	}
	st, sr, tr := in.Report, cl.Screen, cl.Title
	app := appOf(runtime)
	switch {
	case in.Ended:
		t.Errorf("%s: idle but ended", where)
	case st.Stale:
		t.Errorf("%s: idle with a stale report", where)
	case st.Has && st.Report.App != app:
		t.Errorf("%s: idle with a report from app %q", where, st.Report.App)
	case st.Has && (st.Report.State == osc7501.StateBlocked || st.Report.State == osc7501.StateWorking ||
		st.Report.State == osc7501.StateError || st.Report.State == osc7501.StateClear):
		t.Errorf("%s: idle with a %s report", where, st.Report.State)
	case !sr.Known || !in.Screen.AltScreen || !sr.Composer:
		t.Errorf("%s: idle without a known composer on the alternate screen: %+v", where, sr)
	case sr.Dialog || sr.BackgroundTask || sr.Working:
		t.Errorf("%s: idle with a dialog, a background task or a working line: %+v", where, sr)
	case tr.Busy:
		t.Errorf("%s: idle with a busy title", where)
	case runtime == "codex" && (!st.Has || !(st.Report.State == osc7501.StateIdle || st.Report.State == osc7501.StateDone)):
		t.Errorf("%s: codex idle without an idle or done report", where)
	case runtime == "codex" && tr.ActionRequired:
		t.Errorf("%s: codex idle with Action Required", where)
	case runtime == "claude" && !sr.CursorInComposer:
		t.Errorf("%s: claude idle without the cursor in the composer", where)
	case runtime == "claude" && sr.StateHidden && !cl.ReportOnly:
		t.Errorf("%s: claude idle under the paste footer without ReportOnly", where)
	case runtime == "claude" && sr.StateHidden &&
		!(st.Has && (st.Report.State == osc7501.StateIdle || st.Report.State == osc7501.StateDone)):
		t.Errorf("%s: claude ReportOnly idle without an idle or done report", where)
	case !sr.StateHidden && cl.ReportOnly:
		t.Errorf("%s: ReportOnly idle with the state shown on screen", where)
	case runtime == "codex" && cl.ReportOnly:
		t.Errorf("%s: codex idle with ReportOnly", where)
	case runtime == "codex" && !sr.CursorInComposer:
		t.Errorf("%s: codex idle without the cursor in the composer", where)
	case runtime == "claude" && !sr.StateHidden && (in.ExpectedMode == "" || sr.Mode != in.ExpectedMode):
		t.Errorf("%s: claude idle with mode %q, expected %q", where, sr.Mode, in.ExpectedMode)
	}
}

// mutate builds an input from a random choice. It is used by the random test
// and by the fuzz test.
func mutate(all []recorded, fixture, repKind, repState, titleKind, flags uint8, ttl string) (recorded, Input) {
	rec := all[int(fixture)%len(all)]
	in := Input{Screen: rec.snap, ExpectedMode: []string{"manual", "auto", "", "other"}[int(flags>>4)%4]}
	states := []osc7501.State{
		osc7501.StateIdle, osc7501.StateWorking, osc7501.StateDone, osc7501.StateBlocked,
		osc7501.StateError, osc7501.StateClear, "", "bogus",
	}
	apps := []string{appOf(rec.runtime), appOf(rec.runtime), appOf(rec.runtime), "claude-code", "codex", "", "other"}
	switch repKind % 4 {
	case 0: // no report
	default:
		in.Report = osc7501.Status{
			Has:    true,
			Report: osc7501.Report{State: states[int(repState)%len(states)], App: apps[int(repKind>>2)%len(apps)]},
		}
	}
	in.Report.Stale = flags&1 != 0 && repKind%7 == 0 || flags&1 != 0 && flags&2 != 0
	if flags&4 != 0 {
		in.Screen.AltScreen = !in.Screen.AltScreen
	}
	if flags&8 != 0 {
		in.Screen.CursorHidden = !in.Screen.CursorHidden
	}
	in.Ended = flags&0x40 != 0 && flags&0x80 != 0 && repKind%5 == 0
	titles := []string{
		in.Screen.Title, "", "✳ topic", "◐ topic", "⠙ topic", "[ ! ] Action Required | x", "★ x", "plain", "\xff", ttl,
	}
	in.Screen.Title = titles[int(titleKind)%len(titles)]
	return rec, in
}

func TestPropertyRandom(t *testing.T) {
	all := loadAll(t)
	rng := rand.New(rand.NewSource(7501))
	idle := 0
	for i := 0; i < 20000; i++ {
		rec, in := mutate(all, uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)),
			uint8(rng.Intn(256)), uint8(rng.Intn(256)), "x")
		cl := lookup(t, rec.runtime, rec.version).Classify(in)
		if cl.State == StateIdle {
			idle++
		}
		checkProperty(t, rec.runtime, in, cl, rec.runtime+"/"+rec.name)
	}
	if idle < 100 {
		t.Errorf("only %d of 20000 random inputs were idle: the property is not exercised", idle)
	}
}

func FuzzClassify(f *testing.F) {
	for fixture := 0; fixture < 48; fixture += 3 {
		f.Add(uint8(fixture), uint8(1), uint8(0), uint8(0), uint8(0), "")
		f.Add(uint8(fixture), uint8(1), uint8(3), uint8(1), uint8(0), "x")
		f.Add(uint8(fixture), uint8(0), uint8(0), uint8(0), uint8(0x40), "")
	}
	all := loadAll(f)
	f.Fuzz(func(t *testing.T, fixture, repKind, repState, titleKind, flags uint8, ttl string) {
		rec, in := mutate(all, fixture, repKind, repState, titleKind, flags, ttl)
		cl := lookup(t, rec.runtime, rec.version).Classify(in)
		checkProperty(t, rec.runtime, in, cl, rec.runtime+"/"+rec.name)
	})
}

// A snapshot from no recording at all: a fuzzed screen text with a fixture's
// geometry never panics.
func TestShuffledText(t *testing.T) {
	all := loadAll(t)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		rec := all[rng.Intn(len(all))]
		b := []byte(rec.snap.Text)
		for j := 0; j < 1+rng.Intn(8) && len(b) > 0; j++ {
			b[rng.Intn(len(b))] = byte(rng.Intn(256))
		}
		in := Input{Screen: rec.snap, ExpectedMode: "manual", Report: status(osc7501.StateIdle, appOf(rec.runtime))}
		in.Screen.Text = string(b)
		cl := lookup(t, rec.runtime, rec.version).Classify(in)
		checkProperty(t, rec.runtime, in, cl, rec.runtime+"/"+rec.name)
	}
}
