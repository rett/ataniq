package qr

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The reference grids are the module grids of an independent encoder
// (qrencode), stored as text, so that the conformance of the encoder runs
// everywhere and does not need the tool. testdata/qrencode-reference.txt
// holds, for versions 1, 2, 6, 7 and 10 at both levels, grids with every
// mask used at least once; qrencode picks the mask itself, so the texts were
// searched until all eight masks were covered. Rewrite the file (this needs
// qrencode on PATH) with:
//
//	sh scripts/go-tool.sh test ./internal/qr/ -run WriteReferenceGrids -record-qrencode
//
// The file format: lines that start with // are comments; a grid starts with
// "grid VERSION LEVEL MASK QUOTED-TEXT", followed by one line per row of the
// grid without the quiet zone, # for a dark module and . for a light one, and
// ends at the next blank line.
const referenceFile = "testdata/qrencode-reference.txt"

var recordQrencode = flag.Bool("record-qrencode", false, "rewrite "+referenceFile+" from qrencode")

type refGrid struct {
	version int
	level   Level
	mask    int
	text    string
	rows    []string
}

func parseReference(t testing.TB) []refGrid {
	t.Helper()
	f, err := os.Open(referenceFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []refGrid
	var cur *refGrid
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "//"):
		case line == "":
			cur = nil
		case strings.HasPrefix(line, "grid "):
			parts := strings.SplitN(line, " ", 5)
			if len(parts) != 5 {
				t.Fatalf("bad grid line %q", line)
			}
			v, err1 := strconv.Atoi(parts[1])
			mask, err2 := strconv.Atoi(parts[3])
			text, err3 := strconv.Unquote(parts[4])
			var l Level
			switch parts[2] {
			case "L":
				l = L
			case "M":
				l = M
			default:
				t.Fatalf("bad level in %q", line)
			}
			if err1 != nil || err2 != nil || err3 != nil {
				t.Fatalf("bad grid line %q", line)
			}
			out = append(out, refGrid{version: v, level: l, mask: mask, text: text})
			cur = &out[len(out)-1]
		case cur != nil:
			cur.rows = append(cur.rows, line)
		default:
			t.Fatalf("row outside a grid: %q", line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (g refGrid) matrix(t testing.TB) *matrix {
	t.Helper()
	n := sizeOf(g.version)
	if len(g.rows) != n {
		t.Fatalf("version %d grid has %d rows, want %d", g.version, len(g.rows), n)
	}
	m := newMatrix(n)
	for y, row := range g.rows {
		if len(row) != n {
			t.Fatalf("version %d row %d has %d modules, want %d", g.version, y, len(row), n)
		}
		for x := 0; x < n; x++ {
			m.mod[y*n+x] = row[x] == '#'
		}
	}
	return m
}

// Encode's matrix at the reference's version, level and mask is the
// reference's, module by module, for the stored grids of qrencode. The set
// covers versions 1, 2, 6, 7 and 10 at both levels and every mask, so a wrong
// mask condition, placement, block table or version information fails here
// without any tool.
func TestMatrixEqualsReferenceGrids(t *testing.T) {
	grids := parseReference(t)
	type key struct {
		v int
		l Level
	}
	combos := map[key]bool{}
	masks := map[int]bool{}
	for _, g := range grids {
		want := g.matrix(t)
		combos[key{g.version, g.level}] = true
		masks[g.mask] = true
		// The stored grid is its own witness for the mask it says.
		if l, mask := maskOf(t, want); l != g.level || mask != g.mask {
			t.Errorf("version %d %v: the grid's format information says %v mask %d, the header %v mask %d", g.version, g.level, l, mask, g.level, g.mask)
		}
		c, err := encode(g.text, g.level, g.version, g.mask)
		if err != nil {
			t.Fatal(err)
		}
		if x, y, ok := sameMatrix(c.m, want); !ok {
			t.Errorf("version %d %v mask %d (%q): differs from the reference at (%d, %d)", g.version, g.level, g.mask, g.text, x, y)
		}
	}
	for _, v := range []int{1, 2, 6, 7, 10} {
		for _, l := range []Level{L, M} {
			if !combos[key{v, l}] {
				t.Errorf("no reference grid for version %d at %v", v, l)
			}
		}
	}
	for mask := 0; mask < 8; mask++ {
		if !masks[mask] {
			t.Errorf("no reference grid uses mask %d", mask)
		}
	}
}

// candidate is the i-th text that fits version v at level l: its length runs
// through the lengths that choose that version, and its bytes are URL-safe.
func candidate(v int, l Level, i int) string {
	var lens []int
	for n := 1; n <= byteCapacity(v, l); n++ {
		if got, err := pickVersion(n, l); err == nil && got == v {
			lens = append(lens, n)
		}
	}
	n := lens[i%len(lens)]
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	b := make([]byte, 0, n)
	x := uint32(i*2654435761 + v*40503 + int(l)*977 + 1)
	for len(b) < n {
		x = x*1664525 + 1013904223
		b = append(b, alpha[(x>>16)%uint32(len(alpha))])
	}
	b[0] = 'q' // not a dash: qrencode would read it as an option
	return string(b)
}

// TestWriteReferenceGrids rewrites the reference file from qrencode. It does
// nothing without -record-qrencode.
func TestWriteReferenceGrids(t *testing.T) {
	if !*recordQrencode {
		t.Skip("-record-qrencode is not set")
	}
	needQrencode(t)
	ver, _ := exec.Command("qrencode", "--version").CombinedOutput()
	vline := strings.SplitN(string(ver), "\n", 2)[0]
	var b strings.Builder
	fmt.Fprintf(&b, "// Reference module grids of versions 1, 2, 6, 7 and 10 at levels L and M, in byte mode,\n")
	fmt.Fprintf(&b, "// made by %s with\n", vline)
	fmt.Fprintf(&b, "//   qrencode -t ASCII -8 -m 0 -l LEVEL -v VERSION -o - TEXT\n")
	fmt.Fprintf(&b, "// and kept as # (dark) and . (light), without the quiet zone. qrencode picks the\n")
	fmt.Fprintf(&b, "// mask itself: the texts were searched until every mask was used at least once.\n")
	fmt.Fprintf(&b, "// Rewrite with: sh scripts/go-tool.sh test ./internal/qr/ -run WriteReferenceGrids -record-qrencode\n\n")
	seen := map[int]bool{}
	for _, v := range []int{1, 2, 6, 7, 10} {
		for _, l := range []Level{L, M} {
			have := 0
			for i := 0; i < 400; i++ {
				text := candidate(v, l, i)
				m := qrencodeMatrix(t, text, l, v)
				_, mask := maskOf(t, m)
				if have > 0 && seen[mask] {
					continue
				}
				have++
				seen[mask] = true
				fmt.Fprintf(&b, "grid %d %v %d %s\n", v, l, mask, strconv.Quote(text))
				for y := 0; y < m.n; y++ {
					for x := 0; x < m.n; x++ {
						if m.get(x, y) {
							b.WriteByte('#')
						} else {
							b.WriteByte('.')
						}
					}
					b.WriteByte('\n')
				}
				b.WriteByte('\n')
				if have >= 2 && len(seen) == 8 {
					break
				}
			}
		}
	}
	if len(seen) != 8 {
		t.Fatalf("only %d masks were found", len(seen))
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(referenceFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
