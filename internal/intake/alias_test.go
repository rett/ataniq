package intake

import (
	"context"
	"errors"
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
