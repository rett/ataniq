package all_test

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"unicode"

	"ataniq/internal/pack"
	"ataniq/packs/all"
)

func boundaryData(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/boundary/" + file)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func boundaryWords(t *testing.T) []pack.Word {
	t.Helper()
	r := pack.New()
	if err := all.Register(r); err != nil {
		t.Fatal(err)
	}
	r.Freeze()
	var words []pack.Word
	for _, name := range r.Names() {
		p, _ := r.Lookup(name)
		m := p.Manifest()
		// Use the declaration's word-mode, minimum-length and NoWordsReason
		// checks even for a pack that assembles its manifest in Go.
		if err := (pack.Declaration{Manifest: m}).Validate(pack.BindLayout{}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		words = append(words, m.Words...)
	}
	for _, model := range strings.Fields(boundaryData(t, "model-words.txt")) {
		words = append(words, pack.Word{Text: model, Match: "model"})
	}
	return words
}

type wordHit struct{ file, word string }

// allow.txt freezes individual file/word pairs, so another file in a legacy
// package or another pack's word in a legacy file still fails. The D36 entries
// last until the authority-host release migrates the detector to release data.
func boundaryAllow(t *testing.T) map[wordHit]bool {
	t.Helper()
	allow := map[wordHit]bool{}
	for i, line := range strings.Split(boundaryData(t, "allow.txt"), "\n") {
		entry, reason, _ := strings.Cut(line, "#")
		f := strings.Fields(entry)
		if len(f) == 0 {
			continue
		}
		if len(f) < 2 || !fs.ValidPath(f[0]) || !strings.HasSuffix(f[0], ".go") ||
			!wordMigrationReason(reason) {
			t.Fatalf("allow.txt:%d: want file, words and migration reason", i+1)
		}
		for _, word := range f[1:] {
			hit := wordHit{f[0], word}
			if allow[hit] {
				t.Fatalf("allow.txt:%d: duplicate entry for %s %s", i+1, f[0], word)
			}
			allow[hit] = true
		}
	}
	return allow
}

// D36 retires with the release that ships detector prefixes. Each file
// records the edit that removes its words in that release.
func wordMigrationReason(reason string) bool {
	task, ok := strings.CutPrefix(reason, " Migration: ")
	if strings.HasPrefix(task, "D36") {
		task, ok = strings.CutPrefix(task, "D36 authority-host prefix-list release: ")
	}
	return ok && strings.TrimSpace(task) != ""
}

func TestBoundaryWordsD36Retirement(t *testing.T) {
	for _, reason := range []string{" Migration: D36 checker examples", " Migration: D36 authority-host prefix-list release: "} {
		if wordMigrationReason(reason) {
			t.Errorf("accepted D36 without its release and file retirement condition: %q", reason)
		}
	}
}

// A token is a run of letters and digits, split at a lower-to-upper case
// transition. Keep the original case until those boundaries are known.
// Model names have numeric versions, so a digit also ends a model token.
func wordTokens(src string, includeDigits bool) map[string]bool {
	tokens := map[string]bool{}
	var prev rune
	start := -1
	for i, r := range src {
		word := unicode.IsLetter(r) || (includeDigits && unicode.IsDigit(r))
		if start >= 0 && (!word || (unicode.IsLower(prev) && unicode.IsUpper(r))) {
			tokens[strings.ToLower(src[start:i])] = true
			start = -1
		}
		if word && start < 0 {
			start = i
		}
		prev = r
	}
	if start >= 0 {
		tokens[strings.ToLower(src[start:])] = true
	}
	return tokens
}

func findBoundaryWords(repo fs.FS, words []pack.Word, allow map[wordHit]bool) ([]wordHit, error) {
	unused := maps.Clone(allow)
	var hits []wordHit
	err := fs.WalkDir(repo, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch name {
			case ".git", ".claude/worktrees", "build", "packs", "third_party":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		b, err := fs.ReadFile(repo, name)
		if err != nil {
			return err
		}
		src := string(b)
		lower, tokens := strings.ToLower(src), wordTokens(src, true)
		models := wordTokens(src, false)
		for _, w := range words {
			var match bool
			switch w.Match {
			case "substring":
				match = strings.Contains(lower, strings.ToLower(w.Text))
			case "literal":
				match = strings.Contains(src, w.Text)
			case "token":
				match = tokens[strings.ToLower(w.Text)]
			case "model":
				match = models[strings.ToLower(w.Text)]
			default:
				return fmt.Errorf("unknown match mode %q for %q", w.Match, w.Text)
			}
			hit := wordHit{name, w.Text}
			if match {
				if allow[hit] {
					delete(unused, hit)
					continue
				}
				hits = append(hits, hit)
			}
		}
		return nil
	})
	if err == nil && len(unused) > 0 {
		var entries []string
		for hit := range unused {
			entries = append(entries, hit.file+" "+hit.word)
		}
		slices.Sort(entries)
		err = fmt.Errorf("unused allow.txt entries: %s", strings.Join(entries, ", "))
	}
	return hits, err
}

func TestBoundaryWords(t *testing.T) {
	hits, err := findBoundaryWords(os.DirFS("../.."), boundaryWords(t), boundaryAllow(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Errorf("%s: forbidden word %q", h.file, h.word)
	}
}

func TestBoundaryWordsOutsidePacks(t *testing.T) {
	words := []pack.Word{{Text: "codex", Match: "substring"}, {Text: "amp", Match: "token"}, {Text: ".cursor/", Match: "literal"}}
	repo := fstest.MapFS{
		"core/old.go":       {Data: []byte("// CODEX and .cursor/")},
		"core/old_extra.go": {Data: []byte("package core // myCoDeXClient")},
		"core/view_test.go": {Data: []byte("const myAmp = 1 // example, amp2, .Cursor/")},
		"core/clean.go":     {Data: []byte("// sample amplitude lamp2 .Cursor/")},
		"packs/new/pack.go": {Data: []byte("// codex")},
		"third_party/x.go":  {Data: []byte("// codex")},
	}
	hits, err := findBoundaryWords(repo, words, map[wordHit]bool{{"core/old.go", "codex"}: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []wordHit{{"core/old.go", ".cursor/"}, {"core/old_extra.go", "codex"}, {"core/view_test.go", "amp"}}
	if !slices.Equal(hits, want) {
		t.Fatalf("violations = %v, want %v", hits, want)
	}
}

func TestBoundaryWordsUnusedAllow(t *testing.T) {
	repo := fstest.MapFS{"core/live.go": {Data: []byte("package core // amp")}}
	allow := map[wordHit]bool{{"core/live.go", "amp"}: true, {"core/live.go", "codex"}: true, {"core/gone.go", "amp"}: true}
	words := []pack.Word{{Text: "amp", Match: "token"}, {Text: "codex", Match: "substring"}}
	_, err := findBoundaryWords(repo, words, allow)
	if err == nil || err.Error() != "unused allow.txt entries: core/gone.go amp, core/live.go codex" {
		t.Fatalf("stale allowances: %v", err)
	}
}

func TestBoundaryWordsModelVersions(t *testing.T) {
	words := []pack.Word{{Text: "gpt", Match: "model"}, {Text: "fable", Match: "model"}, {Text: "amp", Match: "token"}}
	repo := fstest.MapFS{
		"a.go": {Data: []byte("gpt5")},
		"b.go": {Data: []byte("GPT4o")},
		"c.go": {Data: []byte("myFable5Client")},
		"d.go": {Data: []byte("amp2 lamp2 mygpt5")},
	}
	got, err := findBoundaryWords(repo, words, nil)
	want := []wordHit{{"a.go", "gpt"}, {"b.go", "gpt"}, {"c.go", "fable"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("model version hits = %v, %v; want %v", got, err, want)
	}
}
