package seatpair

import (
	"sort"
	"strings"
	"testing"
)

// offensive holds fragments no word of the list may contain. It is a floor,
// not a review: the list was made of plain nouns by hand.
var offensive = strings.Fields(`
	ass sex gay fuck shit hell nazi rape porn slut damn dick cock crap drug
	bomb gun war nude tit piss fart poo butt hate dead death blood gore sin
	hot pimp jew coon spic nig fag dyke crack weed whore hooker kill die
	cum anal anus bum pee prick rump sperm bong boob balls nuts wad
	jail gang mob spy thug scam liar stab shoot fight punch choke hang
	noose cult devil demon satan evil vile rot stink slime puke vomit sick
	cancer tumor virus germ plague rat roach louse lice tick flea maggot
	worm slug leech boil pus scab wart mold dung sewer toilet
`)

func lev(a, b string) int {
	d := make([]int, len(b)+1)
	for j := range d {
		d[j] = j
	}
	for i := 1; i <= len(a); i++ {
		prev := d[0]
		d[0] = i
		for j := 1; j <= len(b); j++ {
			cur := d[j]
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[j] = min(d[j]+1, d[j-1]+1, prev+cost)
			prev = cur
		}
	}
	return d[len(b)]
}

func TestWordListSize(t *testing.T) {
	if len(wordList) != 256 {
		t.Fatalf("%d words, want 256", len(wordList))
	}
	for i, w := range wordList {
		if w == "" {
			t.Errorf("word %d is empty", i)
		}
	}
}

func TestWordListShape(t *testing.T) {
	for i, w := range wordList {
		if len(w) < 3 || len(w) > 6 {
			t.Errorf("word %d %q has %d letters, want 3 to 6", i, w, len(w))
		}
		for _, r := range w {
			if r < 'a' || r > 'z' {
				t.Errorf("word %d %q has %q, want lower-case ASCII letters only", i, w, r)
			}
		}
	}
	if !sort.StringsAreSorted(wordList[:]) {
		t.Error("the word list is not sorted")
	}
}

func TestWordListWordsAreDistinct(t *testing.T) {
	seen := map[string]int{}
	for i, w := range wordList {
		if j, ok := seen[w]; ok {
			t.Errorf("%q is word %d and word %d", w, j, i)
		}
		seen[w] = i
	}
}

func TestWordListHasNoPrefixes(t *testing.T) {
	for i, a := range wordList {
		for j, b := range wordList {
			if i != j && strings.HasPrefix(b, a) {
				t.Errorf("%q (%d) is a prefix of %q (%d)", a, i, b, j)
			}
		}
	}
}

func TestWordListFirstFourLettersAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, w := range wordList {
		k := w
		if len(k) > 4 {
			k = k[:4]
		}
		if other, ok := seen[k]; ok {
			t.Errorf("%q and %q begin with %q", other, w, k)
		}
		seen[k] = w
	}
}

func TestWordListWordsAreNotConfusable(t *testing.T) {
	for i, a := range wordList {
		for _, b := range wordList[i+1:] {
			if lev(a, b) < 2 {
				t.Errorf("%q and %q are one letter apart", a, b)
			}
			if len(a) == len(b) {
				var at []int
				for k := range a {
					if a[k] != b[k] {
						at = append(at, k)
					}
				}
				if len(at) == 2 && at[1] == at[0]+1 && a[at[0]] == b[at[1]] && a[at[1]] == b[at[0]] {
					t.Errorf("%q and %q differ by a swap of two letters", a, b)
				}
			}
		}
	}
}

func TestWordListHasNoOffensiveFragment(t *testing.T) {
	for _, w := range wordList {
		for _, bad := range offensive {
			if strings.Contains(w, bad) {
				t.Errorf("%q holds %q", w, bad)
			}
		}
	}
}

func TestLevenshteinHelper(t *testing.T) {
	for _, c := range []struct {
		a, b string
		n    int
	}{{"", "", 0}, {"a", "", 1}, {"kitten", "sitting", 3}, {"abc", "abd", 1}, {"abc", "acb", 2}, {"flaw", "lawn", 2}} {
		if got := lev(c.a, c.b); got != c.n {
			t.Errorf("lev(%q, %q) = %d, want %d", c.a, c.b, got, c.n)
		}
	}
}
