package noaicheck

import (
	"reflect"
	"strings"
	"testing"
)

const ruleset = `table inet filter { # handle 1
	chain input { # handle 1
		type filter hook input priority filter; policy drop;
		ct state established,related accept # handle 4
		iif "lo" accept
		tcp dport 443 counter packets 12 bytes 3456 accept
	}
	set seen {
		type ipv4_addr
		flags timeout
		elements = { 192.0.2.1 expires 59s123ms }
	}
}
`

func TestNormaliseNFT(t *testing.T) {
	want := "table inet filter {\n\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n" +
		"\t\tct state established,related accept\n\t\tiif \"lo\" accept\n\t\ttcp dport 443 counter accept\n\t}\n" +
		"\tset seen {\n\t\ttype ipv4_addr\n\t\tflags timeout\n\t\telements = { 192.0.2.1 }\n\t}\n}\n"
	if got := string(NormaliseNFT([]byte(ruleset))); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestNFTDigest(t *testing.T) {
	later := `table inet filter { # handle 1

	chain input { # handle 1
		type filter hook input priority filter; policy drop;
		ct state established,related accept # handle 4
		iif "lo" accept
		tcp dport 443 counter packets 99999 bytes 1 accept
	}
	set seen {
		type ipv4_addr
		flags timeout
		elements = { 192.0.2.1 expires 2s }
	}
}`
	if NFTDigest([]byte(ruleset)) != NFTDigest([]byte(later)) {
		t.Error("counters, expiry, handles, blank lines and trailing spaces changed the digest")
	}
	changed := []byte(ruleset[:len(ruleset)-2] + "\tchain output { type filter hook output priority filter; policy accept; }\n}\n")
	if NFTDigest([]byte(ruleset)) == NFTDigest(changed) {
		t.Error("a new chain did not change the digest")
	}
	if len(NFTDigest(nil)) != 64 {
		t.Error("not a SHA-256 in hex")
	}
}

// Only the documented volatile values are removed, by their position in
// the line's tokens; a quoted operand is never touched.
func TestNFTDigestQuotedOperands(t *testing.T) {
	rule := func(r string) []byte {
		return []byte("table inet filter {\n\tchain input {\n\t\t" + r + "\n\t}\n}\n")
	}
	same := [][2]string{
		{`tcp dport 443 counter packets 1 bytes 60 accept`, `tcp dport 443 counter packets 99 bytes 7000 accept`},
		{`ip saddr @seen counter packets 0 bytes 0 drop # handle 7`, `ip saddr @seen counter packets 5 bytes 300 drop # handle 12`},
		{`quota over 25 mbytes used 0 bytes accept`, `quota over 25 mbytes used 4096 bytes accept`},
		{`elements = { 192.0.2.1 expires 59s123ms, 192.0.2.2 expires 3s }`, `elements = { 192.0.2.1 expires 1s, 192.0.2.2 expires 2m }`},
		{`packets 3 bytes 180`, `packets 9 bytes 540`},
	}
	for _, p := range same {
		if NFTDigest(rule(p[0])) != NFTDigest(rule(p[1])) {
			t.Errorf("volatile values changed the digest:\n%s\n%s", p[0], p[1])
		}
	}
	differ := [][2]string{
		{`iifname "x counter packets 1 bytes 2" accept`, `iifname "x counter packets 3 bytes 4" accept`},
		{`iifname "eth0 expires 5s" accept`, `iifname "eth0 expires 9s" accept`},
		{`tcp dport 22 accept comment "used 1 bytes"`, `tcp dport 22 accept comment "used 2 bytes"`},
		{`tcp dport 22 accept comment "# handle 1"`, `tcp dport 22 accept comment "# handle 2"`},
		{`tcp dport 22 counter packets 1 bytes 2 accept`, `tcp dport 23 counter packets 1 bytes 2 accept`},
		{`meta mark 1 accept # handle 3 extra`, `meta mark 1 accept # handle 4 extra`},
	}
	for _, p := range differ {
		if NFTDigest(rule(p[0])) == NFTDigest(rule(p[1])) {
			t.Errorf("different rules gave one digest:\n%s\n%s", p[0], p[1])
		}
	}
	if got := string(NormaliseNFT(rule(`iifname "a counter packets 1 bytes 2" counter packets 7 bytes 9 accept # handle 3`))); !strings.Contains(got,
		"\t\tiifname \"a counter packets 1 bytes 2\" counter accept\n") {
		t.Errorf("normalised %q", got)
	}
}

func TestCheckNFT(t *testing.T) {
	pinned := NFTDigest([]byte(ruleset))
	cases := []struct {
		name      string
		digest    string
		listing   []byte
		want      []Hit
		wantNotes []string
	}{
		{"same", pinned, []byte(ruleset), nil, nil},
		{"differs", pinned, []byte("table inet filter {\n}\n"), []Hit{{KindNFT, "ruleset", DetailDigestDiffers}}, nil},
		{"empty listing", pinned, []byte{}, []Hit{{KindNFT, "ruleset", DetailDigestDiffers}}, nil},
		{"no listing", pinned, nil, []Hit{{KindNFT, "ruleset", DetailListingMissing}}, nil},
		{"not pinned", "", []byte(ruleset), nil, []string{NoteNFTNotPinned}},
		{"not pinned, no listing", "", nil, nil, []string{NoteNFTNotPinned}},
	}
	t.Run("not pinned, policy required", func(t *testing.T) {
		c := newFixture(t).checker(testLists())
		c.RequirePolicy = true
		c.CheckNFT([]byte(ruleset))
		r := c.Report()
		if want := []Hit{{KindPolicy, FileNFTDigest, DetailMissing}}; !reflect.DeepEqual(r.Hits, want) || len(r.Notes) != 0 || r.Complete {
			t.Errorf("%+v", r)
		}
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := testLists()
			l.NFTDigest = tc.digest
			c := newFixture(t).checker(l)
			c.CheckNFT(tc.listing)
			r := c.Report()
			if !(len(r.Hits) == 0 && len(tc.want) == 0) && !reflect.DeepEqual(r.Hits, tc.want) {
				t.Errorf("hits %v, want %v", r.Hits, tc.want)
			}
			if !(len(r.Notes) == 0 && len(tc.wantNotes) == 0) && !reflect.DeepEqual(r.Notes, tc.wantNotes) {
				t.Errorf("notes %v, want %v", r.Notes, tc.wantNotes)
			}
		})
	}
}
