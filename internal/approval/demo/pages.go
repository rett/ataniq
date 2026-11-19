package demo

import "ataniq/internal/approval/record"

// The sizes of a page. A patch line longer than pieceBytes is carried in
// pieces, and a page holds at most pageLines pieces. A page of the path list
// holds pathsPerPage rows.
const (
	pieceBytes   = 1024
	pageLines    = 40
	pathsPerPage = 100
)

// patchPage is one page of a patch: its pieces, and for each whether it
// continues in the next piece.
type patchPage struct {
	lines []string
	cont  []bool
}

// splitPatch cuts the lines of a patch into pages. A line longer than
// pieceBytes becomes several pieces, each non-empty, and every piece but the
// last continues. An empty line is one empty piece that does not continue,
// so an empty piece never continues. Every page has at least one piece,
// except the single page of an empty patch, which has none.
func splitPatch(lines []string) []patchPage {
	var pieces []string
	var cont []bool
	for _, line := range lines {
		if line == "" {
			pieces = append(pieces, "")
			cont = append(cont, false)
			continue
		}
		for len(line) > 0 {
			n := min(len(line), pieceBytes)
			pieces = append(pieces, line[:n])
			line = line[n:]
			cont = append(cont, len(line) > 0)
		}
	}
	if len(pieces) == 0 {
		return []patchPage{{}}
	}
	var pages []patchPage
	for len(pieces) > 0 {
		n := min(len(pieces), pageLines)
		pages = append(pages, patchPage{lines: pieces[:n:n], cont: cont[:n:n]})
		pieces, cont = pieces[n:], cont[n:]
	}
	return pages
}

// splitPaths cuts the whole path list into pages of pathsPerPage rows. An
// empty list has one empty page.
func splitPaths(paths []record.PathChange) [][]record.PathChange {
	if len(paths) == 0 {
		return [][]record.PathChange{nil}
	}
	var pages [][]record.PathChange
	for len(paths) > 0 {
		n := min(len(paths), pathsPerPage)
		pages = append(pages, paths[:n:n])
		paths = paths[n:]
	}
	return pages
}
