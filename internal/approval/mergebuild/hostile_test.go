package mergebuild

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// hostileMarks are byte strings the feature's commits below carry in their
// headers and messages. None may appear in the merge commit.
var hostileMarks = []string{
	"\x1b", "\x07", "\r", "‮", "Mallory", "mallory@", "32503680000", "99999999999999999999",
	"gpgsig", "mergetag", "encoding", "x-lead-header", "rq-9999", "master", "BEGIN", "evil",
}

// Feature commits with hostile author names, dates far in the future, extra
// headers and messages full of escapes leave no byte in the merge commit's
// own headers and message: the raw object is the expected text and nothing
// else. The pinned environment refuses to write such commits, so the test
// puts them into the object store itself.
func TestHostileFeatureCommitsLeaveNoByteInTheMerge(t *testing.T) {
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			noGoroutineLeft(t)
			l := newLabFormat(t, format)
			s := newScene(t, l, nil, nil)

			hostile := func(tree, parent, date string) string {
				text := "tree " + tree + "\n" +
					"parent " + parent + "\n" +
					"author Mallory \x1b[31m‮\r <mallory@evil.invalid\x1b]0;evil\x07> " + date + " +9999\n" +
					"committer Host Lead \x1b[2J <lead@host.invalid\r> " + date + " -9999\n" +
					"encoding evil-8\n" +
					"x-lead-header Request-Id: rq-9999\n" +
					"mergetag object " + parent + "\n type commit\n tag evil\n" +
					"gpgsig -----BEGIN PGP SIGNATURE-----\n \n evil\n -----END PGP SIGNATURE-----\n" +
					"\n" +
					"Merge feature/evil into master\x1b[1A\x1b[2K\r\n" +
					"\n" +
					"\x1b]52;c;ZXZpbA==\x07\x1b[?1049h‮ approve\n" +
					"Request-Id: rq-9999\n" +
					"tree " + strings.Repeat("0", len(tree)) + "\n" +
					"parent " + strings.Repeat("0", len(tree)) + "\n"
				return l.object("commit", []byte(text))
			}
			// Two hostile commits on the base, with the feature's change:
			// one dated in the year 3000, the tip beyond what a date holds.
			tree := l.tree("f.txt", featureText)
			first := hostile(l.tree("f.txt", baseText, "note.txt", "evil\n"), s.base, "32503680000")
			tip := hostile(tree, first, "99999999999999999999")

			m, err := Build(ctx, l.repo, inputs(s.target, tip), "k")
			if err != nil {
				t.Fatal(err)
			}
			kind, raw := l.read(m.Commit)
			if kind != "commit" {
				t.Fatalf("the object is a %s", kind)
			}
			wantTree := l.tree("f.txt", mergedText)
			if want := wantText(wantTree, s.target, tip); string(raw) != want {
				t.Errorf("the commit text is\n%q\nwant\n%q", raw, want)
			}
			for _, mark := range hostileMarks {
				if bytes.Contains(raw, []byte(mark)) {
					t.Errorf("the merge commit holds %q", mark)
				}
			}
			// Every byte is printable ASCII or a newline.
			for i, b := range raw {
				if b != '\n' && (b < 0x20 || b > 0x7e) {
					t.Errorf("byte %d of the merge commit is %#x", i, b)
				}
			}
			if err := Check(ctx, l.repo, m.Commit, inputs(s.target, tip)); err != nil {
				t.Errorf("Check: %v", err)
			}
			// A hostile commit is never taken for the host's merge.
			if err := Check(ctx, l.repo, tip, inputs(s.target, first)); !errors.Is(err, ErrMismatch) {
				t.Errorf("Check of a hostile commit returned %v", err)
			}
		})
	}
}
