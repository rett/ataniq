package card

import (
	"fmt"
	"testing"

	"ataniq/internal/approval/record"
)

// Printing a Page, alone or inside another value, writes its shape and none
// of the raw bytes of a lead that its rows and lines carry.
func TestPagePrintsNoContent(t *testing.T) {
	hostile := "a\x1b[31m‮\xffb"
	pages := []Page{
		{Kind: PagePatch, N: 2, Pages: 5, Patch: []string{hostile, "x"}, Cont: []bool{false, true}},
		{Kind: PagePaths, N: 0, Pages: 1, Paths: []record.PathChange{{Path: record.Text(hostile)}}},
	}
	want := []string{
		"card.Page{kind=patch n=2 pages=5 rows=0 lines=2}",
		"card.Page{kind=paths n=0 pages=1 rows=1 lines=0}",
	}
	for i, p := range pages {
		for _, format := range []string{"%v", "%s", "%q", "%+v", "%#v", "%x", "%d"} {
			if got := fmt.Sprintf(format, p); got != want[i] {
				t.Errorf("page %d printed with %s is %q, want %q", i, format, got, want[i])
			}
		}
		got := fmt.Sprint(p, []Page{p}, struct{ P Page }{p}, &p)
		if why := rawByte(got); why != "" {
			t.Errorf("page %d printed inside other values holds %s", i, why)
		}
		if p.Kind == PagePatch && p.Patch[0] != hostile {
			t.Errorf("the field was changed")
		}
	}
}
