package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/terminalconsole"
)

func eightWords() []string {
	return []string{"amber", "basil", "cedar", "delta", "ember", "fable", "grain", "haven"}
}

const pendingID = "0123456789abcdef"

// TestThePhasesOfTheAdapterAreTheWiresOwn holds the adapter's copy of the
// wire's phase words (the command may not import the wire package) equal to
// the wire's, and the word count the view wants equal to the wire's.
func TestThePhasesOfTheAdapterAreTheWiresOwn(t *testing.T) {
	for _, c := range []struct{ name, mine, wire string }{
		{"none", wirePairNone, ownerwire.PairNone},
		{"waiting", wirePairWaiting, ownerwire.PairWaiting},
		{"pending", wirePairPending, ownerwire.PairPending},
		{"expired", wirePairExpired, ownerwire.PairExpired},
	} {
		if c.mine != c.wire {
			t.Errorf("%s: adapter %q, wire %q", c.name, c.mine, c.wire)
		}
	}
	if terminalconsole.PairWordCount != ownerwire.PairWords {
		t.Errorf("the view wants %d words, the wire sends %d", terminalconsole.PairWordCount, ownerwire.PairWords)
	}
	if pairTimeLayout != "2006-01-02T15:04:05Z" {
		t.Errorf("layout %q", pairTimeLayout)
	}
}

func TestStateFromTakesAPendingStatusOnlyWithTheViewsWordCount(t *testing.T) {
	end := "2026-11-22T10:00:00Z"
	st, err := stateFrom(wirePairPending, eightWords(), pendingID, end)
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != terminalconsole.PairPending || st.PendingID != pendingID || len(st.Words) != terminalconsole.PairWordCount ||
		st.Words[0] != "amber" || st.Words[terminalconsole.PairWordCount-1] != "haven" ||
		!st.ExpiresAt.Equal(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("state = %+v", st)
	}
	// Any other count is a bad answer, and none of the words is passed on.
	for _, n := range []int{0, 1, terminalconsole.PairWordCount - 1, terminalconsole.PairWordCount + 1, 2 * terminalconsole.PairWordCount} {
		words := make([]string, n)
		for i := range words {
			words[i] = "word"
		}
		st, err := stateFrom(wirePairPending, words, pendingID, end)
		if !errors.Is(err, errPairAnswer) || st != (terminalconsole.PairState{}) {
			t.Errorf("%d words: state %+v, error %v", n, st, err)
		}
	}
	// A pending status with no id cannot be confirmed or rejected.
	if _, err := stateFrom(wirePairPending, eightWords(), "", end); !errors.Is(err, errPairAnswer) {
		t.Errorf("no id: %v", err)
	}
}

func TestStateFromReadsTheOtherPhasesAndRefusesWhatItDoesNotKnow(t *testing.T) {
	end := "2026-11-22T10:00:00Z"
	for _, c := range []struct {
		phase string
		want  terminalconsole.PairPhase
	}{
		{wirePairWaiting, terminalconsole.PairWaiting},
		{wirePairExpired, terminalconsole.PairExpired},
	} {
		st, err := stateFrom(c.phase, nil, "", end)
		if err != nil || st.Phase != c.want || st.ExpiresAt.IsZero() {
			t.Errorf("%s: %+v, %v", c.phase, st, err)
		}
		// Words and an id are not carried by a phase that has none.
		st, _ = stateFrom(c.phase, eightWords(), pendingID, end)
		if st.PendingID != "" || st.Words != [terminalconsole.PairWordCount]string{} {
			t.Errorf("%s carries words or an id: %+v", c.phase, st)
		}
		if _, err := stateFrom(c.phase, nil, "", ""); !errors.Is(err, errPairAnswer) {
			t.Errorf("%s with no time: %v", c.phase, err)
		}
	}
	st, err := stateFrom(wirePairNone, nil, "", "")
	if err != nil || st.Phase != terminalconsole.PairNone || !st.ExpiresAt.IsZero() {
		t.Errorf("none: %+v, %v", st, err)
	}
	for _, c := range []struct{ phase, expires string }{
		{"", ""}, {"open", end}, {"PENDING", end}, {wirePairWaiting, "yesterday"},
		{wirePairWaiting, "2026-11-22 10:00:00"}, {wirePairWaiting, "2026-11-22T10:00:00+01:00"}, {wirePairNone, "garbage"},
	} {
		if st, err := stateFrom(c.phase, eightWords(), pendingID, c.expires); !errors.Is(err, errPairAnswer) || st != (terminalconsole.PairState{}) {
			t.Errorf("%q %q: %+v, %v", c.phase, c.expires, st, err)
		}
	}
}

func TestOfferFromNeedsACodeAndAReadableTime(t *testing.T) {
	o, err := offerFrom("0123456789ABCDEFGHJK", "2026-11-22T10:00:00Z")
	if err != nil || o.Code != "0123456789ABCDEFGHJK" || !o.ExpiresAt.Equal(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("offer = %+v, %v", o, err)
	}
	for _, c := range [][2]string{{"", "2026-11-22T10:00:00Z"}, {"CODE", ""}, {"CODE", "soon"}} {
		o, err := offerFrom(c[0], c[1])
		if !errors.Is(err, errPairAnswer) || o != (terminalconsole.PairOffer{}) {
			t.Errorf("%q %q: %+v, %v", c[0], c[1], o, err)
		}
	}
	if strings.Contains(errPairAnswer.Error(), "CODE") {
		t.Error("the error holds the code")
	}
}

// origin files

const testOrigin = "https://app.example"

// configHome makes a configuration folder with the owner-local folder in it
// and returns its getenv and the origin file's path.
func configHome(t *testing.T) (func(string) string, string) {
	t.Helper()
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "ataniq", "owner-local")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return cfg
		}
		return ""
	}, filepath.Join(dir, "seat-origin")
}

func writeOrigin(t *testing.T, file, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, mode); err != nil {
		t.Fatal(err)
	}
}

func TestThePairingOriginComesFromTheOwnerLocalFileAndFromNowhereElse(t *testing.T) {
	t.Run("a good file", func(t *testing.T) {
		getenv, file := configHome(t)
		writeOrigin(t, file, testOrigin+"\n", 0o600)
		if got := pairOrigin(getenv); got != testOrigin {
			t.Fatalf("origin = %q", got)
		}
	})
	t.Run("several lines give the first", func(t *testing.T) {
		getenv, file := configHome(t)
		writeOrigin(t, file, "  "+testOrigin+"  \nhttps://other.example\n", 0o600)
		if got := pairOrigin(getenv); got != testOrigin {
			t.Fatalf("origin = %q", got)
		}
	})
	t.Run("missing", func(t *testing.T) {
		getenv, _ := configHome(t)
		if got := pairOrigin(getenv); got != "" {
			t.Fatalf("origin = %q", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		getenv, file := configHome(t)
		writeOrigin(t, file, "", 0o600)
		if got := pairOrigin(getenv); got != "" {
			t.Fatalf("origin = %q", got)
		}
	})
	t.Run("a link", func(t *testing.T) {
		getenv, file := configHome(t)
		target := filepath.Join(filepath.Dir(file), "target")
		writeOrigin(t, target, testOrigin+"\n", 0o600)
		if err := os.Symlink(target, file); err != nil {
			t.Fatal(err)
		}
		if got := pairOrigin(getenv); got != "" {
			t.Fatalf("origin = %q", got)
		}
	})
	t.Run("too big", func(t *testing.T) {
		getenv, file := configHome(t)
		writeOrigin(t, file, testOrigin+"\n"+strings.Repeat("x", 2000), 0o600)
		if got := pairOrigin(getenv); got != "" {
			t.Fatalf("origin = %q", got)
		}
	})
	t.Run("writable by the group", func(t *testing.T) {
		getenv, file := configHome(t)
		writeOrigin(t, file, testOrigin+"\n", 0o660)
		if got := pairOrigin(getenv); got != "" {
			t.Fatalf("origin = %q", got)
		}
	})
	t.Run("writable by others", func(t *testing.T) {
		getenv, file := configHome(t)
		writeOrigin(t, file, testOrigin+"\n", 0o606)
		if got := pairOrigin(getenv); got != "" {
			t.Fatalf("origin = %q", got)
		}
	})
	t.Run("nothing to look in", func(t *testing.T) {
		if got := pairOrigin(func(string) string { return "" }); got != "" {
			t.Fatalf("origin = %q", got)
		}
	})
}
