package noaicheck

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// allocated runs fn and returns the bytes it allocated and the time it
// took.
func allocated(fn func()) (uint64, time.Duration) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	fn()
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, took
}

// The shell parser is charged before it does the work: a profile nested
// deeper than maxNesting, and one whose loop words fan out over many
// sources, end the run with "incomplete budget" within a small time and
// memory bound, instead of recursing or allocating first.
func TestShellParseIsBounded(t *testing.T) {
	const (
		timeBound   = 5 * time.Second
		memoryBound = 64 << 20
	)
	var fanout strings.Builder
	fanout.WriteString("for p in")
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&fanout, " /etc/app/file-%04d.sh", i)
	}
	fanout.WriteString("; do\n")
	for i := 0; i < 3000; i++ {
		fanout.WriteString(". \"$p\"\n")
	}
	fanout.WriteString("done\n")
	cases := []struct {
		name, profile string
		memory        int64
		detail        string
	}{
		{"nested substitutions", "X=" + strings.Repeat("$(", 100000) + strings.Repeat(")", 100000) + "\n", 1 << 30, budgetNesting},
		{"nested parameter expansions", "X=" + strings.Repeat("${a:-", 100000) + strings.Repeat("}", 100000) + "\n", 1 << 30, budgetNesting},
		{"nested quotes in substitutions", "X=" + strings.Repeat("\"$(", 50000) + strings.Repeat(")\"", 50000) + "\n", 1 << 30, budgetNesting},
		{"nested brace groups", strings.Repeat("{ ", 100000) + strings.Repeat("} ", 100000) + "\n", 1 << 30, budgetNesting},
		{"loop words over many sources", fanout.String(), 8 << 20, budgetMemory},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			f.file("etc/profile.d/x.sh", tc.profile)
			c := f.checker(testLists())
			c.t.b = newBudget(limits{entries: 1 << 40, bytes: 1 << 40, time: time.Hour, memory: tc.memory})
			mem, took := allocated(func() { within(t, c.CheckEnv) })
			want := Hit{Kind: KindIncomplete, Path: PathBudget, Detail: tc.detail}
			if r := c.Report(); len(r.Hits) == 0 || r.Hits[len(r.Hits)-1] != want || r.Complete {
				t.Errorf("hits %v", r.Hits)
			}
			if took > timeBound || mem > memoryBound {
				t.Errorf("took %v and allocated %d bytes", took, mem)
			}
		})
	}
}

// The startup scan's own expansion is charged too: a profile that gives
// XDG_CONFIG_HOME many literal folders and one that sources many files
// relative to it stand for the product of the two lists, which the scan
// streams, charging each path before it builds it, and stops building
// (both loops) as soon as a budget is spent, so the run ends with
// "incomplete budget" within a small time and memory bound.
func TestStartupExpansionIsBounded(t *testing.T) {
	const (
		dirs, sources = 2000, 2000
		timeBound     = 3 * time.Second
		memoryBound   = 48 << 20
	)
	var xdg, src, paths strings.Builder
	for i := 0; i < dirs; i++ {
		fmt.Fprintf(&xdg, "export XDG_CONFIG_HOME=/cfg/folder-%04d\n", i)
	}
	for i := 0; i < sources; i++ {
		fmt.Fprintf(&src, ". \"$XDG_CONFIG_HOME/source-%04d.sh\"\n", i)
		fmt.Fprintf(&paths, "export PATH=\"$XDG_CONFIG_HOME/bin-%04d:$PATH\"\n", i)
	}
	entries := limits{entries: 20000, bytes: 1 << 40, time: time.Hour, memory: 1 << 40}
	cases := []struct {
		name    string
		profile string
		check   func(c *Checker)
		lim     limits
		detail  string
	}{
		{"sources, entries", src.String(), (*Checker).CheckEnv, entries, budgetEntries},
		{"sources, memory", src.String(), (*Checker).CheckEnv, limits{entries: 1 << 40, bytes: 1 << 40, time: time.Hour, memory: 6 << 20}, budgetMemory},
		{"sources, time", src.String(), (*Checker).CheckEnv, limits{entries: 1 << 40, bytes: 1 << 40, time: 100 * time.Millisecond, memory: 1 << 40}, budgetTime},
		{"PATH folders, entries", paths.String(), (*Checker).CheckBinaries, entries, budgetEntries},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			f.file("home/user/.profile", xdg.String())
			f.file("home/user/.bashrc", tc.profile)
			c := f.checker(testLists())
			c.t.b = newBudget(tc.lim)
			mem, took := allocated(func() { within(t, func() { tc.check(c) }) })
			want := Hit{Kind: KindIncomplete, Path: PathBudget, Detail: tc.detail}
			if r := c.Report(); len(r.Hits) == 0 || r.Hits[len(r.Hits)-1] != want || r.Complete {
				t.Errorf("hits %v", r.Hits)
			}
			if took > timeBound || mem > memoryBound {
				t.Errorf("took %v and allocated %d bytes", took, mem)
			}
		})
	}
}

// A candidate release with very many admissible unit files, each with many
// EnvironmentFile= lines, does not hold every parse until the walk ends: what
// a unit file keeps (its file names) is charged before it is kept, the
// units are checked one at a time and each one's data is released after its
// references are checked, and the walk stops as soon as the memory budget is
// spent. The run ends "incomplete budget memory" within a small memory
// bound; with a budget that fits it, it finishes and holds nothing back.
func TestCandidateUnitsAreBounded(t *testing.T) {
	const (
		units, lines = 3000, 400
		memoryBound  = 64 << 20
		timeBound    = 10 * time.Second
	)
	plant := func(f *fixture) {
		for i := 0; i < units; i++ {
			var b strings.Builder
			b.WriteString("[Service]\n")
			for j := 0; j < lines; j++ {
				fmt.Fprintf(&b, "EnvironmentFile=-/etc/app/e-%04d-%03d\n", i, j)
			}
			f.file(fmt.Sprintf("%s/etc/systemd/u%04d.service", releaseDir, i), b.String())
		}
	}
	t.Run("memory budget spent", func(t *testing.T) {
		f := cleanFixture(t)
		plant(f)
		c := f.checker(testLists())
		c.t.b = newBudget(limits{entries: 1 << 40, bytes: 1 << 40, time: time.Hour, memory: 8 << 20})
		var err error
		mem, took := allocated(func() { within(t, func() { err = c.CheckRelease(f.abs(releaseDir)) }) })
		if err != nil {
			t.Fatal(err)
		}
		want := Hit{Kind: KindIncomplete, Path: PathBudget, Detail: budgetMemory}
		if r := c.Report(); len(r.Hits) == 0 || r.Hits[len(r.Hits)-1] != want || r.Complete {
			t.Errorf("hits %v", r.Hits)
		}
		if c.t.b.memory > c.t.b.lim.memory {
			t.Errorf("held %d bytes past the budget of %d", c.t.b.memory, c.t.b.lim.memory)
		}
		if took > timeBound || mem > memoryBound {
			t.Errorf("took %v and allocated %d bytes", took, mem)
		}
	})
	t.Run("budget that fits", func(t *testing.T) {
		f := cleanFixture(t)
		for i := 0; i < 40; i++ {
			var b strings.Builder
			b.WriteString("[Service]\n")
			for j := 0; j < lines; j++ {
				fmt.Fprintf(&b, "EnvironmentFile=-/etc/app/e-%04d-%03d\n", i, j)
			}
			f.file(fmt.Sprintf("%s/etc/systemd/u%04d.service", releaseDir, i), b.String())
		}
		c := f.checker(testLists())
		var err error
		within(t, func() { err = c.CheckRelease(f.abs(releaseDir)) })
		if r := c.Report(); err != nil || !r.Complete || len(r.Hits) != 0 {
			t.Errorf("err %v hits %v complete %v", err, r.Hits, r.Complete)
		}
		if c.t.b.spent != "" {
			t.Errorf("budget spent: %s", c.t.b.spent)
		}
	})
}

// Parsed alone, without a run's budget, a profile nested too deep is
// syntax the check cannot parse.
func TestShellParseNestingWithoutABudget(t *testing.T) {
	r := parseEnv(syntaxShell, "X="+strings.Repeat("$(", maxNesting+1)+strings.Repeat(")", maxNesting+1)+"\n", false)
	if !r.bad {
		t.Error("a profile nested too deep was parsed")
	}
	r = parseEnv(syntaxShell, "X="+strings.Repeat("$(", maxNesting-1)+strings.Repeat(")", maxNesting-1)+"\nOPENAI_API_KEY=1\n", false)
	if r.bad || len(r.names) != 1 {
		t.Errorf("a profile within the limit: %+v", r)
	}
}

// The firewall listing is normalised token by token: a long line costs
// the memory of the listing and its result, not of its tokens, and past
// maxNFTLineTokens the rest of a line is kept as it is.
func TestNormaliseNFTStreams(t *testing.T) {
	line := strings.Repeat("{ ", maxNFTLineTokens-8) + "counter packets 1 bytes 2 accept"
	var out []byte
	mem, took := allocated(func() { out = NormaliseNFT([]byte(line)) })
	if !strings.HasSuffix(string(out), "{ counter accept\n") {
		t.Errorf("normalised %q", out[max(0, len(out)-40):])
	}
	if mem > 12<<20 || took > 5*time.Second {
		t.Errorf("took %v and allocated %d bytes for a %d-byte line", took, mem, len(line))
	}
	line = strings.Repeat("{ ", maxNFTLineTokens) + "counter packets 1 bytes 2 accept"
	if out := NormaliseNFT([]byte(line)); !strings.HasSuffix(string(out), "{ counter packets 1 bytes 2 accept\n") {
		t.Errorf("past the bound %q", out[max(0, len(out)-40):])
	}
}
