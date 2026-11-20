package ownerwire

import (
	"bytes"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

// checkPatchPages holds what must be true of every split patch: each page
// is within the cap as encoded and decodes to itself; the pages say whose
// they are and stand in their places; no piece that continues is empty; no
// page is without a piece, except the one page of an empty patch; no line
// is in more than maxPieces pieces. It returns the pages as a reader gets
// them: encoded and decoded again.
func checkPatchPages(t testing.TB, pages []PatchPage, limit, maxPieces int) []PatchPage {
	t.Helper()
	if len(pages) == 0 {
		t.Fatal("a split gave no page")
	}
	read := make([]PatchPage, len(pages))
	pieces := 0
	for i := range pages {
		p := pages[i]
		if p.ID != sampleID || p.ReviewDigest != sampleDigest || p.Page != i || p.Pages != len(pages) {
			t.Fatalf("page %d of %d says it is page %d of %d of %s", i, len(pages), p.Page, p.Pages, p.ID)
		}
		out, err := encode(&p, limit, record.CodeRecordInvalid, "patch")
		if err != nil {
			t.Fatalf("page %d of %d is not within %d bytes as encoded: %v", i, len(pages), limit, err)
		}
		if limit <= MaxPageBytes {
			if read[i], err = DecodePatchPage(out); err != nil {
				t.Fatalf("page %d does not decode: %v", i, err)
			}
			if again, err := EncodePatchPage(read[i]); err != nil || !bytes.Equal(again, out) {
				t.Fatalf("page %d does not encode to itself (%v)", i, err)
			}
		}
		if len(p.Lines) == 0 && len(pages) != 1 {
			t.Fatalf("page %d of %d has no piece", i, len(pages))
		}
		for j, l := range p.Lines {
			pieces++
			if pieces > maxPieces {
				t.Fatalf("a line is in more than %d pieces at page %d", maxPieces, i)
			}
			if l.Cont && l.Text == "" {
				t.Fatalf("page %d, piece %d is empty and continues", i, j)
			}
			if !l.Cont {
				pieces = 0
			}
		}
	}
	if pieces != 0 {
		t.Fatal("the last piece of the last page continues")
	}
	return read
}

func sameLines(t testing.TB, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d lines came back, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d came back changed (%d bytes for %d)", i, len(got[i]), len(want[i]))
		}
	}
}

// hostileLines is the hostile set as lines of a patch.
func hostileLines() []string {
	return []string{
		"\x1b[2J\x1b[31mred\x1b[0m",
		"\x1b]0;title\x07 and \x1b]8;;http://example.invalid\x1b\\link\x1b]8;;\x1b\\",
		"\x1bP1$r0m\x1b\\",
		"\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0b\x0c\x0d\x0e\x0f\x7f",
		"\xc2\x80\xc2\x9b31m\xc2\x9f",
		"\x9b31m raw C1",
		"‮evil‬ ​‍⁦x⁩",
		"\xff\xfe\xfd not UTF-8 \xc3",
		"\xed\xa0\x80 a surrogate half, \xf4\x90\x80\x80 past the last code point, \xc0\xaf overlong",
		"[a] approve   [x] deny",
		"+----------+\n| APPROVED |\n+----------+",
		"ap-20261120-0007  project-a  feature finish  code ACDE",
		`{"b64":"/w=="}`,
		`"quotes" and \backslashes\ and "more"`,
		"",
		" ",
		"\r",
		"line\rwith\ncontrol\tbytes",
		"plain text with é, ü and 日本語 and \U0001f600",
	}
}

// longLines are single lines of 1 MiB, each hard for the splitter in its
// own way.
func longLines() map[string]string {
	const mib = 1 << 20
	r := rand.New(rand.NewSource(7))
	random := make([]byte, mib)
	r.Read(random)
	mixed := make([]byte, 0, mib)
	for len(mixed) < mib-8 {
		switch r.Intn(6) {
		case 0:
			mixed = append(mixed, "é日\U0001f600"...)
		case 1:
			mixed = append(mixed, '"', '\\')
		case 2:
			mixed = append(mixed, 0xff)
		case 3:
			mixed = append(mixed, 0x1b, '[', '2', 'J')
		default:
			mixed = append(mixed, "plain "...)
		}
	}
	return map[string]string{
		"letters":                  strings.Repeat("a", mib),
		"quotes":                   strings.Repeat(`"`, mib),
		"backslashes":              strings.Repeat(`\`, mib),
		"bytes that are not UTF-8": strings.Repeat("\xff", mib),
		"two-byte characters":      strings.Repeat("é", mib/2),
		"three-byte characters":    "x" + strings.Repeat("日", mib/3),
		"four-byte characters":     "xy" + strings.Repeat("\U0001f600", mib/4),
		"escape sequences":         strings.Repeat("\x1b[31m", mib/5),
		"control characters":       strings.Repeat("\x00", mib),
		"random bytes":             string(random),
		"a mix":                    string(mixed),
		"text and one bad byte":    strings.Repeat("a", mib/2) + "\xff" + strings.Repeat("a", mib/2),
		"quotes then letters":      strings.Repeat(`"`, 200000) + strings.Repeat("a", mib-200000),
	}
}

// The property of the split: joined again, a patch is what it was, byte
// for byte, and every page is under the cap, for hostile inputs.
func TestSplitPatchThenJoinGivesThePatchBack(t *testing.T) {
	cases := map[string][]string{
		"an empty patch":              nil,
		"one empty line":              {""},
		"three empty lines":           {"", "", ""},
		"one short line":              {"+a"},
		"the hostile set":             hostileLines(),
		"the hostile set, many times": repeatLines(hostileLines(), 3000),
		"many short lines":            repeatLines([]string{"+ a line of a patch", "- another", " context"}, 20000),
		"lines of half a page":        repeatLines([]string{strings.Repeat("h", MaxPageBytes/2)}, 7),
		"lines of just under a page":  repeatLines([]string{strings.Repeat("p", MaxPageBytes-400)}, 3),
		"lines of exactly a page":     repeatLines([]string{strings.Repeat("p", MaxPageBytes)}, 3),
	}
	for name, line := range longLines() {
		cases["a line of 1 MiB: "+name] = []string{line}
		cases["a line of 1 MiB among others: "+name] = append(append(hostileLines(), line), hostileLines()...)
		cases["a line of 1 MiB twice: "+name] = []string{line, line}
	}
	for name, lines := range cases {
		pages, err := SplitPatch(sampleID, sampleDigest, lines)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		read := checkPatchPages(t, pages, MaxPageBytes, MaxLinePieces)
		got, err := JoinPatch(read)
		if err != nil {
			t.Errorf("%s: the pages do not join: %v", name, err)
			continue
		}
		sameLines(t, got, lines)
	}
}

func repeatLines(lines []string, times int) []string {
	out := make([]string, 0, len(lines)*times)
	for i := 0; i < times; i++ {
		out = append(out, lines...)
	}
	return out
}

// The same property under small caps and random lines, where every edge of
// a page is met often: a cut inside a character, a piece that just fits, a
// page with room for nothing more.
func TestSplitPatchThenJoinUnderSmallCaps(t *testing.T) {
	alphabets := []string{
		"a",
		`"\`,
		"aé日\U0001f600",
		"a\xff",
		"\x1b[31m\x00\x7f",
		`a"é` + "\xff\x1b",
		"\xc3\xa9\xc3", // a character and half a character
	}
	r := rand.New(rand.NewSource(20261118))
	frame := pageFrame("lines", sampleID, sampleDigest)
	for round := 0; round < 3000; round++ {
		limit := frame + pieceFrame + 14 + r.Intn(900)
		alphabet := alphabets[r.Intn(len(alphabets))]
		lines := make([]string, r.Intn(12))
		for i := range lines {
			n := r.Intn(700)
			if r.Intn(4) == 0 {
				n = r.Intn(8)
			}
			b := make([]byte, n)
			for j := range b {
				b[j] = alphabet[r.Intn(len(alphabet))]
			}
			lines[i] = string(b)
		}
		pages, err := splitPatch(sampleID, sampleDigest, lines, limit, MaxLinePieces)
		if err != nil {
			t.Fatalf("round %d, a cap of %d: %v", round, limit, err)
		}
		read := checkPatchPages(t, pages, limit, MaxLinePieces)
		got, err := JoinPatch(read)
		if err != nil {
			t.Fatalf("round %d, a cap of %d: the pages do not join: %v", round, limit, err)
		}
		sameLines(t, got, lines)
	}
}

// A page of a long line is filled: the splitter does not waste the cap.
func TestALongLineFillsItsPages(t *testing.T) {
	const mib = 1 << 20
	for name, c := range map[string]struct {
		line  string
		pages int
	}{
		// 1 MiB as a string is 1 MiB and a few bytes: five pages.
		"letters": {strings.Repeat("a", mib), 5},
		// As b64 it is four thirds of that: six pages.
		"bytes that are not UTF-8": {strings.Repeat("\xff", mib), 6},
		// Every quote is two bytes: nine pages.
		"quotes": {strings.Repeat(`"`, mib), 9},
	} {
		pages, err := SplitPatch(sampleID, sampleDigest, []string{c.line})
		if err != nil {
			t.Fatal(err)
		}
		if len(pages) != c.pages {
			t.Errorf("%s: 1 MiB is carried in %d pages, want %d", name, len(pages), c.pages)
		}
	}
}

// No line is ever in more pieces than the pager reads: the splitter
// refuses a line that would need more, and gives no page of it.
func TestSplitPatchRefusesALineInTooManyPieces(t *testing.T) {
	frame := pageFrame("lines", sampleID, sampleDigest)
	limit := frame + pieceFrame + 102 // a page holds one piece of 100 letters
	line := func(n int) []string { return []string{strings.Repeat("a", n)} }

	pages, err := splitPatch(sampleID, sampleDigest, line(300), limit, 3)
	if err != nil {
		t.Fatalf("a line of three pieces is refused: %v", err)
	}
	checkPatchPages(t, pages, limit, 3)
	if len(pages) != 3 {
		t.Fatalf("300 letters are in %d pages, want 3", len(pages))
	}

	pages, err = splitPatch(sampleID, sampleDigest, line(301), limit, 3)
	refused(t, err, record.CodeRecordTooLarge, "a line of four pieces")
	if pages != nil {
		t.Fatal("a refused split gave pages")
	}
	// The refusal does not depend on where the line stands.
	pages, err = splitPatch(sampleID, sampleDigest, append([]string{"short", ""}, line(1000)...), limit, 3)
	refused(t, err, record.CodeRecordTooLarge, "a long line after short ones")
	if pages != nil {
		t.Fatal("a refused split gave pages")
	}
	// A line of three pieces that starts after a line that leaves a sliver
	// of a page is still three pieces: the sliver is skipped, not counted
	// as a piece, and not used for a piece of the line.
	pages, err = splitPatch(sampleID, sampleDigest, []string{strings.Repeat("a", 70), strings.Repeat("a", 300)}, limit, 3)
	if err != nil {
		t.Fatalf("a line of three pieces after a short one is refused: %v", err)
	}
	checkPatchPages(t, pages, limit, 3)
	// A page with no room for one byte of a line is refused, not looped
	// on.
	_, err = splitPatch(sampleID, sampleDigest, line(50), frame+pieceFrame+1, MaxLinePieces)
	refused(t, err, record.CodeRecordTooLarge, "a cap with no room for a piece")
}

// Under the real cap the longest line the pager reads, at its worst for
// the splitter, needs far fewer pieces than the most; a longer line is
// refused.
func TestTheLongestLineIsWithinTheMostPieces(t *testing.T) {
	if testing.Short() {
		t.Skip("splits 16 MiB")
	}
	worst := strings.Repeat(`"`, MaxLineBytes)
	pages, err := SplitPatch(sampleID, sampleDigest, []string{"first", worst})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) > MaxLinePieces/8 {
		t.Fatalf("the longest line is in %d pages", len(pages))
	}
	for i := range pages {
		if len(pages[i].Lines) == 0 {
			t.Fatalf("page %d has no piece", i)
		}
	}
	got, err := JoinPatch(pages)
	if err != nil {
		t.Fatal(err)
	}
	sameLines(t, got, []string{"first", worst})

	_, err = SplitPatch(sampleID, sampleDigest, []string{worst + `"`})
	refused(t, err, record.CodeRecordTooLarge, "a line one byte longer than the longest")
}

func TestSplitChecksWhoseThePagesAre(t *testing.T) {
	for name, c := range map[string][2]string{
		"an id outside the grammar": {"ap-../x", sampleDigest},
		"no id":                     {"", sampleDigest},
		"a short digest":            {sampleID, "abc"},
		"a digest in capitals":      {sampleID, strings.ToUpper(sampleDigest)},
	} {
		_, err := SplitPatch(c[0], c[1], []string{"x"})
		refused(t, err, record.CodeRecordInvalid, "a patch for "+name)
		_, err = SplitPaths(c[0], c[1], nil)
		refused(t, err, record.CodeRecordInvalid, "paths for "+name)
	}
}

func samplePaths(n int) []record.PathChange {
	hostile := hostileLines()
	paths := make([]record.PathChange, n)
	for i := range paths {
		name := "dir/sub/file-" + strconv.Itoa(i) + ".go"
		if i%7 == 3 {
			name = hostile[i%len(hostile)] + strconv.Itoa(i)
		}
		paths[i] = record.PathChange{Path: record.Text(name), Status: "AMDT"[i%4 : i%4+1],
			OldMode: "100644", NewMode: "100755", Added: int64(i), Removed: int64(i % 5), NewBytes: int64(i) * 31}
	}
	return paths
}

func checkPathsPages(t testing.TB, pages []PathsPage, limit int) []PathsPage {
	t.Helper()
	if len(pages) == 0 {
		t.Fatal("a split gave no page")
	}
	read := make([]PathsPage, len(pages))
	for i := range pages {
		p := pages[i]
		if p.ID != sampleID || p.ReviewDigest != sampleDigest || p.Page != i || p.Pages != len(pages) {
			t.Fatalf("page %d of %d says it is page %d of %d of %s", i, len(pages), p.Page, p.Pages, p.ID)
		}
		if len(p.Paths) == 0 && len(pages) != 1 {
			t.Fatalf("page %d of %d has no path", i, len(pages))
		}
		out, err := encode(&p, limit, record.CodeRecordInvalid, "paths")
		if err != nil {
			t.Fatalf("page %d of %d is not within %d bytes as encoded: %v", i, len(pages), limit, err)
		}
		if read[i], err = DecodePathsPage(out); err != nil {
			t.Fatalf("page %d does not decode: %v", i, err)
		}
	}
	return read
}

func TestSplitPathsThenJoinGivesTheListBack(t *testing.T) {
	big := samplePaths(3)
	big[1].Path = record.Text(strings.Repeat("\xff", 100000))
	for name, paths := range map[string][]record.PathChange{
		"an empty list":          nil,
		"one path":               samplePaths(1),
		"5,000 paths":            samplePaths(5000),
		"40,000 paths":           samplePaths(40000),
		"paths of 100,000 bytes": append(append(big, big...), big...),
	} {
		pages, err := SplitPaths(sampleID, sampleDigest, paths)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		read := checkPathsPages(t, pages, MaxPageBytes)
		got, err := JoinPaths(read)
		if err != nil {
			t.Errorf("%s: the pages do not join: %v", name, err)
			continue
		}
		if len(got) != len(paths) || (len(paths) > 0 && !reflect.DeepEqual(got, paths)) {
			t.Errorf("%s: the list came back changed", name)
		}
		if name == "5,000 paths" && len(pages) < 2 {
			t.Errorf("%s are on %d page", name, len(pages))
		}
	}

	// Under small caps, with a page that holds one path or a few.
	r := rand.New(rand.NewSource(3))
	frame := pageFrame("paths", sampleID, sampleDigest)
	for round := 0; round < 300; round++ {
		paths := samplePaths(r.Intn(40))
		limit := frame + 200 + r.Intn(600)
		pages, err := splitPaths(sampleID, sampleDigest, paths, limit)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		read := checkPathsPages(t, pages, limit)
		got, err := JoinPaths(read)
		if err != nil || len(got) != len(paths) || (len(paths) > 0 && !reflect.DeepEqual(got, paths)) {
			t.Fatalf("round %d: the list came back changed (%v)", round, err)
		}
	}
}

func TestSplitPathsRefusesWhatCannotBePaged(t *testing.T) {
	huge := samplePaths(2)
	huge[1].Path = record.Text(strings.Repeat("p", MaxPageBytes))
	pages, err := SplitPaths(sampleID, sampleDigest, huge)
	refused(t, err, record.CodeRecordTooLarge, "a path of a whole page")
	if pages != nil {
		t.Fatal("a refused split gave pages")
	}
	bad := samplePaths(2)
	bad[0].Status = "R"
	_, err = SplitPaths(sampleID, sampleDigest, bad)
	refused(t, err, record.CodeRecordInvalid, "a path with a status outside the list")
	bad = samplePaths(2)
	bad[1].OldMode = "\x1b[2J"
	_, err = SplitPaths(sampleID, sampleDigest, bad)
	refused(t, err, record.CodeRecordInvalid, "a path with a mode that holds an escape")
}

func TestJoinRefusesPagesThatAreNotOnePatch(t *testing.T) {
	lines := repeatLines([]string{strings.Repeat("a", 100000), "short"}, 12)
	split := func() []PatchPage {
		pages, err := SplitPatch(sampleID, sampleDigest, lines)
		if err != nil {
			t.Fatal(err)
		}
		if len(pages) < 4 {
			t.Fatalf("the test's patch is on %d pages", len(pages))
		}
		return pages
	}
	other, err := SplitPatch(sampleID, otherDigest, lines)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := SplitPatch("ap-another", sampleDigest, lines)
	if err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		edit func([]PatchPage) []PatchPage
		code record.Code
	}{
		"no page":                   {func(p []PatchPage) []PatchPage { return nil }, record.CodeRecordInvalid},
		"the last page lost":        {func(p []PatchPage) []PatchPage { return p[:len(p)-1] }, record.CodeRecordInvalid},
		"the first page lost":       {func(p []PatchPage) []PatchPage { return p[1:] }, record.CodeRecordInvalid},
		"a page lost in the middle": {func(p []PatchPage) []PatchPage { return append(p[:1:1], p[2:]...) }, record.CodeRecordInvalid},
		"two pages swapped":         {func(p []PatchPage) []PatchPage { p[1], p[2] = p[2], p[1]; return p }, record.CodeRecordInvalid},
		"a page twice":              {func(p []PatchPage) []PatchPage { p[2] = p[1]; return p }, record.CodeRecordInvalid},
		"a count of pages that changes": {func(p []PatchPage) []PatchPage {
			p[len(p)-1].Pages++
			return p
		}, record.CodeRecordInvalid},
		"a page of another review": {func(p []PatchPage) []PatchPage { p[2] = other[2]; return p }, record.CodeReviewChanged},
		"a page of another id":     {func(p []PatchPage) []PatchPage { p[1] = otherID[1]; return p }, record.CodeReviewChanged},
		"a last page of another review": {func(p []PatchPage) []PatchPage {
			p[len(p)-1] = other[len(p)-1]
			return p
		}, record.CodeReviewChanged},
		"an empty piece that continues": {func(p []PatchPage) []PatchPage {
			p[1].Lines = append([]PatchLine{{Text: "", Cont: true}}, p[1].Lines...)
			return p
		}, record.CodeRecordInvalid},
		"a page without a piece": {func(p []PatchPage) []PatchPage { p[1].Lines = nil; return p }, record.CodeRecordInvalid},
		"a last piece that continues": {func(p []PatchPage) []PatchPage {
			last := &p[len(p)-1]
			last.Lines = append(last.Lines[:len(last.Lines):len(last.Lines)], PatchLine{Text: "x", Cont: true})
			return p
		}, record.CodeRecordInvalid},
	} {
		got, err := JoinPatch(c.edit(split()))
		refused(t, err, c.code, name)
		if got != nil {
			t.Errorf("%s: a refused join gave lines", name)
		}
	}

	// The same for the path list.
	paths, err := SplitPaths(sampleID, sampleDigest, samplePaths(20000))
	if err != nil || len(paths) < 3 {
		t.Fatalf("the test's list is on %d pages (%v)", len(paths), err)
	}
	otherPaths, err := SplitPaths(sampleID, otherDigest, samplePaths(20000))
	if err != nil {
		t.Fatal(err)
	}
	_, err = JoinPaths(nil)
	refused(t, err, record.CodeRecordInvalid, "paths: no page")
	_, err = JoinPaths(paths[:len(paths)-1])
	refused(t, err, record.CodeRecordInvalid, "paths: the last page lost")
	_, err = JoinPaths(append(paths[:1:1], otherPaths[1:]...))
	refused(t, err, record.CodeReviewChanged, "paths: pages of another review")
	_, err = JoinPaths(append(paths[1:2:2], paths...))
	refused(t, err, record.CodeRecordInvalid, "paths: a page out of its place")
}

// The reader holds the pager's two bounds on a line as well, whoever made
// the pages.
func TestJoinRefusesALineThePagerWouldNotRead(t *testing.T) {
	// 4,097 pieces of one line, on pages that are each well formed.
	var pages []PatchPage
	for left := MaxLinePieces + 1; left > 0; {
		p := PatchPage{ID: sampleID, ReviewDigest: sampleDigest}
		for n := 0; n < 1500 && left > 0; n++ {
			left--
			p.Lines = append(p.Lines, PatchLine{Text: "x", Cont: left > 0})
		}
		pages = append(pages, p)
	}
	for i := range pages {
		pages[i].Page, pages[i].Pages = i, len(pages)
	}
	_, err := JoinPatch(pages)
	refused(t, err, record.CodeRecordInvalid, "a line in 4,097 pieces")

	// One piece fewer is the most, and is read.
	pages[0].Lines = pages[0].Lines[1:]
	got, err := JoinPatch(pages)
	if err != nil || len(got) != 1 || len(got[0]) != MaxLinePieces {
		t.Fatalf("a line in %d pieces is not read (%v)", MaxLinePieces, err)
	}

	// A line of more than 16 MiB, in pieces that are each allowed.
	piece := record.Text(strings.Repeat("a", 1<<20))
	pages = nil
	for i := 0; i < 17; i++ {
		pages = append(pages, PatchPage{ID: sampleID, ReviewDigest: sampleDigest, Page: i, Pages: 17,
			Lines: []PatchLine{{Text: piece, Cont: i < 16}}})
	}
	_, err = JoinPatch(pages)
	refused(t, err, record.CodeRecordInvalid, "a line of 17 MiB")
	got, err = JoinPatch(func() []PatchPage {
		p := append([]PatchPage(nil), pages[:16]...)
		for i := range p {
			p[i].Pages = 16
		}
		p[15].Lines = []PatchLine{{Text: piece}}
		return p
	}())
	if err != nil || len(got) != 1 || len(got[0]) != MaxLineBytes {
		t.Fatalf("a line of 16 MiB is not read (%v)", err)
	}
}

// A reader checks each page against what it asked for before it shows it.
func TestAPageAnswersWhatWasAsked(t *testing.T) {
	asked := PageParams{ID: sampleID, ReviewDigest: sampleDigest, Page: 1}
	patch := PatchPage{ID: sampleID, ReviewDigest: sampleDigest, Page: 1, Pages: 3, Lines: []PatchLine{{Text: "x"}}}
	paths := PathsPage{ID: sampleID, ReviewDigest: sampleDigest, Page: 1, Pages: 3, Paths: samplePaths(1)}
	if err := patch.Answers(asked); err != nil {
		t.Errorf("the page asked for is refused: %v", err)
	}
	if err := paths.Answers(asked); err != nil {
		t.Errorf("the page asked for is refused: %v", err)
	}
	for name, c := range map[string]struct {
		asked PageParams
		code  record.Code
	}{
		"another review":  {PageParams{ID: sampleID, ReviewDigest: otherDigest, Page: 1}, record.CodeReviewChanged},
		"another id":      {PageParams{ID: "ap-another", ReviewDigest: sampleDigest, Page: 1}, record.CodeReviewChanged},
		"another page":    {PageParams{ID: sampleID, ReviewDigest: sampleDigest, Page: 2}, record.CodeRecordInvalid},
		"the page before": {PageParams{ID: sampleID, ReviewDigest: sampleDigest, Page: 0}, record.CodeRecordInvalid},
	} {
		refused(t, patch.Answers(c.asked), c.code, "a patch page for "+name)
		refused(t, paths.Answers(c.asked), c.code, "a paths page for "+name)
	}
}

// FuzzSplitPatch splits any bytes, read as lines, under any small cap, and
// holds the property of the split.
func FuzzSplitPatch(f *testing.F) {
	for _, l := range hostileLines() {
		f.Add([]byte(l), uint16(0))
		f.Add([]byte(strings.Repeat(l, 40)), uint16(17))
	}
	f.Add([]byte(strings.Join(hostileLines(), "\n")), uint16(300))
	f.Add([]byte("\n\n\n"), uint16(1))
	f.Add([]byte(nil), uint16(0))
	f.Add(bytes.Repeat([]byte("é"), 2000), uint16(2))
	f.Add(bytes.Repeat([]byte(`"`), 3000), uint16(40))
	f.Add(bytes.Repeat([]byte("ab\xff"), 1000), uint16(5))
	frame := pageFrame("lines", sampleID, sampleDigest)
	f.Fuzz(func(t *testing.T, data []byte, extra uint16) {
		var lines []string
		if len(data) > 0 {
			lines = strings.Split(string(data), "\n")
		}
		limit := frame + pieceFrame + 14 + int(extra)%4000
		pages, err := splitPatch(sampleID, sampleDigest, lines, limit, MaxLinePieces)
		if err != nil {
			// Only a line in too many pieces can be refused here.
			if code, _ := record.CodeOf(err); code != record.CodeRecordTooLarge || len(data)/MaxLinePieces < 1 {
				t.Fatalf("a cap of %d: %v", limit, err)
			}
			return
		}
		read := checkPatchPages(t, pages, limit, MaxLinePieces)
		got, err := JoinPatch(read)
		if err != nil {
			t.Fatalf("a cap of %d: the pages do not join: %v", limit, err)
		}
		sameLines(t, got, lines)
	})
}
