package seatapp

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The main navigation: on every page behind a session, a skip link first,
// then the header's <nav aria-label="Main"> of plain links ("Cards", and
// "Projects" and "Leads" while their ports are offered), with aria-current on the link
// of the page's own section, and then the content in <main id="content">.
// No public page has any of it.

// The markup of the navigation that the tests look for.
const (
	skipLink  = `<a id="skip" class="skip" href="#content">Skip to content</a>`
	navOpen   = `<nav aria-label="Main">`
	mainOpen  = `<main id="content">`
	cardsLink = `<a id="nav-cards" href="/cards/"`
	projLink  = `<a id="nav-projects" href="/projects/"`
)

// focusableRe finds the start tag of an element that takes the focus by Tab:
// a link with an address, a form control, a summary, or anything with a
// tabindex.
var focusableRe = regexp.MustCompile(`(?is)<(?:a\s[^>]*\bhref=|button\b|input\b|select\b|textarea\b|summary\b|[a-z]+\s[^>]*\btabindex=)[^>]*>`)

// firstFocusable is the start tag of the first element of a page's body that
// takes the focus by Tab, or "".
func firstFocusable(page string) string {
	i := strings.Index(page, "<body")
	if i < 0 {
		return ""
	}
	for _, m := range focusableRe.FindAllString(page[i:], -1) {
		if strings.HasPrefix(strings.ToLower(m), "<input") && strings.Contains(m, `type="hidden"`) {
			continue
		}
		return m
	}
	return ""
}

// navOf is the main navigation's markup, from its start tag to its end, or
// "" when the page has none.
func navOf(page string) string {
	i := strings.Index(page, navOpen)
	if i < 0 {
		return ""
	}
	j := strings.Index(page[i:], "</nav>")
	if j < 0 {
		return page[i:]
	}
	return page[i : i+j+len("</nav>")]
}

// hasNav reports whether a page holds any part of the main navigation.
func hasNav(page string) bool {
	for _, p := range []string{`aria-label="Main"`, `id="skip"`, `id="content"`, "aria-current", `id="nav-`} {
		if strings.Contains(page, p) {
			return true
		}
	}
	return false
}

// checkNav returns what is wrong with the main navigation of a page behind a
// session: the skip link first of the focusable elements and aimed at the
// content, one navigation inside the header before the content, holding
// plain links of the app only, and exactly one aria-current on the page, on
// a link of the navigation.
func checkNav(page string) []string {
	var found []string
	if f := firstFocusable(page); !strings.HasPrefix(f, `<a id="skip"`) {
		found = append(found, "the first focusable element is not the skip link: "+f)
	}
	if strings.Count(page, skipLink) != 1 {
		found = append(found, "not one skip link")
	}
	if strings.Count(page, mainOpen) != 1 {
		found = append(found, "the skip link's target is not the main landmark")
	}
	if strings.Count(page, navOpen) != 1 {
		found = append(found, "not one main navigation")
	}
	nav := navOf(page)
	skip, header, open, end, content := strings.Index(page, skipLink), strings.Index(page, "<header>"),
		strings.Index(page, navOpen), strings.Index(page, "</header>"), strings.Index(page, mainOpen)
	if !(skip >= 0 && skip < header && header < open && open < end && end < content) {
		found = append(found, "not in the order skip link, header with the navigation, content")
	}
	for _, bad := range []string{"<button", "<input", "<form", "<script", "<select", "<textarea", "<iframe", "<img", "style="} {
		if strings.Contains(strings.ToLower(nav), bad) {
			found = append(found, "the navigation holds "+bad)
		}
	}
	if regexp.MustCompile(`(?i)\son[a-z]+=`).MatchString(nav) {
		found = append(found, "the navigation holds an event handler")
	}
	links := regexp.MustCompile(`<a [^>]*>`).FindAllString(nav, -1)
	if len(links) == 0 || !strings.Contains(nav, cardsLink) {
		found = append(found, "the navigation has no link to the cards")
	}
	for _, a := range links {
		if !regexp.MustCompile(`^<a id="nav-[a-z]+" href="/[a-z]+/"( aria-current="page")?>$`).MatchString(a) {
			found = append(found, "a link of the navigation that is not a plain link of the app: "+a)
		}
	}
	elements, err := pageElements(page)
	if err != nil {
		return append(found, err.Error())
	}
	n := 0
	for _, el := range elements {
		if _, ok := el.attrs["aria-current"]; ok {
			n++
		}
	}
	if n != 1 {
		found = append(found, "aria-current set "+strconv.Itoa(n)+" times, not once")
	} else if !strings.Contains(nav, ` aria-current="page">`) {
		found = append(found, "aria-current outside the navigation")
	}
	return found
}

// current is the id of the navigation's link that has aria-current.
func current(page string) string {
	m := regexp.MustCompile(`<a id="(nav-[a-z]+)" href="[^"]*" aria-current="page">`).FindStringSubmatch(navOf(page))
	if m == nil {
		return ""
	}
	return m[1]
}

// Every page behind a session has the navigation, its own section current:
// the card pages "Cards", the project pages and the plan "Projects".
func TestMainNavigationOnThePagesBehindASession(t *testing.T) {
	r, g, s := genesisRig(t)
	pages := []struct{ name, path, section string }{
		{"card list", "/cards/", "nav-cards"},
		{"card", "/cards/" + testCardID, "nav-cards"},
		{"paths", "/cards/" + testCardID + "/paths/1", "nav-cards"},
		{"project list", "/projects/", "nav-projects"},
		{"question", "/projects/" + testThread, "nav-projects"},
		{"change", "/projects/" + testThread + "/change/kind", "nav-projects"},
		{"discard", "/projects/" + testThread + "/discard", "nav-projects"},
		{"plan", planPath, "nav-projects"},
		{"summary", "summary", "nav-projects"},
		{"finished", "finished", "nav-projects"},
		{"no plan", "noplan", "nav-projects"},
	}
	for _, p := range pages {
		path := p.path
		switch p.path {
		case "summary":
			sum := sampleSummary()
			g.set(GenesisVerbRound, &sum, nil)
			path = "/projects/" + testThread
		case "finished":
			fin := sampleSummary()
			fin.State, fin.Digest = GenesisStateFinished, strings.Repeat("ab", 32)
			g.set(GenesisVerbRound, &fin, nil)
			path = "/projects/" + testThread
		case "noplan":
			g.set(GenesisVerbPlan, nil, ErrGenesisNoTemplate)
			path = planPath
		}
		body := r.getWith(path, s).Body.String()
		for _, f := range checkNav(body) {
			t.Errorf("%s: %s", p.name, f)
		}
		if got := current(body); got != p.section {
			t.Errorf("%s: the current link is %q, want %q", p.name, got, p.section)
		}
		// The daemon offers the interview, so every page links it.
		if !strings.Contains(navOf(body), projLink) {
			t.Errorf("%s: no Projects link while projects are offered", p.name)
		}
		// The class the wide screen's layout styles.
		if !strings.Contains(body, "<body class=\"nav-page\">\n"+skipLink) {
			t.Errorf("%s: the body is not of the class nav-page", p.name)
		}
		// The title and the one level-one heading are the page's own.
		if strings.Count(body, "<h1") != 1 || strings.Count(body, "<title>") != 1 {
			t.Errorf("%s: not one title and one level-one heading", p.name)
		}
	}
}

// The page title identifies which card, path-list page or interview round is
// open, even in history. The project list has a distinct section heading.
func TestNavigationPageNames(t *testing.T) {
	r, _, s := genesisRig(t)
	for path, title := range map[string]string{
		"/cards/" + testCardID:              "Card " + testCardID,
		"/cards/" + testCardID + "/paths/1": "Changed paths, page 2 of 3 · " + testCardID,
		"/projects/" + testThread:           "New project, round 2 of 6",
		"/projects/":                        "Projects",
	} {
		page := r.getWith(path, s).Body.String()
		if !strings.Contains(page, "<title>"+title+"</title>") {
			t.Errorf("%s: missing title %q", path, title)
		}
		if path == "/projects/" && (!strings.Contains(page, "<h1>Projects</h1>") || !strings.Contains(page, `<h2 id="projects-h">New projects</h2>`)) {
			t.Error("project list headings do not distinguish page and section")
		}
	}
}

// The navigation shows "Projects" only while the interview is offered: the
// card list by its own list call, and the Card page and the path pages,
// which ask nothing of the interview, by the card list's last answer.
func TestMainNavigationProjectsLinkFollowsTheOffer(t *testing.T) {
	cardPages := []string{"/cards/", "/cards/" + testCardID, "/cards/" + testCardID + "/paths/1"}
	// A daemon that does not run the interview.
	r := newRig(t)
	s := r.login(t)
	for _, path := range cardPages {
		body := r.getWith(path, s).Body.String()
		for _, f := range checkNav(body) {
			t.Errorf("no interview, %s: %s", path, f)
		}
		if strings.Contains(body, projLink) || current(body) != "nav-cards" {
			t.Errorf("no interview, %s: the navigation is\n%s", path, navOf(body))
		}
	}

	// A daemon whose gate does not offer it, and then does.
	r, g, s := genesisRig(t)
	g.set(GenesisVerbList, nil, ErrNotOffered)
	for _, path := range cardPages {
		if body := r.getWith(path, s).Body.String(); strings.Contains(body, projLink) {
			t.Errorf("not offered, %s: a Projects link", path)
		}
	}
	calls := len(g.seen())
	g.set(GenesisVerbList, &GenesisView{State: GenesisStateThreads}, nil)
	// The Card page asks nothing of the interview: it still follows the
	// card list's last answer, not offered.
	if body := r.getWith(cardPages[1], s).Body.String(); strings.Contains(body, projLink) {
		t.Error("the Card page shows Projects before the card list learnt the offer")
	}
	if n := len(g.seen()); n != calls {
		t.Errorf("the Card page made %d calls of the interview", n-calls)
	}
	for _, path := range cardPages {
		if body := r.getWith(path, s).Body.String(); !strings.Contains(navOf(body), projLink) {
			t.Errorf("offered, %s: no Projects link", path)
		}
	}
	// The card list counts no more calls of the interview than before: one
	// list a load; the other two pages none.
	if n := len(g.seen()); n != calls+1 {
		t.Errorf("the pages made %d calls of the interview, want the card list's one", n-calls)
	}
}

// No public page, and no problem page, has the navigation, a skip link or
// aria-current: they are made without a session, or without knowing of one.
func TestPublicPagesHaveNoNavigation(t *testing.T) {
	r, _, s := genesisRig(t)
	for _, path := range []string{"/", "/pair/", "/login/"} {
		w := r.do("GET", path, nil, nil)
		if w.Code != 200 || hasNav(w.Body.String()) {
			t.Errorf("%s: %d, with the navigation:\n%s", path, w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), "<body>\n<header>\n<p class=\"brand\">Authority seat</p>\n</header>\n<main>\n") {
			t.Errorf("%s: the public page's frame changed:\n%s", path, w.Body)
		}
	}
	// A page behind a session asked without one is a redirect to the login,
	// and a problem page has no navigation, with a session or without.
	if w := r.do("GET", "/cards/", nil, nil); w.Code != 303 || hasNav(w.Body.String()) {
		t.Errorf("/cards/ without a session: %d\n%s", w.Code, w.Body)
	}
	for _, path := range []string{"/cards/not-a-card", "/projects/not-a-thread"} {
		w := r.getWith(path, s)
		if w.Code != 404 || hasNav(w.Body.String()) || !strings.Contains(w.Body.String(), `<a id="back" href="/cards/">`) {
			t.Errorf("%s: %d\n%s", path, w.Code, w.Body)
		}
	}
}

// The navigation adds no logout control and no script: the plan page's
// controls are links only, its own two and the navigation's, and it holds no
// button, form or script.
func TestPlanPageKeepsLinksOnly(t *testing.T) {
	var controls []string
	for _, e := range inventory {
		if e.pattern == "GET /projects/{thread}/plan" {
			controls = e.controls
		}
	}
	want := []string{"a#skip", "a#nav-cards", "a#nav-projects", "a#nav-leads", "a#project-page", "a#project-list"}
	if strings.Join(controls, " ") != strings.Join(want, " ") {
		t.Errorf("the plan page's inventory is %q, want %q", controls, want)
	}
	r, _, s := planRig(t, samplePlan())
	body := r.getWith(planPath, s).Body.String()
	for _, bad := range []string{"<button", "<form", "<script", "<input", "logout"} {
		if strings.Contains(body, bad) {
			t.Errorf("the plan page holds %q", bad)
		}
	}
	if n := strings.Count(body, "<a "); n != len(want)-1 {
		t.Errorf("the plan page has %d links, want %d", n, len(want)-1)
	}
}

// TestCheckNavSeesWhatItLooksFor shows the navigation's check on pages that
// break each rule, and on one that breaks none.
func TestCheckNavSeesWhatItLooksFor(t *testing.T) {
	page := func(nav, content string) string {
		return "<!doctype html><html lang=\"en\"><head><title>x</title></head><body>\n" + skipLink +
			"\n<header>\n<p class=\"brand\">Authority seat</p>\n" + nav + "\n</header>\n" + mainOpen + "\n" + content + "\n</main>\n</body></html>"
	}
	good := `<nav aria-label="Main">
<ul class="nav">
<li><a id="nav-cards" href="/cards/" aria-current="page">Cards</a></li>
<li><a id="nav-projects" href="/projects/">Projects</a></li>
</ul>
</nav>`
	if f := checkNav(page(good, `<h1>x</h1><p><a id="back" href="/cards/">Back</a></p>`)); len(f) != 0 {
		t.Errorf("a good page: %v", f)
	}
	for name, p := range map[string]string{
		"a button":           page(strings.Replace(good, "</ul>", `</ul><button id="logout" type="button">Log out</button>`, 1), ""),
		"a form":             page(strings.Replace(good, "</ul>", `</ul><form action="/api/logout" method="post"></form>`, 1), ""),
		"a script":           page(strings.Replace(good, "</ul>", `</ul><script src="/static/session.js"></script>`, 1), ""),
		"an input":           page(strings.Replace(good, "</ul>", `</ul><input id="q">`, 1), ""),
		"an event handler":   page(strings.Replace(good, `href="/projects/"`, `href="/projects/" onclick="x()"`, 1), ""),
		"two currents":       page(strings.Replace(good, `href="/projects/"`, `href="/projects/" aria-current="page"`, 1), ""),
		"no current":         page(strings.Replace(good, ` aria-current="page"`, "", 1), ""),
		"current elsewhere":  page(strings.Replace(good, ` aria-current="page"`, "", 1), `<a href="/cards/" aria-current="page">x</a>`),
		"a link out":         page(strings.Replace(good, `href="/projects/"`, `href="https://example.com/"`, 1), ""),
		"no cards link":      page(strings.Replace(good, `id="nav-cards" href="/cards/"`, `id="nav-x" href="/x/"`, 1), ""),
		"no navigation":      page("", ""),
		"two navigations":    page(good+good, ""),
		"a control first":    strings.Replace(page(good, ""), skipLink, `<button id="x" type="button">x</button>`+skipLink, 1),
		"a link first":       strings.Replace(page(good, ""), skipLink, `<a href="/cards/">x</a>`+skipLink, 1),
		"tabindex first":     strings.Replace(page(good, ""), skipLink, `<div tabindex="0">x</div>`+skipLink, 1),
		"no skip link":       strings.Replace(page(good, ""), skipLink, "", 1),
		"no content target":  strings.Replace(page(good, ""), mainOpen, "<main>", 1),
		"nav in the content": page("", good),
	} {
		if f := checkNav(p); len(f) == 0 {
			t.Errorf("%s: no finding", name)
		}
	}
	// A hidden input before the skip link takes no focus; a visible one does.
	if f := firstFocusable(`<body><input type="hidden" name="x">` + skipLink); !strings.HasPrefix(f, `<a id="skip"`) {
		t.Errorf("a hidden input taken for the first focusable: %s", f)
	}
	if f := firstFocusable(`<body><input type="text" id="x">` + skipLink); !strings.HasPrefix(f, "<input") {
		t.Errorf("a text input not taken for the first focusable: %s", f)
	}
}

func TestLeadsNavFollowsTheLocalHello(t *testing.T) {
	f := sampleChat()
	r := newRig(t, withGenesis(&fakeGenesis{}), withChat(f))
	s := r.login(t)
	for _, offered := range []bool{true, false} {
		f.offered = offered
		for _, path := range []string{"/cards/", "/projects/", planPath} {
			w := r.getWith(path, s)
			nav := navOf(w.Body.String())
			if w.Code != 200 || strings.Contains(nav, `id="nav-leads"`) != offered || !strings.Contains(nav, projLink) {
				t.Fatalf("%s: %d %s", path, w.Code, nav)
			}
			if offered && strings.Index(nav, projLink) > strings.Index(nav, `id="nav-leads"`) {
				t.Error("Leads precedes Projects")
			}
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("header called chat")
	}
	f.offered = true
	only := newRig(t, withChat(f))
	nav := navOf(only.getWith("/cards/", only.login(t)).Body.String())
	if strings.Contains(nav, projLink) || !strings.Contains(nav, `id="nav-leads"`) {
		t.Error("independent offers", nav)
	}
}
