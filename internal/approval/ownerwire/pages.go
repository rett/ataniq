package ownerwire

import (
	"strconv"
	"unicode/utf8"

	"ataniq/internal/approval/record"
)

// This file splits a path list and a patch into pages that fit under the
// cap, and joins pages back. The daemon splits and the console joins.
//
// The rules of a split patch are the ones the console's pager reads by,
// because the pager ends a reading on a page that breaks one: no piece that
// continues is empty; every page holds at least one piece, except the one
// page of an empty patch; no line travels in more than MaxLinePieces
// pieces.

// pageDigits is the room kept for each of a page's two numbers. The number
// of pages is not known until the last page is made, so every page keeps
// room for the longest number an int can be.
const pageDigits = 20

// pieceFrame is what a piece costs beside its text: the object around it
// and the comma after it. "false" is the longer of the two words, so a
// piece that continues costs one byte less than is counted.
const pieceFrame = len(`{"cont":false,"text":},`)

// minPieceRoom is the least room on a page that a piece of a split line is
// put in. A smaller rest of a page is left unused, so that a line is not
// carried in slivers.
const minPieceRoom = 64

// pageFrame returns what a page costs beside its entries: its keys, its id,
// its digest, and room for its two numbers. list is the key of the entries.
func pageFrame(list, id, digest string) int {
	return len(`{"id":"","":[],"page":,"pages":,"review_digest":""}`) +
		len(list) + len(id) + len(digest) + 2*pageDigits
}

// checkPageOwner checks the id and the review digest a split is made for.
func checkPageOwner(id, digest, where string) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.pendingID(id, where+".id")
	c.sha256(digest, where+".review_digest")
	return c.result()
}

func splitRefusal(detail string) error {
	return &record.Error{Code: record.CodeRecordTooLarge, Detail: detail}
}

func joinRefusal(detail string) error {
	return &record.Error{Code: record.CodeRecordInvalid, Detail: detail}
}

// SplitPaths splits the full path list of a review into pages for
// owner.paths. id is the pending record's id and reviewDigest the digest of
// the review. Every page is at most [MaxPageBytes] as encoded and holds at
// least one path; an empty list gives one page with none. The paths keep
// their order, and [JoinPaths] returns the list as it was.
//
// A path is never split. One path too large for a page, or a list of more
// than [MaxPages] pages, is refused record-too-large.
func SplitPaths(id, reviewDigest string, paths []record.PathChange) ([]PathsPage, error) {
	return splitPaths(id, reviewDigest, paths, MaxPageBytes)
}

func splitPaths(id, reviewDigest string, paths []record.PathChange, limit int) ([]PathsPage, error) {
	if err := checkPageOwner(id, reviewDigest, "paths"); err != nil {
		return nil, err
	}
	budget := limit - pageFrame("paths", id, reviewDigest)
	var (
		pages []PathsPage
		page  []record.PathChange
		used  int
		buf   []byte
	)
	for i := range paths {
		var err error
		if buf, err = record.AppendMarshal(buf[:0], &paths[i], "paths.paths["+strconv.Itoa(i)+"]"); err != nil {
			return nil, &record.Error{Code: record.CodeRecordInvalid, Detail: err.Error()}
		}
		cost := len(buf) + 1 // the comma
		if cost > budget {
			return nil, splitRefusal("a path is too large for a page of " + strconv.Itoa(limit) + " bytes")
		}
		if used+cost > budget {
			pages = append(pages, PathsPage{Paths: page})
			page, used = nil, 0
		}
		page = append(page, paths[i])
		used += cost
	}
	if len(page) > 0 || len(pages) == 0 {
		if page == nil {
			page = []record.PathChange{}
		}
		pages = append(pages, PathsPage{Paths: page})
	}
	if len(pages) > MaxPages {
		return nil, splitRefusal("the path list needs more than " + strconv.Itoa(MaxPages) + " pages")
	}
	for i := range pages {
		pages[i].ID, pages[i].ReviewDigest = id, reviewDigest
		pages[i].Page, pages[i].Pages = i, len(pages)
		if err := checkPathsPage(&pages[i]); err != nil {
			return nil, err
		}
	}
	return pages, nil
}

// JoinPaths puts the pages of a path list together again. pages are all the
// pages of one list, in order. Pages of another id or another review digest
// than the first are refused review-changed; a page that is malformed, out
// of its place, or of another count of pages is refused record-invalid.
func JoinPaths(pages []PathsPage) ([]record.PathChange, error) {
	if len(pages) == 0 {
		return nil, joinRefusal("a path list has no page")
	}
	paths := []record.PathChange{}
	for i := range pages {
		p := &pages[i]
		if err := checkPathsPage(p); err != nil {
			return nil, err
		}
		if err := checkPlace(p.ID, p.ReviewDigest, p.Page, p.Pages, pages[0].ID, pages[0].ReviewDigest, i, len(pages)); err != nil {
			return nil, err
		}
		paths = append(paths, p.Paths...)
	}
	return paths, nil
}

// checkPlace checks that a page belongs to the same review as the first
// page and stands in its own place among n pages.
func checkPlace(id, digest string, page, pages int, firstID, firstDigest string, i, n int) error {
	if id != firstID || digest != firstDigest {
		return &record.Error{Code: record.CodeReviewChanged, Detail: "page " + strconv.Itoa(i) + " belongs to another review than the first page"}
	}
	if page != i || pages != n {
		return joinRefusal("page " + strconv.Itoa(i) + " of " + strconv.Itoa(n) + " says it is another page, or of another number of pages")
	}
	return nil
}

// SplitPatch splits a patch into pages for owner.patch. id is the pending
// record's id and reviewDigest the digest of the review. lines are the
// patch's lines as raw bytes, without their line ends; any byte may be in
// them. Every page is at most [MaxPageBytes] as encoded. [JoinPatch]
// returns the lines as they were, byte for byte.
//
// A line that does not fit in the room left on a page is carried in pieces:
// each piece but the last continues, and the next piece starts the next
// page. No piece that continues is empty, and every page holds at least one
// piece, except that an empty patch gives one page with none.
//
// A line longer than [MaxLineBytes], a line that would need more than
// [MaxLinePieces] pieces, or a patch of more than [MaxPages] pages is
// refused record-too-large: the console's pager would end the reading on
// it.
func SplitPatch(id, reviewDigest string, lines []string) ([]PatchPage, error) {
	return splitPatch(id, reviewDigest, lines, MaxPageBytes, MaxLinePieces)
}

func splitPatch(id, reviewDigest string, lines []string, limit, maxPieces int) ([]PatchPage, error) {
	if err := checkPageOwner(id, reviewDigest, "patch"); err != nil {
		return nil, err
	}
	budget := limit - pageFrame("lines", id, reviewDigest)
	var (
		pages []PatchPage
		page  []PatchLine
		used  int
	)
	flush := func() {
		pages = append(pages, PatchPage{Lines: page})
		page, used = nil, 0
	}
	for i, line := range lines {
		if len(line) > MaxLineBytes {
			return nil, splitRefusal("line " + strconv.Itoa(i) + " of the patch is longer than " + strconv.Itoa(MaxLineBytes) + " bytes")
		}
		rest, pieces := line, 0
		for {
			room := budget - used - pieceFrame
			pieces++
			if pieces > maxPieces {
				return nil, splitRefusal("line " + strconv.Itoa(i) + " of the patch needs more than " + strconv.Itoa(maxPieces) + " pieces")
			}
			// Encoded, a text is longer than its bytes, so a rest
			// longer than the room needs no measuring.
			if len(rest) <= room && record.TextLen(record.Text(rest)) <= room {
				page = append(page, PatchLine{Text: record.Text(rest)})
				used += pieceFrame + record.TextLen(record.Text(rest))
				break
			}
			n := 0
			if room >= minPieceRoom || len(page) == 0 {
				n = fit(rest, room)
			}
			if n == 0 {
				if len(page) == 0 {
					return nil, splitRefusal("a page of " + strconv.Itoa(limit) + " bytes has no room for a piece of a line")
				}
				// The rest of this page is too small: the line
				// starts, or goes on, at the top of the next.
				flush()
				pieces--
				continue
			}
			page = append(page, PatchLine{Text: record.Text(rest[:n]), Cont: true})
			rest = rest[n:]
			flush()
		}
	}
	if len(page) > 0 || len(pages) == 0 {
		if page == nil {
			page = []PatchLine{}
		}
		flush()
	}
	if len(pages) > MaxPages {
		return nil, splitRefusal("the patch needs more than " + strconv.Itoa(MaxPages) + " pages")
	}
	for i := range pages {
		pages[i].ID, pages[i].ReviewDigest = id, reviewDigest
		pages[i].Page, pages[i].Pages = i, len(pages)
		if err := checkPatchPage(&pages[i]); err != nil {
			return nil, err
		}
	}
	return pages, nil
}

// fit returns how many bytes of s, from its start, make the longest piece
// whose text is at most room bytes as encoded. s itself does not fit. The
// answer is under len(s), and is 0 when not one byte fits.
//
// A text has two forms, and which one a piece takes depends on where the
// cut falls: a cut inside a character leaves bytes that are not UTF-8, and
// the piece is then written as b64. So two cuts are tried. One is the most
// bytes that fit as b64. The other is the longest run of whole characters
// that can stand in a JSON string and fits as one.
func fit(s string, room int) int {
	// As b64: the frame, and four bytes of base64 for every three.
	asB64 := 0
	if over := record.B64FrameBytes; room >= over+4 {
		asB64 = (room - over) / 4 * 3
	}
	if asB64 > len(s) {
		asB64 = len(s)
	}
	// As a string: the two quotes, every byte, and a backslash before each
	// quote and backslash.
	asString, cost := 0, 2
	for asString < len(s) {
		r, size := utf8.DecodeRuneInString(s[asString:])
		if (r == utf8.RuneError && size <= 1) || record.IsControl(r) {
			break
		}
		step := size
		if r == '"' || r == '\\' {
			step++
		}
		if cost+step > room {
			break
		}
		cost += step
		asString += size
	}
	// The b64 cut is taken only when its bytes are in fact written as b64.
	// When they could stand in a string they are written as one, and that
	// is the other cut, or a longer one that did not fit.
	n := asString
	if asB64 > n && !record.Text(s[:asB64]).Plain() {
		n = asB64
	}
	if n >= len(s) || (n > 0 && record.TextLen(record.Text(s[:n])) > room) {
		return 0
	}
	return n
}

// JoinPatch puts the pages of a patch together again and returns its lines
// as raw bytes, each line joined from its pieces. pages are all the pages
// of one patch, in order. Pages of another id or another review digest than
// the first are refused review-changed. A page that is malformed, out of
// its place or of another count of pages, a line in more than
// [MaxLinePieces] pieces or longer than [MaxLineBytes], and a patch that
// ends in a piece that continues are refused record-invalid.
func JoinPatch(pages []PatchPage) ([]string, error) {
	if len(pages) == 0 {
		return nil, joinRefusal("a patch has no page")
	}
	lines := []string{}
	var (
		open   []byte
		pieces int
	)
	for i := range pages {
		p := &pages[i]
		if err := checkPatchPage(p); err != nil {
			return nil, err
		}
		if err := checkPlace(p.ID, p.ReviewDigest, p.Page, p.Pages, pages[0].ID, pages[0].ReviewDigest, i, len(pages)); err != nil {
			return nil, err
		}
		for _, l := range p.Lines {
			pieces++
			if pieces > MaxLinePieces {
				return nil, joinRefusal("a line of the patch is in more than " + strconv.Itoa(MaxLinePieces) + " pieces")
			}
			if len(open)+len(l.Text) > MaxLineBytes {
				return nil, joinRefusal("a line of the patch is longer than " + strconv.Itoa(MaxLineBytes) + " bytes")
			}
			if l.Cont {
				open = append(open, l.Text...)
				continue
			}
			if pieces == 1 {
				lines = append(lines, string(l.Text))
			} else {
				lines = append(lines, string(append(open, l.Text...)))
			}
			open, pieces = open[:0], 0
		}
	}
	if pieces != 0 {
		// The check of the last page holds this already.
		return nil, joinRefusal("the patch ends in a piece that continues")
	}
	return lines, nil
}

// Answers checks that the page is the one q asked for. A page of another
// id or another review digest is refused review-changed, and a page of
// another number record-invalid. A reader makes this check on every page
// before it shows a byte of it.
func (p PathsPage) Answers(q PageParams) error {
	return answers(p.ID, p.ReviewDigest, p.Page, q)
}

// Answers checks that the page is the one q asked for, as
// [PathsPage.Answers] does.
func (p PatchPage) Answers(q PageParams) error {
	return answers(p.ID, p.ReviewDigest, p.Page, q)
}

func answers(id, digest string, page int, q PageParams) error {
	if id != q.ID || digest != q.ReviewDigest {
		return &record.Error{Code: record.CodeReviewChanged, Detail: "the page belongs to another review than the one asked for"}
	}
	if page != q.Page {
		return joinRefusal("the page is not the page asked for")
	}
	return nil
}
