package seatapp

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestPagesAreWellFormedForAPhone renders the two pages and checks what a
// phone browser and a screen reader need: the viewport, the language, a title
// that tells the pages apart, one level-one heading and the landmarks.
func TestPagesAreWellFormedForAPhone(t *testing.T) {
	r := newRig(t)
	titles := map[string]string{}
	for _, path := range []string{"/", "/pair/"} {
		body := r.do("GET", path, nil, nil).Body.String()
		for _, want := range []string{
			"<!doctype html>",
			`<html lang="en">`,
			`<meta charset="utf-8">`,
			`<meta name="viewport" content="width=device-width, initial-scale=1">`,
			`<meta name="color-scheme" content="light dark">`,
			`<link rel="stylesheet" href="/static/app.css">`,
			"<header>", "<main>", "</main>",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: lacks %s", path, want)
			}
		}
		m := regexp.MustCompile(`<title>([^<]+)</title>`).FindStringSubmatch(body)
		if m == nil || strings.TrimSpace(m[1]) == "" {
			t.Errorf("%s: no title", path)
			continue
		}
		titles[path] = m[1]
		if n := strings.Count(body, "<h1"); n != 1 {
			t.Errorf("%s: %d level-one headings", path, n)
		}
		if n := strings.Count(body, "<main"); n != 1 {
			t.Errorf("%s: %d main landmarks", path, n)
		}
		// The viewport does not stop the person from zooming.
		if strings.Contains(body, "user-scalable") || strings.Contains(body, "maximum-scale") {
			t.Errorf("%s: the viewport stops zooming", path)
		}
	}
	if titles["/"] == titles["/pair/"] {
		t.Errorf("the pages share the title %q", titles["/"])
	}
}

func TestPairPageStatesAreWords(t *testing.T) {
	body := newRig(t).do("GET", "/pair/", nil, nil).Body.String()
	// The status line is a live region for the result, and the script's
	// failures are words (see the script's messages); the page works without
	// script by saying so.
	for _, want := range []string{`role="status"`, "<noscript>", "needs script"} {
		if !strings.Contains(body, want) {
			t.Errorf("the pair page lacks %q", want)
		}
	}
}

var cssVar = regexp.MustCompile(`--([a-z-]+):\s*#([0-9a-f]{6});`)

// luminance is the relative luminance of an sRGB colour, per WCAG.
func luminance(hex string) float64 {
	v, _ := strconv.ParseUint(hex, 16, 32)
	ch := func(shift uint) float64 {
		c := float64((v>>shift)&0xff) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(16) + 0.7152*ch(8) + 0.0722*ch(0)
}

func contrast(a, b string) float64 {
	x, y := luminance(a), luminance(b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

// TestStyleSheet checks the rules of the look that a test can: both colour
// schemes, their contrast, text no smaller than 16 px, touch targets of 44 px,
// a visible focus, nothing wider than the smallest phone and nothing fetched.
func TestStyleSheet(t *testing.T) {
	css := newRig(t).do("GET", "/static/app.css", nil, nil).Body.String()

	for _, want := range []string{
		"prefers-color-scheme: dark", "color-scheme: light dark", ":focus-visible", "min-height: 44px",
		"min-width: 44px", "overflow-wrap: anywhere", "padding-left: 16px", "padding-right: 16px",
		"font-family: system-ui", "[hidden]",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("the style sheet lacks %q", want)
		}
	}
	for _, bad := range []string{"url(", "@import", "@font-face", "http:", "https:"} {
		if strings.Contains(css, bad) {
			t.Errorf("the style sheet holds %q", bad)
		}
	}

	// Text is at least 16 px: every font-size is in rem or em of at least 1,
	// or in px of at least 16.
	for _, m := range regexp.MustCompile(`font-size:\s*([0-9.]+)(px|rem|em|%)`).FindAllStringSubmatch(css, -1) {
		v, _ := strconv.ParseFloat(m[1], 64)
		min := map[string]float64{"px": 16, "rem": 1, "em": 1, "%": 100}[m[2]]
		if v < min {
			t.Errorf("font-size %s%s is under 16 px", m[1], m[2])
		}
	}
	// Nothing sets a width that does not fit 320 px with its 16 px gutters.
	for _, m := range regexp.MustCompile(`(?m)(?:^|\s)(?:min-)?width:\s*([0-9.]+)(px|rem)`).FindAllStringSubmatch(css, -1) {
		v, _ := strconv.ParseFloat(m[1], 64)
		if m[2] == "rem" {
			v *= 16
		}
		if v > 288 && !inMedia(css, m[0]) {
			t.Errorf("a width of %s%s does not fit 320 px", m[1], m[2])
		}
	}

	// The two colour schemes are the two blocks of variables; each is
	// checked for contrast (WCAG 2.2: 4.5 for text, 7 for the main text, 3
	// for the lines and the focus ring that mark a control).
	root := css[strings.Index(css, ":root {"):]
	dark := root[strings.Index(root, "prefers-color-scheme: dark"):]
	light := root[:strings.Index(root, "@media")]
	for name, block := range map[string]string{"light": light, "dark": dark} {
		v := map[string]string{}
		for _, m := range cssVar.FindAllStringSubmatch(block, -1) {
			v[m[1]] = m[2]
		}
		for _, k := range []string{"bg", "fg", "muted", "line", "accent", "accent-fg", "focus"} {
			if v[k] == "" {
				t.Fatalf("%s scheme: no --%s", name, k)
			}
		}
		for _, c := range []struct {
			a, b string
			min  float64
		}{
			{"fg", "bg", 7}, {"muted", "bg", 4.5}, {"accent-fg", "accent", 4.5},
			{"accent", "bg", 3}, {"line", "bg", 3}, {"focus", "bg", 3},
		} {
			if got := contrast(v[c.a], v[c.b]); got < c.min {
				t.Errorf("%s scheme: --%s on --%s is %.2f:1, under %.1f:1", name, c.a, c.b, got, c.min)
			}
		}
	}
}

// inMedia reports whether the declaration is inside a media block.
func inMedia(css, decl string) bool {
	i := strings.Index(css, decl)
	open := strings.LastIndex(css[:i], "@media")
	if open < 0 {
		return false
	}
	// A media block closes with a brace at the start of a line.
	return !strings.Contains(css[open:i], "\n}")
}
