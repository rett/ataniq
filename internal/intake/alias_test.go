package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestResolveLink(t *testing.T) {
	for _, tc := range []struct {
		link, target string
		want         string
		problem      bool
	}{
		{".claude", "cfg", "cfg", false},
		{".claude", "cfg/", "cfg", false},
		{".claude", "./cfg//sub/../x", "cfg/x", false},
		{"a/.claude", "../cfg", "cfg", false},
		{"a/b/.claude", "../../cfg/x", "cfg/x", false},
		{"a/b/.claude", "c", "a/b/c", false},
		{"a/.claude", "..", "", false},
		{".claude", ".", "", false},
		{"a/.claude", "../b/..", "", false},
		{".claude", "..", "", true},
		{"a/.claude", "../../x", "", true},
		{".claude", "/etc", "", true},
		{".claude", "", "", true},
		{".claude", "\x00cfg", "", true},
		{".claude", "cfg\x00../..", "cfg", false},
	} {
		got, problem := resolveLink(tc.link, tc.target)
		if (problem != "") != tc.problem || got != tc.want {
			t.Errorf("resolveLink(%q, %q) = %q, %q; want %q, problem %v", tc.link, tc.target, got, problem, tc.want, tc.problem)
		}
	}
}

// aliasScene has the files cfg/settings.json, cfg/sub/x.json, cfgx/y.json and
// other/z.json in its base.
func aliasScene(t *testing.T) *scene {
	return newScene(t,
		"cfg/settings.json", "{}\n",
		"cfg/sub/x.json", "{}\n",
		"cfgx/y.json", "{}\n",
		"other/z.json", "{}\n",
	)
}

func changeAll(s *scene) {
	s.put("cfg/settings.json", `{"hooks":1}`+"\n")
	s.put("cfg/sub/x.json", `{"hooks":1}`+"\n")
	s.put("cfgx/y.json", `{"hooks":1}`+"\n")
	s.put("other/z.json", `{"hooks":1}`+"\n")
}

func TestAliasSymlinkAlreadyInBase(t *testing.T) {
	s := aliasScene(t)
	// The base already has the link; the lead only changes files below its
	// target.
	s.symlink(".claude", "cfg")
	base := s.commit("link in upstream\n")
	s.publish(upstreamRef, base)
	changeAll(s)
	s.commit("change files\n")
	s.finish()

	r := s.inspect()
	if len(r.Changes) != 4 {
		t.Fatalf("the unchanged link is listed: %+v", r.Changes)
	}
	for _, ch := range r.Changes {
		if ch.Path == ".claude" {
			t.Fatalf("the unchanged link is listed: %+v", r.Changes)
		}
	}
	for _, p := range []string{"cfg/settings.json", "cfg/sub/x.json"} {
		ch := byPath(t, r, p)
		f := flagFor(t, ch.Flags, FlagAliased, p)
		if f.Pattern == "" || !strings.Contains(f.Reason, ".claude") || !strings.Contains(f.Reason, "cfg") {
			t.Errorf("%s: flag %+v", p, f)
		}
		flagFor(t, r.Flagged, FlagAliased, p)
	}
	for _, p := range []string{"cfgx/y.json", "other/z.json"} {
		if ch := byPath(t, r, p); hasFlag(ch.Flags, FlagAliased) {
			t.Errorf("%s is flagged: %+v", p, ch.Flags)
		}
	}
}

func TestAliasSymlinkAddedByLead(t *testing.T) {
	s := aliasScene(t)
	changeAll(s)
	s.commit("change files\n")
	s.symlink(".claude", "cfg")
	s.commit("add the link last\n")
	s.finish()

	r := s.inspect()
	link := byPath(t, r, ".claude")
	if link.Status != 'A' || link.NewMode != 0o120000 || !hasFlag(link.Flags, FlagSymlink) {
		t.Errorf("link: %+v", link)
	}
	if hasFlag(link.Flags, FlagAliased) {
		t.Errorf("a link that resolves is flagged on itself: %+v", link.Flags)
	}
	for _, p := range []string{"cfg/settings.json", "cfg/sub/x.json"} {
		flagFor(t, byPath(t, r, p).Flags, FlagAliased, p)
	}
	if hasFlag(byPath(t, r, "cfgx/y.json").Flags, FlagAliased) || hasFlag(byPath(t, r, "other/z.json").Flags, FlagAliased) {
		t.Error("a path outside the target is flagged")
	}
}

func TestAliasVariants(t *testing.T) {
	// A link in a folder, a link by another case, and a link to a nested
	// folder; each with its own changed file.
	s := aliasScene(t)
	s.symlink("sub/.codex", "../CFG/sub")
	s.symlink("Makefile", "other")
	s.commit("links\n")
	s.publish(upstreamRef, s.cur)
	changeAll(s)
	s.commit("change files\n")
	s.finish()
	r := s.inspect()
	flagFor(t, byPath(t, r, "cfg/sub/x.json").Flags, FlagAliased, "cfg/sub/x.json")
	if hasFlag(byPath(t, r, "cfg/settings.json").Flags, FlagAliased) {
		t.Error("only the nested folder is the target")
	}
	// A link named like a file rule stands for a folder or a file.
	flagFor(t, byPath(t, r, "other/z.json").Flags, FlagAliased, "other/z.json")
}

func TestAliasLinkNotMatchingTheRulesIsNotFollowed(t *testing.T) {
	s := aliasScene(t)
	s.symlink("notes", "cfg")
	s.commit("an ordinary link\n")
	s.publish(upstreamRef, s.cur)
	changeAll(s)
	s.commit("change files\n")
	s.finish()
	r := s.inspect()
	for _, ch := range r.Changes {
		if hasFlag(ch.Flags, FlagAliased) {
			t.Errorf("%s is flagged: %+v", ch.Path, ch.Flags)
		}
	}
}

func TestAliasLinkToTheRoot(t *testing.T) {
	s := aliasScene(t)
	s.symlink(".claude", ".")
	s.commit("the link\n")
	s.publish(upstreamRef, s.cur)
	changeAll(s)
	s.commit("change files\n")
	s.finish()
	r := s.inspect()
	for _, p := range []string{"cfg/settings.json", "cfgx/y.json", "other/z.json"} {
		f := flagFor(t, byPath(t, r, p).Flags, FlagAliased, p)
		if !strings.Contains(f.Reason, "points at .") {
			t.Errorf("reason: %q", f.Reason)
		}
	}
}

func TestAliasLinkThatCannotBeFollowed(t *testing.T) {
	s := aliasScene(t)
	s.symlink(".claude", "/opt/agent/.claude")
	s.symlink("sub/.codex", "../../outside")
	s.symlink(".agents", strings.Repeat("a/", 3000))
	s.symlink("scripts", "")
	s.commit("links that lead out\n")
	s.publish(upstreamRef, s.cur)
	// A changed link flags itself; an unchanged one does not.
	s.symlink(".claude", "/opt/other/.claude")
	s.symlink("sub/.codex", "../../elsewhere")
	s.symlink(".agents", strings.Repeat("b/", 3000))
	s.put("src/ordinary.go", "package main\n")
	s.commit("retarget\n")
	s.finish()
	r := s.inspect()
	for p, want := range map[string]string{
		".claude":    "absolute",
		"sub/.codex": "leaves the repository",
		".agents":    "4096",
	} {
		ch := byPath(t, r, p)
		f := flagFor(t, ch.Flags, FlagAliased, p)
		if !strings.Contains(f.Reason, want) {
			t.Errorf("%s: reason %q lacks %q", p, f.Reason, want)
		}
		if !hasFlag(ch.Flags, FlagSymlink) {
			t.Errorf("%s: no symlink flag", p)
		}
		flagFor(t, r.Flagged, FlagAliased, p)
	}
	if len(r.Changes) != 4 {
		t.Errorf("changes: %+v", r.Changes)
	}
	for _, ch := range r.Changes {
		if ch.Path == "scripts" {
			t.Errorf("the unchanged link is listed")
		}
	}
	if o := byPath(t, r, "src/ordinary.go"); len(o.Flags) != 0 {
		t.Errorf("ordinary: %+v", o.Flags)
	}
}

func TestAliasNoLinkNoExtraRuns(t *testing.T) {
	// With no symlink in the tree the review is the same as without the
	// alias step: no aliased flag anywhere.
	s := aliasScene(t)
	changeAll(s)
	s.commit("change files\n")
	s.finish()
	r := s.inspect()
	for _, ch := range r.Changes {
		if hasFlag(ch.Flags, FlagAliased) {
			t.Errorf("%s: %+v", ch.Path, ch.Flags)
		}
	}
}

func TestAliasTreeOverTheLimit(t *testing.T) {
	s := aliasScene(t)
	s.put("cfg/settings.json", "{\"x\":1}\n")
	s.commit("one change\n")
	s.finish()
	req := s.request()
	req.Limits.MaxPaths = 3 // the tree has more entries than that
	_, err := Inspect(context.Background(), s.source(), req)
	if !errors.Is(err, ErrLimit) {
		t.Errorf("error = %v", err)
	}
}

// chainScene has cfg/settings.json and b/cfg/settings.json in its base, and
// the links the test gives, which are in the upstream branch too.
func chainScene(t *testing.T, links ...string) *scene {
	t.Helper()
	s := newScene(t, "cfg/settings.json", "{}\n", "b/cfg/settings.json", "{}\n")
	for i := 0; i+1 < len(links); i += 2 {
		s.symlink(links[i], links[i+1])
	}
	s.commit("links in upstream\n")
	s.publish(upstreamRef, s.cur)
	return s
}

func TestAliasChainOfSymlinks(t *testing.T) {
	for name, links := range map[string][]string{
		// The target passes through a link in the middle.
		"middle": {".claude", "a/cfg", "a", "b"},
		// The last element of the target is a link.
		"last": {".claude", "a", "a", "b/cfg"},
		// A link whose own folder is reached by a relative target.
		"nested": {"x/.claude", "../a", "a", "b/cfg"},
	} {
		t.Run(name, func(t *testing.T) {
			s := chainScene(t, links...)
			s.put("b/cfg/settings.json", `{"hooks":1}`+"\n")
			s.put("cfg/settings.json", `{"hooks":1}`+"\n")
			s.commit("change\n")
			s.finish()
			r := s.inspect()
			f := flagFor(t, byPath(t, r, "b/cfg/settings.json").Flags, FlagAliased, "b/cfg/settings.json")
			if !strings.Contains(f.Reason, ".claude") || !strings.Contains(f.Reason, "b/cfg") {
				t.Errorf("reason: %q", f.Reason)
			}
			if hasFlag(byPath(t, r, "cfg/settings.json").Flags, FlagAliased) {
				t.Error("a path outside the chain's target is flagged")
			}
		})
	}
}

func TestAliasChainLength(t *testing.T) {
	// .claude -> l1 -> l2 -> ... -> cfg. The link being resolved is the first
	// step, so seven links after it are eight steps and the ninth is too many.
	chain := func(n int) []string {
		links := []string{".claude", "l1"}
		for i := 1; i < n; i++ {
			links = append(links, fmt.Sprintf("l%d", i), fmt.Sprintf("l%d", i+1))
		}
		return append(links, fmt.Sprintf("l%d", n), "cfg")
	}
	for n, follows := range map[int]bool{6: true, 7: true, 8: false, 12: false} {
		s := chainScene(t)
		links := chain(n)
		for i := 0; i+1 < len(links); i += 2 {
			s.symlink(links[i], links[i+1])
		}
		s.put("cfg/settings.json", `{"hooks":1}`+"\n")
		s.commit("the chain and a change\n")
		s.finish()
		r := s.inspect()
		viaChain := hasFlag(byPath(t, r, "cfg/settings.json").Flags, FlagAliased)
		onLink := hasFlag(byPath(t, r, ".claude").Flags, FlagAliased)
		if viaChain != follows || onLink == follows {
			t.Errorf("chain of %d links after the first: path flagged %v, link flagged %v", n, viaChain, onLink)
		}
		if !follows {
			f := flagFor(t, byPath(t, r, ".claude").Flags, FlagAliased, ".claude")
			if !strings.Contains(f.Reason, "more than 8 symlinks") {
				t.Errorf("reason: %q", f.Reason)
			}
		}
	}
}

func TestAliasLoop(t *testing.T) {
	for name, links := range map[string][]string{
		"itself": {".claude", ".claude"},
		"pair":   {".claude", "a", "a", ".claude"},
		"deep":   {".claude", "a/x", "a", "a"},
	} {
		t.Run(name, func(t *testing.T) {
			s := chainScene(t)
			for i := 0; i+1 < len(links); i += 2 {
				s.symlink(links[i], links[i+1])
			}
			s.put("cfg/settings.json", `{"hooks":1}`+"\n")
			s.commit("a loop\n")
			s.finish()
			r := s.inspect()
			f := flagFor(t, byPath(t, r, ".claude").Flags, FlagAliased, ".claude")
			if !strings.Contains(f.Reason, "or loops") {
				t.Errorf("reason: %q", f.Reason)
			}
			if hasFlag(byPath(t, r, "cfg/settings.json").Flags, FlagAliased) {
				t.Error("a loop stands for a path")
			}
		})
	}
}

func TestAliasChainLinkThatCannotBeFollowed(t *testing.T) {
	for name, tc := range map[string]struct {
		links []string
		want  string
	}{
		"absolute":    {[]string{".claude", "a/cfg", "a", "/opt/elsewhere"}, "the symlink a on its way: its target is an absolute path"},
		"outside":     {[]string{".claude", "a/cfg", "a", ".."}, "its target leaves the repository"},
		"empty":       {[]string{".claude", "a", "a", ""}, "the symlink a on its way: its target is empty"},
		"too long":    {[]string{".claude", "a", "a", strings.Repeat("b/", 3000)}, "the symlink a on its way: its target cannot be read"},
		"first empty": {[]string{".claude", ""}, "its target is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			s := chainScene(t)
			for i := 0; i+1 < len(tc.links); i += 2 {
				s.symlink(tc.links[i], tc.links[i+1])
			}
			s.commit("links\n")
			s.finish()
			r := s.inspect()
			f := flagFor(t, byPath(t, r, ".claude").Flags, FlagAliased, ".claude")
			if !strings.Contains(f.Reason, tc.want) {
				t.Errorf("reason %q lacks %q", f.Reason, tc.want)
			}
		})
	}
}

// TestAliasChangedAndRevertedInTheBranch: a hook is put into a path that a
// symlink of the base stands for, and taken out again. The commit with the
// hook enters upstream history, so it is flagged history-only, with a reason
// that names the symlink.
func TestAliasChangedAndRevertedInTheBranch(t *testing.T) {
	s := aliasScene(t)
	s.symlink(".claude", "cfg")
	s.commit("link in upstream\n")
	s.publish(upstreamRef, s.cur)
	s.put("cfg/settings.json", `{"hooks":1}`+"\n")
	s.put("cfgx/y.json", `{"hooks":1}`+"\n")
	s.commit("hook\n")
	s.put("cfg/settings.json", "{}\n")
	s.put("cfgx/y.json", "{}\n")
	s.commit("revert\n")
	s.put("src/x.go", "package main\n// x\n")
	s.commit("other\n")
	s.finish()
	r := s.inspect()
	if len(r.Changes) != 1 || r.Changes[0].Path != "src/x.go" {
		t.Fatalf("changes: %+v", r.Changes)
	}
	if len(r.Flagged) != 1 {
		t.Fatalf("Flagged: %+v", r.Flagged)
	}
	f := flagFor(t, r.Flagged, FlagHistory, "cfg/settings.json")
	if !strings.Contains(f.Reason, ".claude") || !strings.Contains(f.Reason, "cfg") || f.Pattern == "" {
		t.Errorf("flag: %+v", f)
	}
}

// TestAliasChangedAndRevertedThroughAChain: the same, through a chain of
// links and for a folder that stands for the whole repository.
func TestAliasChangedAndRevertedThroughAChain(t *testing.T) {
	s := chainScene(t, ".claude", "a/cfg", "a", "b")
	s.put("b/cfg/settings.json", `{"hooks":1}`+"\n")
	s.commit("hook\n")
	s.put("b/cfg/settings.json", "{}\n")
	s.commit("revert\n")
	s.finish()
	r := s.inspect()
	if len(r.Changes) != 0 {
		t.Fatalf("changes: %+v", r.Changes)
	}
	flagFor(t, r.Flagged, FlagHistory, "b/cfg/settings.json")

	s = aliasScene(t)
	s.symlink(".claude", ".")
	s.commit("link to the root\n")
	s.publish(upstreamRef, s.cur)
	s.put("other/z.json", `{"hooks":1}`+"\n")
	s.commit("change\n")
	s.put("other/z.json", "{}\n")
	s.commit("revert\n")
	s.finish()
	flagFor(t, s.inspect().Flagged, FlagHistory, "other/z.json")
}

// TestAliasOfTheBaseThatTheBranchRemoves: the base has a link that stands for
// a folder. The branch puts a hook in the folder, takes it out again, and
// then removes or retargets the link. The commit with the hook enters
// history while the link still points at the folder, so the history pass
// flags it by the base's aliases too, though the tip has no such link.
func TestAliasOfTheBaseThatTheBranchRemoves(t *testing.T) {
	for name, end := range map[string]func(s *scene){
		"removed":    func(s *scene) { s.remove(".claude") },
		"retargeted": func(s *scene) { s.symlink(".claude", "other") },
		"kept":       func(s *scene) { s.put("src/x.go", "package main\n// x\n") },
	} {
		t.Run(name, func(t *testing.T) {
			s := aliasScene(t)
			s.symlink(".claude", "cfg")
			s.commit("link in upstream\n")
			s.publish(upstreamRef, s.cur)
			s.put("cfg/settings.json", `{"hooks":1}`+"\n")
			s.commit("hook\n")
			s.put("cfg/settings.json", "{}\n")
			s.commit("revert\n")
			end(s)
			s.commit("the link goes\n")
			s.finish()
			r := s.inspect()
			f := flagFor(t, r.Flagged, FlagHistory, "cfg/settings.json")
			if !strings.Contains(f.Reason, ".claude") || !strings.Contains(f.Reason, "cfg") || f.Pattern == "" {
				t.Errorf("flag: %+v", f)
			}
			// The link is in the base and, when kept, in the tip as well,
			// and it is not named twice.
			if n := strings.Count(f.Reason, "reached through the symlink"); n != 1 {
				t.Errorf("the symlink is named %d times: %s", n, f.Reason)
			}
		})
	}
}
