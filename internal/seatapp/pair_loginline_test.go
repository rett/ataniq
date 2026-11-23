package seatapp

import (
	"regexp"
	"strings"
	"testing"
)

const (
	loginNextText = "When the desk has confirmed these words, log in:"
	loginNextLink = `<a id="login-link" href="/login/">Log in</a>`
)

// TestPairPageLoginLineIsFixedAndOnlyWithTheWords: after the words are shown
// the page says to log in once the desk has confirmed, and links the login
// page. The line is a fixed element of the page, inside the section that
// holds the words, hidden until the script shows it; the script shows it only
// when the words match, so a failure state, the state before the words and
// the mismatch state never show it. It holds no server data, and no other
// page or script holds the line. What a browser shows needs a real browser.
func TestPairPageLoginLineIsFixedAndOnlyWithTheWords(t *testing.T) {
	r := newRig(t)
	page := r.do("GET", "/pair/", nil, nil).Body.String()
	if got := strings.Count(page, loginNextText); got != 1 {
		t.Fatalf("the page holds the line %d times, want once", got)
	}
	if got := strings.Count(page, loginNextLink); got != 1 {
		t.Fatalf("the page holds the link %d times, want once: %s", got, loginNextLink)
	}
	// One element: the line, then the plain same-origin link, hidden to start.
	want := `<p id="login-next" hidden>` + loginNextText + ` ` + loginNextLink + `</p>`
	if !strings.Contains(page, want) {
		t.Errorf("the page lacks %q", want)
	}
	// It is inside the section that is hidden until the words are shown.
	open := strings.Index(page, `<section id="result"`)
	line := strings.Index(page, `id="login-next"`)
	end := strings.Index(page, "</section>")
	if open < 0 || !(open < line && line < end) {
		t.Errorf("the line (at %d) is not inside the words' section (%d..%d)", line, open, end)
	}
	if !regexp.MustCompile(`<section id="result"[^>]* hidden>`).MatchString(page) {
		t.Error("the section of the words is not hidden to start")
	}
	// The page is the same whatever the request holds: no server data in it.
	if other := r.do("GET", "/pair/?code=SECRET&x=y", nil, nil).Body.String(); other != page {
		t.Error("the page changes with the query")
	}

	// The script shows the line with the words and at no other time.
	js := r.do("GET", "/static/pair.js", nil, nil).Body.String()
	if strings.Count(js, `getElementById("login-next")`) != 1 {
		t.Error(`the script must find the line once, by getElementById("login-next")`)
	}
	if strings.Count(js, "loginNext.hidden") != 1 || !strings.Contains(js, "loginNext.hidden = !res.same;") {
		t.Error("the script must set loginNext.hidden only from the match of the words, once")
	}
	// That one statement sits in show(), the function that shows the words.
	showAt := strings.Index(js, "function show(res)")
	setAt := strings.Index(js, "loginNext.hidden")
	nextFn := strings.Index(js[showAt+1:], "\n  function ")
	if showAt < 0 || setAt < showAt || (nextFn >= 0 && setAt > showAt+1+nextFn) {
		t.Error("loginNext.hidden is set outside show()")
	}
	// The script holds neither the text nor the address, and no redirect: the
	// line is the page's, and the phone is not told when the desk confirms.
	for _, bad := range []string{"log in:", "/login/", "has confirmed", `createElement("a"`, "location.href", "location.assign", "location.replace"} {
		if strings.Contains(js, bad) {
			t.Errorf("the script holds %q", bad)
		}
	}

	// No other page or script holds the line or its link.
	for _, p := range []string{"/", "/login/", "/static/login.js", "/static/words.js", "/static/app.css"} {
		body := r.do("GET", p, nil, nil).Body.String()
		for _, s := range []string{loginNextText, "login-next", "login-link"} {
			if strings.Contains(body, s) {
				t.Errorf("%s holds %q, which only the pairing page may", p, s)
			}
		}
	}
	s := r.login(t)
	for _, p := range []string{"/cards/", "/cards/" + testCardID, "/cards/" + testCardID + "/paths/1"} {
		body := r.getWith(p, s).Body.String()
		for _, x := range []string{loginNextText, "login-next", "login-link"} {
			if strings.Contains(body, x) {
				t.Errorf("%s holds %q, which only the pairing page may", p, x)
			}
		}
	}
}
