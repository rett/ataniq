package seatapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// testThread is the id of the fake's open project.
const testThread = "0123456789abcdef"

// fakeGenesis is the interview's port in memory: it answers each verb with
// a view of its own, or with the error set for the verb, and notes every
// call.
type fakeGenesis struct {
	mu    sync.Mutex
	calls []GenesisCall
	// views replace the view of a verb; errs answer a verb with an error.
	views map[string]GenesisView
	errs  map[string]error
}

// genesisDaemon is the fake daemon that also runs the interview.
type genesisDaemon struct {
	*fakeDaemon
	g *fakeGenesis
}

func (d genesisDaemon) Genesis(ctx context.Context, c GenesisCall) (GenesisView, error) {
	return d.g.Genesis(ctx, c)
}

// withGenesis makes the rig's daemon one that runs the interview with g.
func withGenesis(g *fakeGenesis) func(*Config) {
	return func(c *Config) { c.Daemon = genesisDaemon{fakeDaemon: c.Daemon.(*fakeDaemon), g: g} }
}

// kindQuestion is the first question of the catalog, with text that a page
// must escape.
func kindQuestion() GenesisQuestion {
	return GenesisQuestion{ID: "kind", Type: GenesisTypeSingle, Prompt: "What <b>kind</b> of project is it?", Topic: "kind",
		Options: []GenesisOption{
			{ID: "software", Label: "Software <i>x</i>", Example: "A web app, e.g. a booking page"},
			{ID: "publishing", Label: "Writing to publish", Example: "A newsletter"},
			{ID: "media", Label: "A podcast or a book", Example: "A family memoir"},
		}, Other: true, YouDecide: true, Skip: true}
}

// sampleRound is a round of the three kinds of question: a single choice with
// every other pick, a multiple choice, and a scale.
func sampleRound() GenesisView {
	return GenesisView{State: GenesisStateQuestion, Thread: testThread, Round: 2, Of: 6, Filled: 1, Total: 14,
		Questions: []GenesisQuestion{
			kindQuestion(),
			{ID: "formats", Type: GenesisTypeMulti, Prompt: "Which formats?", Topic: "formats", Options: []GenesisOption{
				{ID: "document", Label: "A document", Example: "A report"},
				{ID: "website", Label: "A website", Example: "A small site"},
				{ID: "audio", Label: "Audio", Example: "A podcast"},
			}},
			{ID: "schedule", Type: GenesisTypeScale, Prompt: "How soon?", Topic: "schedule"},
		}}
}

// sampleSummary is a summary with a row in each group, the owner's own words
// in one.
func sampleSummary() GenesisView {
	q := kindQuestion()
	return GenesisView{State: GenesisStateSummary, Thread: testThread, Round: 6, Of: 6, Filled: 14, Total: 14, PlanReady: true,
		YouChose: []GenesisRow{
			{Node: "kind", Field: "kind", Label: "What kind of project is it?", Value: "Software", Question: &q},
			{Node: "software.stack", Field: "software.stack", Label: "What will it be built with?", Value: "Go <script>alert(1)</script>", OwnersWords: true},
		},
		DecidedForYou: []GenesisRow{{Node: "software.data", Field: "software.data", Label: "What data does it keep?", Value: "None"}},
		Defaults:      []GenesisRow{},
	}
}

func (f *fakeGenesis) Genesis(_ context.Context, c GenesisCall) (GenesisView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if err := f.errs[c.Verb]; err != nil {
		return GenesisView{}, err
	}
	if v, ok := f.views[c.Verb]; ok {
		return v, nil
	}
	switch c.Verb {
	case GenesisVerbList:
		return GenesisView{State: GenesisStateThreads, Threads: []GenesisThread{
			{Thread: testThread, State: GenesisStateQuestion, Round: 2, Of: 6, Filled: 1, Total: 14}}}, nil
	case GenesisVerbStart:
		v := sampleRound()
		v.Round, v.Thread = 1, "fedcba9876543210"
		return v, nil
	case GenesisVerbRound, GenesisVerbAnswer:
		v := sampleRound()
		v.Thread = c.Thread
		return v, nil
	case GenesisVerbChange:
		v := sampleSummary()
		v.Thread, v.Reset = c.Thread, 2
		return v, nil
	case GenesisVerbSummary:
		v := sampleSummary()
		v.Thread = c.Thread
		return v, nil
	case GenesisVerbFinish:
		v := sampleSummary()
		v.Thread, v.State, v.Digest = c.Thread, GenesisStateFinished, strings.Repeat("7d", 32)
		return v, nil
	case GenesisVerbDiscard:
		return GenesisView{State: GenesisStateDiscarded, Thread: c.Thread}, nil
	case GenesisVerbPlan:
		p := samplePlan()
		return GenesisView{State: GenesisStatePlan, Thread: c.Thread, Round: 6, Of: 6, Filled: 14, Total: 14, PlanReady: true, Plan: &p}, nil
	}
	return GenesisView{}, errors.New("unknown verb")
}

func (f *fakeGenesis) seen() []GenesisCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]GenesisCall(nil), f.calls...)
}

func (f *fakeGenesis) set(verb string, v *GenesisView, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.views == nil {
		f.views, f.errs = map[string]GenesisView{}, map[string]error{}
	}
	delete(f.views, verb)
	delete(f.errs, verb)
	if v != nil {
		f.views[verb] = *v
	}
	if err != nil {
		f.errs[verb] = err
	}
}

// postForm sends a same-origin form post, as a browser sends a page's form,
// with the cookies given.
func (r *rig) postForm(path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := newRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

// seeOtherTo fails the test unless w is a 303 to the path.
func seeOtherTo(t *testing.T, what string, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Errorf("%s: %d to %q, want 303 to %q", what, w.Code, w.Header().Get("Location"), want)
	}
}

func genesisRig(t *testing.T) (*rig, *fakeGenesis, *http.Cookie) {
	t.Helper()
	g := &fakeGenesis{}
	r := newRig(t, withGenesis(g))
	return r, g, r.login(t)
}

// The card list offers "New project" and links each open project when the
// daemon runs the interview, and says when two are open or the list cannot
// be read.
func TestGenesisOnTheCardList(t *testing.T) {
	r, g, s := genesisRig(t)
	body := r.getWith("/cards/", s).Body.String()
	for _, want := range []string{`<form id="start-project" action="/projects/new" method="post">`, `<button id="start-project-go" type="submit">New project</button>`,
		`href="/projects/` + testThread + `"`, "Round 2 of 6.", "Projects in progress"} {
		if !strings.Contains(body, want) {
			t.Errorf("the card list lacks %q", want)
		}
	}
	if c := g.seen(); len(c) != 1 || c[0].Verb != GenesisVerbList {
		t.Errorf("the card list made %+v", c)
	}
	// Two open: no start.
	two := GenesisView{State: GenesisStateThreads, Threads: []GenesisThread{
		{Thread: testThread, State: GenesisStateQuestion, Round: 2, Of: 6, Total: 1},
		{Thread: "fedcba9876543210", State: GenesisStateSummary, Round: 6, Of: 6, Filled: 13, Total: 14}}}
	g.set(GenesisVerbList, &two, nil)
	body = r.getWith("/cards/", s).Body.String()
	if strings.Contains(body, "start-project") || !strings.Contains(body, "Two projects are open.") || !strings.Contains(body, "Ready to review your answers. 13 of 14 required answers given.") {
		t.Errorf("two open projects:\n%s", body)
	}
	// A list that cannot be read: the start is offered and the page says so.
	g.set(GenesisVerbList, nil, ErrBusy)
	body = r.getWith("/cards/", s).Body.String()
	if !strings.Contains(body, "start-project") || !strings.Contains(body, "cannot be listed now") {
		t.Errorf("a list that failed:\n%s", body)
	}
	// Not offered: nothing of the projects.
	g.set(GenesisVerbList, nil, ErrNotOffered)
	body = r.getWith("/cards/", s).Body.String()
	if strings.Contains(body, "project") || strings.Contains(body, "<form") {
		t.Errorf("not offered, the card list still shows projects:\n%s", body)
	}
}

// A daemon without the port, or one that does not offer the interview, shows
// no "New project", and every project page answers the "not offered" page.
func TestGenesisNotOffered(t *testing.T) {
	g := &fakeGenesis{}
	g.set(GenesisVerbList, nil, ErrNotOffered)
	g.set(GenesisVerbRound, nil, ErrNotOffered)
	g.set(GenesisVerbStart, nil, ErrNotOffered)
	g.set(GenesisVerbAnswer, nil, ErrNotOffered)
	for name, r := range map[string]*rig{"no port": newRig(t), "not offered": newRig(t, withGenesis(g))} {
		s := r.login(t)
		if body := r.getWith("/cards/", s).Body.String(); strings.Contains(body, "project") {
			t.Errorf("%s: the card list mentions projects", name)
		}
		for _, path := range []string{"/projects/", "/projects/" + testThread} {
			w := r.getWith(path, s)
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "New projects are not offered") {
				t.Errorf("%s: %s: %d", name, path, w.Code)
			}
		}
		if name == "no port" {
			w := r.getWith("/projects/"+testThread+"/discard", s)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s: the discard page: %d", name, w.Code)
			}
		}
		seeOtherTo(t, name+": a start", r.postForm("/projects/new", nil, s), "/projects/")
		seeOtherTo(t, name+": an answer", r.postForm("/projects/"+testThread+"/answer", url.Values{"round": {"1"}, "n0": {"kind"}, "a0": {"software"}}, s), "/projects/")
	}
}

// A stranger without a session sees the login page only: no page and no
// post reaches the port.
func TestGenesisNeedsASession(t *testing.T) {
	r, g, _ := genesisRig(t)
	for _, path := range []string{"/projects/", "/projects/" + testThread, "/projects/" + testThread + "/change/kind", "/projects/" + testThread + "/discard"} {
		seeOtherTo(t, path, r.getWith(path), "/login/")
	}
	for _, path := range []string{"/projects/new", "/projects/" + testThread + "/answer", "/projects/" + testThread + "/change",
		"/projects/" + testThread + "/finish", "/projects/" + testThread + "/discard"} {
		seeOtherTo(t, path, r.postForm(path, url.Values{"round": {"1"}, "n0": {"kind"}, "a0": {"software"}}), "/login/")
	}
	// A forged, ended or idle session is no session, for a page or a post.
	forged := &http.Cookie{Name: SessionCookie, Value: strings.Repeat("A", tokenLen)}
	ended := r.login(t)
	r.postWith("/api/logout", "{}", ended)
	idle := r.login(t)
	r.clock.Advance(SessionIdle)
	for name, c := range map[string]*http.Cookie{"forged": forged, "ended": ended, "idle": idle} {
		seeOtherTo(t, name+": a start", r.postForm("/projects/new", nil, c), "/login/")
		for _, path := range authedRoutes("GET /projects/") {
			seeOtherTo(t, name+": "+path, r.getWith(path, c), "/login/")
		}
	}
	if c := g.seen(); len(c) != 0 {
		t.Fatalf("the port was called: %+v", c)
	}
}

// A post that does not come from the app's own page is refused before
// anything: another origin, "Origin: null", no Origin without the browser's
// same-origin fetch metadata, another site, another host, or a body that is
// not a form.
func TestGenesisCrossOriginPostsAreRefused(t *testing.T) {
	r, g, s := genesisRig(t)
	body := url.Values{"round": {"1"}, "n0": {"kind"}, "a0": {"software"}}.Encode()
	for name, h := range map[string]map[string]string{
		"another origin":             {"Origin": "https://evil.example"},
		"origin null":                {"Origin": "null"},
		"origin null, cross-site":    {"Origin": "null", "Sec-Fetch-Site": "cross-site"},
		"origin null, no fetch site": {"Origin": "null", "Sec-Fetch-Site": ""},
		"no origin, cross-site":      {"Origin": "", "Sec-Fetch-Site": "cross-site"},
		"no origin, same-site":       {"Origin": "", "Sec-Fetch-Site": "same-site"},
		"no origin, fetch site none": {"Origin": "", "Sec-Fetch-Site": "none"},
		"no origin, no fetch site":   {"Origin": "", "Sec-Fetch-Site": ""},
		"cross-site":                 {"Sec-Fetch-Site": "cross-site"},
		"same-site":                  {"Sec-Fetch-Site": "same-site"},
		"no fetch site":              {"Sec-Fetch-Site": ""},
		"another host":               {"Host": "evil.example"},
		"json":                       {"Content-Type": "application/json"},
		"multipart":                  {"Content-Type": "multipart/form-data; boundary=x"},
		"no content type":            {"Content-Type": ""},
	} {
		for _, path := range []string{"/projects/new", "/projects/" + testThread + "/answer", "/projects/" + testThread + "/discard"} {
			header := map[string]string{"Origin": testOrigin, "Content-Type": "application/x-www-form-urlencoded", "Cookie": s.Name + "=" + s.Value}
			for k, v := range h {
				header[k] = v
			}
			w := r.do("POST", path, strings.NewReader(body), header)
			if w.Code != http.StatusForbidden && w.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s: %s: %d", name, path, w.Code)
			}
		}
	}
	// A body over the app's cap is refused too.
	big := url.Values{"round": {"1"}, "n0": {"kind"}, "t0": {strings.Repeat("x", MaxBody)}}.Encode()
	w := r.do("POST", "/projects/"+testThread+"/answer", strings.NewReader(big), map[string]string{
		"Origin": testOrigin, "Content-Type": "application/x-www-form-urlencoded", "Cookie": s.Name + "=" + s.Value})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over the cap: %d", w.Code)
	}
	if c := g.seen(); len(c) != 0 {
		t.Fatalf("the port was called: %+v", c)
	}
}

// A form's post passes the origin check in exactly the cases a browser sends
// from the app's own page under its same-origin referrer policy: an Origin
// header equal to the app's origin, or no Origin header at all, each with
// Sec-Fetch-Site same-origin and the app's host. "Origin: null" (what a
// browser sends under no-referrer) is refused whatever the fetch metadata
// says, as is an Origin sent twice. A script's JSON POST, whose fetch sets
// its own policy, still needs the Origin header.
func TestGenesisFormOriginCases(t *testing.T) {
	r, g, s := genesisRig(t)
	post := func(header map[string]string) int {
		h := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Cookie": s.Name + "=" + s.Value}
		for k, v := range header {
			h[k] = v
		}
		return r.do("POST", "/projects/new", strings.NewReader(""), h).Code
	}
	for _, c := range []struct {
		name   string
		header map[string]string
		ok     bool
	}{
		{"the app's origin, same-origin", map[string]string{"Origin": testOrigin, "Sec-Fetch-Site": "same-origin"}, true},
		{"no origin, same-origin", map[string]string{"Origin": "", "Sec-Fetch-Site": "same-origin"}, true},
		{"origin null, same-origin", map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}, false},
		{"origin null, cross-site", map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}, false},
		{"origin null, no fetch site", map[string]string{"Origin": "null", "Sec-Fetch-Site": ""}, false},
		{"the app's origin, cross-site", map[string]string{"Origin": testOrigin, "Sec-Fetch-Site": "cross-site"}, false},
		{"the app's origin, no fetch site", map[string]string{"Origin": testOrigin, "Sec-Fetch-Site": ""}, false},
		{"no origin, cross-site", map[string]string{"Origin": "", "Sec-Fetch-Site": "cross-site"}, false},
		{"no origin, same-site", map[string]string{"Origin": "", "Sec-Fetch-Site": "same-site"}, false},
		{"no origin, none", map[string]string{"Origin": "", "Sec-Fetch-Site": "none"}, false},
		{"no origin, no fetch site", map[string]string{"Origin": "", "Sec-Fetch-Site": ""}, false},
		{"no origin, another host", map[string]string{"Origin": "", "Sec-Fetch-Site": "same-origin", "Host": "evil.example"}, false},
		{"another origin, same-origin", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "same-origin"}, false},
		{"an empty origin, same-origin", nil, false},
	} {
		code := 0
		if c.header == nil {
			// An Origin header that is there and empty is not a missing one.
			req := newRequest("POST", "/projects/new", strings.NewReader(""))
			req.Header["Origin"] = []string{""}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(s)
			w := httptest.NewRecorder()
			r.h.ServeHTTP(w, req)
			code = w.Code
		} else {
			code = post(c.header)
		}
		if c.ok && code != http.StatusSeeOther {
			t.Errorf("%s: %d, want the post taken (303)", c.name, code)
		}
		if !c.ok && code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", c.name, code)
		}
	}
	// The Origin header twice, even both the app's, is refused.
	req := newRequest("POST", "/projects/new", strings.NewReader(""))
	req.Header["Origin"] = []string{testOrigin, testOrigin}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s)
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("two Origin headers: %d", w.Code)
	}
	starts := 0
	for _, c := range g.seen() {
		if c.Verb == GenesisVerbStart {
			starts++
		}
	}
	if starts != 2 {
		t.Errorf("%d starts reached the port, want the two posts taken", starts)
	}
	// A script's POST is held to the Origin header: none is refused, even
	// with the same-origin fetch metadata.
	if w := r.do("POST", "/api/logout", strings.NewReader("{}"), map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin",
		"Cookie": s.Name + "=" + s.Value}); w.Code != http.StatusForbidden {
		t.Errorf("a JSON POST with no Origin: %d", w.Code)
	}
}

// Every page that holds a form answers with the referrer policy same-origin,
// under which a browser sends a form's post to the app with the app's origin
// in its Origin header. Under no-referrer it would send "Origin: null", which
// the app refuses: every form of the project pages would answer 403. The page
// inventory holds every page with a form to the same rule.
func TestFormPagesKeepTheOriginOnTheirPosts(t *testing.T) {
	r, g, s := genesisRig(t)
	sum := sampleSummary()
	pages := []struct {
		name, path string
		set        func()
	}{
		{"the card list", "/cards/", nil},
		{"the project list", "/projects/", nil},
		{"a round", "/projects/" + testThread, nil},
		{"the summary", "/projects/" + testThread, func() { g.set(GenesisVerbRound, &sum, nil) }},
		{"a change", "/projects/" + testThread + "/change/kind", nil},
		{"the discard step", "/projects/" + testThread + "/discard", nil},
	}
	for _, p := range pages {
		if p.set != nil {
			p.set()
		}
		w := r.getWith(p.path, s)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<form") {
			t.Errorf("%s: %d, or no form on it", p.name, w.Code)
			continue
		}
		got := w.Header().Values("Referrer-Policy")
		if len(got) != 1 || got[0] != "same-origin" {
			t.Errorf("%s: Referrer-Policy %q, want exactly same-origin", p.name, got)
		}
		if strings.Contains(strings.Join(got, ","), "no-referrer") {
			t.Errorf("%s: a no-referrer policy makes every form post carry Origin: null", p.name)
		}
	}
}

// A thread id or a node id that is not of its form is never asked of the
// daemon: a page is a 404, a post goes to the project list.
func TestGenesisForgedIDsAreRefused(t *testing.T) {
	r, g, s := genesisRig(t)
	for _, id := range []string{"0123456789ABCDEF", "0123456789abcde", "0123456789abcdef0", "0123456789abcdeg", "%2e%2e", "new", "ap-20261122-0001"} {
		for _, path := range []string{"/projects/" + id, "/projects/" + id + "/discard", "/projects/" + id + "/change/kind"} {
			if w := r.getWith(path, s); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: %d", path, w.Code)
			}
		}
		for _, part := range []string{"answer", "change", "finish", "discard"} {
			w := r.postForm("/projects/"+id+"/"+part, url.Values{"round": {"1"}, "n0": {"kind"}, "a0": {"x"}, "node": {"kind"}}, s)
			if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/projects/") || strings.Contains(w.Header().Get("Location"), id) && id != "new" {
				t.Errorf("%s %s: %d to %q", id, part, w.Code, w.Header().Get("Location"))
			}
		}
	}
	for _, node := range []string{"Kind", "1kind", "kind-x", strings.Repeat("a", 49)} {
		if w := r.getWith("/projects/"+testThread+"/change/"+node, s); w.Code != http.StatusNotFound {
			t.Errorf("node %q: %d", node, w.Code)
		}
		w := r.postForm("/projects/"+testThread+"/change", url.Values{"node": {node}, "a0": {"software"}}, s)
		seeOtherTo(t, "a change of node "+node, w, "/projects/"+testThread+"?note=bad")
	}
	if c := g.seen(); len(c) != 0 {
		t.Fatalf("the port was called: %+v", c)
	}
}

// The question page: the progress, every question as a block of real inputs,
// each option with its label and example as escaped text, "Other" with a
// text field of 200 only where allowed, "You decide for me" and "Skip" only
// where allowed, one submit button, and the way back to the project list.
func TestGenesisQuestionPage(t *testing.T) {
	r, g, s := genesisRig(t)
	w := r.getWith("/projects/"+testThread, s)
	body := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, body)
	}
	for _, want := range []string{
		"Round 2 of 6.", "1 of 14 required answers given.",
		`<form id="answer" action="/projects/` + testThread + `/answer" method="post">`,
		`<input type="hidden" id="round" name="round" value="2">`,
		`<input type="hidden" id="n0" name="n0" value="kind">`, `<input type="hidden" id="n1" name="n1" value="formats">`, `<input type="hidden" id="n2" name="n2" value="schedule">`,
		`What &lt;b&gt;kind&lt;/b&gt; of project is it?`, `Software &lt;i&gt;x&lt;/i&gt;`, "A web app, e.g. a booking page",
		`<input type="radio" id="q0-o0" name="a0" value="software">`,
		`<input type="radio" id="q0-other" name="a0" value="~other">`, `<input type="text" id="q0-text" name="t0" maxlength="200"`,
		`<input type="radio" id="q0-host" name="a0" value="~host">`, "You decide for me",
		`<input type="radio" id="q0-skip" name="a0" value="~skip">`,
		`<input type="checkbox" id="q1-o1" name="m1" value="website">`,
		`<input type="radio" id="q2-o1" name="a2" value="1">`, `<input type="radio" id="q2-o5" name="a2" value="5">`,
		`<a id="project-list" href="/projects/">`, `<a id="discard-link" href="/projects/` + testThread + `/discard">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the question page lacks %q", want)
		}
	}
	for _, bad := range []string{`id="q1-other"`, `id="q1-host"`, `id="q1-skip"`, `id="q2-other"`, `id="q2-text"`, "<b>kind", "<i>x", "<script>", "checked", "autofocus"} {
		if strings.Contains(body, bad) {
			t.Errorf("the question page holds %q", bad)
		}
	}
	if n := strings.Count(body, `type="submit"`); n != 1 {
		t.Errorf("%d submit buttons", n)
	}
	// The page sent nothing: one round was read.
	if c := g.seen(); len(c) != 1 || !reflect.DeepEqual(c[0], GenesisCall{Verb: GenesisVerbRound, Thread: testThread}) {
		t.Errorf("the page made %+v", c)
	}
}

// Each kind of answer a form can give reaches the port as its pick, and the
// post is answered with a redirect to the project's page.
func TestGenesisAnswersArePosted(t *testing.T) {
	for name, c := range map[string]struct {
		form url.Values
		want []GenesisAnswer
	}{
		"a choice": {url.Values{"n0": {"kind"}, "a0": {"software"}}, []GenesisAnswer{{Node: "kind", Pick: GenesisPick{Kind: GenesisKindChoice, IDs: []string{"software"}}}}},
		"choices":  {url.Values{"n1": {"formats"}, "m1": {"document", "audio"}}, []GenesisAnswer{{Node: "formats", Pick: GenesisPick{Kind: GenesisKindChoices, IDs: []string{"document", "audio"}}}}},
		"other, picked": {url.Values{"n0": {"kind"}, "a0": {"~other"}, "t0": {"  a family cookbook "}},
			[]GenesisAnswer{{Node: "kind", Pick: GenesisPick{Kind: GenesisKindOther, Text: "a family cookbook"}}}},
		"other, only typed": {url.Values{"n0": {"kind"}, "t0": {"a family cookbook"}}, []GenesisAnswer{{Node: "kind", Pick: GenesisPick{Kind: GenesisKindOther, Text: "a family cookbook"}}}},
		"you decide":        {url.Values{"n0": {"kind"}, "a0": {"~host"}}, []GenesisAnswer{{Node: "kind", Pick: GenesisPick{Kind: GenesisKindYouDecide}}}},
		"skip":              {url.Values{"n0": {"kind"}, "a0": {"~skip"}}, []GenesisAnswer{{Node: "kind", Pick: GenesisPick{Kind: GenesisKindSkip}}}},
		"three, one left blank": {url.Values{"n0": {"kind"}, "a0": {"media"}, "n1": {"formats"}, "n2": {"schedule"}, "a2": {"4"}}, []GenesisAnswer{
			{Node: "kind", Pick: GenesisPick{Kind: GenesisKindChoice, IDs: []string{"media"}}},
			{Node: "schedule", Pick: GenesisPick{Kind: GenesisKindChoice, IDs: []string{"4"}}}}},
	} {
		r, g, s := genesisRig(t)
		c.form.Set("round", "2")
		seeOtherTo(t, name, r.postForm("/projects/"+testThread+"/answer", c.form, s), "/projects/"+testThread)
		calls := g.seen()
		if len(calls) != 1 || !reflect.DeepEqual(calls[0], GenesisCall{Verb: GenesisVerbAnswer, Thread: testThread, Round: 2, Answers: c.want}) {
			t.Errorf("%s: the port was given %+v", name, calls)
		}
	}
}

// A form that does not agree with itself is refused before the port is asked,
// with a fixed note and nothing of what was typed.
func TestGenesisFormsThatDoNotAgreeAreNotSent(t *testing.T) {
	long := strings.Repeat("é", 101)
	for name, c := range map[string]struct {
		form url.Values
		note string
	}{
		"nothing answered":          {url.Values{"round": {"2"}, "n0": {"kind"}}, "bad"},
		"no round":                  {url.Values{"n0": {"kind"}, "a0": {"software"}}, "bad"},
		"round seven":               {url.Values{"round": {"7"}, "n0": {"kind"}, "a0": {"software"}}, "bad"},
		"round with a sign":         {url.Values{"round": {"+2"}, "n0": {"kind"}, "a0": {"software"}}, "bad"},
		"a node twice":              {url.Values{"round": {"2"}, "n0": {"kind"}, "a0": {"software"}, "n1": {"kind"}, "a1": {"media"}}, "bad"},
		"a node of another form":    {url.Values{"round": {"2"}, "n0": {"Kind"}, "a0": {"software"}}, "bad"},
		"two choices of one":        {url.Values{"round": {"2"}, "n0": {"kind"}, "a0": {"software", "media"}}, "bad&q=0#q0-group"},
		"one and many":              {url.Values{"round": {"2"}, "n0": {"kind"}, "a0": {"software"}, "m0": {"media"}}, "bad&q=0#q0-group"},
		"an option and own words":   {url.Values{"round": {"2"}, "n0": {"kind"}, "a0": {"software"}, "t0": {"MARKER words"}}, "bad&q=0#q0-group"},
		"other with no words":       {url.Values{"round": {"2"}, "n0": {"kind"}, "a0": {"~other"}}, "bad&q=0#q0-group"},
		"skip with words":           {url.Values{"round": {"2"}, "n0": {"kind"}, "a0": {"~skip"}, "t0": {"MARKER words"}}, "bad&q=0#q0-group"},
		"skip and an option":        {url.Values{"round": {"2"}, "n1": {"formats"}, "m1": {"~skip", "audio"}}, "bad&q=1#q1-group"},
		"a pick that is not one":    {url.Values{"round": {"2"}, "n0": {"kind"}, "a0": {"~approve"}}, "bad&q=0#q0-group"},
		"words with a control byte": {url.Values{"round": {"2"}, "n0": {"kind"}, "t0": {"MARKER\x07words"}}, "bad&q=0#q0-group"},
		"words not utf-8":           {url.Values{"round": {"2"}, "n0": {"kind"}, "t0": {"MARKER\xffwords"}}, "bad&q=0#q0-group"},
		"words twice":               {url.Values{"round": {"2"}, "n0": {"kind"}, "t0": {"MARKER", "words"}}, "bad&q=0#q0-group"},
		"words over 200 bytes":      {url.Values{"round": {"2"}, "n0": {"kind"}, "t0": {"MARKER" + long}}, "long&q=0#q0-group"},
	} {
		r, g, s := genesisRig(t)
		w := r.postForm("/projects/"+testThread+"/answer", c.form, s)
		seeOtherTo(t, name, w, "/projects/"+testThread+"?note="+c.note)
		if strings.Contains(w.Body.String()+w.Header().Get("Location"), "MARKER") {
			t.Errorf("%s: the answer holds what was typed", name)
		}
		if calls := g.seen(); len(calls) != 0 {
			t.Errorf("%s: the port was given %+v", name, calls)
		}
	}
}

// The redirect's position identifies just the rejected fieldset. Both the
// form and that fieldset refer to the fixed note on the fresh page.
func TestGenesisRejectedQuestion(t *testing.T) {
	r, g, s := genesisRig(t)
	round := sampleRound()
	round.Questions = round.Questions[:2]
	round.Questions[1].Skip = true
	g.set(GenesisVerbRound, &round, nil)
	path := "/projects/" + testThread
	w := r.postForm(path+"/answer", url.Values{"round": {"2"}, "n1": {"formats"}, "m1": {"~skip", "audio"}}, s)
	seeOtherTo(t, "conflicting choices", w, path+"?note=bad&q=1#q1-group")
	dest, _, _ := strings.Cut(w.Header().Get("Location"), "#")
	page := r.getWith(dest, s).Body.String()
	for _, want := range []string{
		`id="q1-group" tabindex="-1" aria-invalid="true" aria-describedby="answer-note"`,
		"<legend>Check this answer. Which formats?</legend>",
		`method="post" aria-describedby="answer-note"`, `id="answer-note"`,
		"Choose any options that apply.", "Leave it to its default. Choose it on its own.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("rejected question lacks %q", want)
		}
	}
	if strings.Count(page, `aria-invalid="true"`) != 1 {
		t.Error("wrong number of marked questions")
	}
	for _, query := range []string{"note=bad", "note=bad&q=2", "note=long&q=-1", "note=bad&q=01",
		"note=bad&q=9999999999999999999999", "note=bad&q=x", "note=bad&q=0&q=1", "note=busy&q=1"} {
		page := r.getWith(path+"?"+query, s).Body.String()
		if strings.Contains(page, "aria-invalid") || !strings.Contains(page, `method="post" aria-describedby="answer-note"`) {
			t.Errorf("%s did not keep the note at form level", query)
		}
	}
	w = r.postForm(path+"/change", url.Values{"node": {"kind"}, "a0": {"~other"}}, s)
	seeOtherTo(t, "invalid change", w, path+"/change/kind?note=bad&q=0#q0-group")
	page = r.getWith(path+"/change/kind?note=bad&q=0", s).Body.String()
	if !strings.Contains(page, "<legend>Check this answer. What &lt;b&gt;kind&lt;/b&gt; of project is it?</legend>") ||
		!strings.Contains(page, `id="q0-group" tabindex="-1" aria-invalid="true" aria-describedby="answer-note"`) {
		t.Error("the change page does not identify the rejected answer")
	}
}

// Every refusal of the port is a fixed note on the page the post redirects
// to; a secret-shaped answer is never shown back, nor is any refused text;
// the busy, done and unknown answers go where they belong.
func TestGenesisRefusalsAreFixedNotes(t *testing.T) {
	secret := "hunter2-MARKER-password"
	for _, c := range []struct {
		err   error
		to    string
		words string
	}{
		{ErrGenesisSecretShaped, "/projects/" + testThread + "?note=secret", "That looks like a password or key. Never type those here; say that a credential is needed instead."},
		{ErrGenesisBadAnswer, "/projects/" + testThread + "?note=bad", "That answer was not accepted. Look at the question again."},
		{ErrGenesisOverCap, "/projects/" + testThread + "?note=over-cap", "That answer would take the project past what it can hold, so it was not taken. Give a shorter one."},
		{ErrGenesisDone, "/projects/" + testThread + "?note=done", "This project is finished. Its answers can no longer change."},
		{ErrGenesisStaleRound, "/projects/" + testThread + "?note=stale", "That round was already answered."},
		{ErrGenesisUnknownThread, "/projects/?note=unknown", "That project is not open. It may have been finished, discarded or left for 14 days."},
		{ErrBusy, "/projects/" + testThread + "?note=busy", "The desk is busy. Try again in a minute."},
		{ErrDaemonUnavailable, "/projects/" + testThread + "?note=unavailable", "The desk cannot be reached. Try again in a moment."},
		{errors.New("MARKER " + secret), "/projects/" + testThread + "?note=failed", "Something went wrong. Look at the project list again."},
		{ErrNotOffered, "/projects/", ""},
	} {
		r, g, s := genesisRig(t)
		g.set(GenesisVerbAnswer, nil, c.err)
		w := r.postForm("/projects/"+testThread+"/answer", url.Values{"round": {"2"}, "n0": {"kind"}, "a0": {"~other"}, "t0": {secret}}, s)
		seeOtherTo(t, c.err.Error(), w, c.to)
		if strings.Contains(w.Body.String()+w.Header().Get("Location"), "MARKER") || strings.Contains(w.Body.String(), "hunter2") {
			t.Errorf("%v: the answer holds the text", c.err)
		}
		if c.words == "" {
			continue
		}
		page := r.getWith(c.to, s).Body.String()
		if !strings.Contains(page, c.words) || strings.Contains(page, "hunter2") || strings.Contains(page, "MARKER") {
			t.Errorf("%v: the page after:\n%s", c.err, page)
		}
		// The page after a refusal asks only for the round again: nothing
		// was sent twice.
		answers := 0
		for _, call := range g.seen() {
			if call.Verb == GenesisVerbAnswer {
				answers++
			}
		}
		if answers != 1 {
			t.Errorf("%v: %d answers sent", c.err, answers)
		}
	}
	// A note the app does not know shows nothing.
	r, _, s := genesisRig(t)
	if page := r.getWith("/projects/"+testThread+"?note=%3Cscript%3E", s).Body.String(); strings.Contains(page, "note-line") || strings.Contains(page, "script%3E") {
		t.Error("an unknown note was shown")
	}
}

// The cap of the owner's own words is in bytes, and the field says so in plain
// words: a short note, about 200 letters. Its maxlength is 200 too, as near as
// HTML comes (a browser counts UTF-16 code units), so words of fewer than 200
// letters with accents can still be too long; the note then says what
// happened, and nothing is sent.
func TestGenesisOwnWordsAreCappedInBytes(t *testing.T) {
	r, g, s := genesisRig(t)
	page := r.getWith("/projects/"+testThread, s).Body.String()
	if !strings.Contains(page, "Your own words (a short note, about 200 letters; never a password or key)") ||
		!strings.Contains(page, `maxlength="200"`) || strings.Contains(page, "200 characters") {
		t.Errorf("the own words' field:\n%s", page)
	}
	// 150 letters of two bytes each: 300 bytes.
	w := r.postForm("/projects/"+testThread+"/answer", url.Values{"round": {"2"}, "n0": {"kind"}, "t0": {strings.Repeat("é", 150)}}, s)
	seeOtherTo(t, "accented words past 200 bytes", w, "/projects/"+testThread+"?note=long&q=0#q0-group")
	for _, c := range g.seen() {
		if c.Verb == GenesisVerbAnswer {
			t.Errorf("the port was given %+v", c)
		}
	}
	note := r.getWith("/projects/"+testThread+"?note=long&q=0", s).Body.String()
	if !strings.Contains(note, "Your own words were too long, so nothing was sent. Keep them to about 200 letters (accents and emoji take more room) and send again.") {
		t.Errorf("the page after:\n%s", note)
	}
	// 200 bytes are taken.
	w = r.postForm("/projects/"+testThread+"/answer", url.Values{"round": {"2"}, "n0": {"kind"}, "t0": {strings.Repeat("é", 100)}}, s)
	seeOtherTo(t, "200 bytes", w, "/projects/"+testThread)
}

// The start: a new project's page, or the project list with a line when two
// are open already.
func TestGenesisStart(t *testing.T) {
	r, g, s := genesisRig(t)
	seeOtherTo(t, "a start", r.postForm("/projects/new", nil, s), "/projects/fedcba9876543210")
	g.set(GenesisVerbStart, nil, ErrGenesisBusy)
	seeOtherTo(t, "a third", r.postForm("/projects/new", nil, s), "/projects/?note=open")
	page := r.getWith("/projects/?note=open", s).Body.String()
	if !strings.Contains(page, "Two projects are open already. Finish or discard one before you start another.") || !strings.Contains(page, `href="/projects/`+testThread+`"`) {
		t.Errorf("the project list after a busy start:\n%s", page)
	}
	// A start whose answer names no thread of the form goes to the list.
	g.set(GenesisVerbStart, &GenesisView{State: GenesisStateQuestion, Thread: "../x"}, nil)
	seeOtherTo(t, "a start with a bad id", r.postForm("/projects/new", nil, s), "/projects/?note=failed")
}

// The summary, a change, the finish and the discard, each a post answered
// with a redirect; the owner's own words are quoted and marked, and never
// markup.
func TestGenesisSummaryChangeFinishDiscard(t *testing.T) {
	r, g, s := genesisRig(t)
	sum := sampleSummary()
	g.set(GenesisVerbRound, &sum, nil)
	page := r.getWith("/projects/"+testThread, s).Body.String()
	for _, want := range []string{"What you chose", "What was decided for you", "Defaults", "None.",
		`<q class="owners">Go &lt;script&gt;alert(1)&lt;/script&gt;</q> <span class="meta">(your own words)</span>`,
		`<a href="/projects/` + testThread + `/change/kind" aria-label="Change answer: What kind of project is it?">Change</a>`,
		`<form id="finish" action="/projects/` + testThread + `/finish" method="post">`, "Looks right: finish",
		`<a id="discard-link" href="/projects/` + testThread + `/discard">Discard this project</a>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the summary lacks %q", want)
		}
	}
	// A row the port gives no question for has no change.
	if strings.Contains(page, "/change/software.stack") || strings.Contains(page, "<script>") {
		t.Error("a row without a question offers a change, or the page holds markup")
	}
	// The change page shows the question of its row, and nothing else.
	cp := r.getWith("/projects/"+testThread+"/change/kind", s)
	if cp.Code != 200 || !strings.Contains(cp.Body.String(), `<input type="hidden" id="node" name="node" value="kind">`) ||
		!strings.Contains(cp.Body.String(), `<form id="change" action="/projects/`+testThread+`/change" method="post">`) ||
		!strings.Contains(cp.Body.String(), `value="publishing"`) {
		t.Fatalf("the change page: %d\n%s", cp.Code, cp.Body)
	}
	if w := r.getWith("/projects/"+testThread+"/change/software.stack", s); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "cannot be changed here") {
		t.Errorf("a change of a row with no question: %d", w.Code)
	}
	if w := r.getWith("/projects/"+testThread+"/change/hosts", s); w.Code != http.StatusNotFound {
		t.Errorf("a change of a node not on the summary: %d", w.Code)
	}
	// The change post: the count of cleared answers on the page after.
	seeOtherTo(t, "a change", r.postForm("/projects/"+testThread+"/change", url.Values{"node": {"kind"}, "a0": {"publishing"}}, s), "/projects/"+testThread+"?reset=2")
	if c := g.seen(); !reflect.DeepEqual(c[len(c)-1], GenesisCall{Verb: GenesisVerbChange, Thread: testThread, Node: "kind",
		Pick: GenesisPick{Kind: GenesisKindChoice, IDs: []string{"publishing"}}}) {
		t.Errorf("the change was %+v", c[len(c)-1])
	}
	if page := r.getWith("/projects/"+testThread+"?reset=2", s).Body.String(); !strings.Contains(page, "Your change cleared 2 later answers.") {
		t.Error("the page after a change does not say what it cleared")
	}
	g.set(GenesisVerbChange, &GenesisView{State: GenesisStateSummary, Thread: testThread, Of: 6}, nil)
	seeOtherTo(t, "a change that cleared nothing", r.postForm("/projects/"+testThread+"/change", url.Values{"node": {"kind"}, "a0": {"media"}}, s), "/projects/"+testThread+"?note=changed")
	// A refused change goes back to the change page with its note.
	g.set(GenesisVerbChange, nil, ErrGenesisSecretShaped)
	seeOtherTo(t, "a refused change", r.postForm("/projects/"+testThread+"/change", url.Values{"node": {"kind"}, "t0": {"MARKER"}}, s),
		"/projects/"+testThread+"/change/kind?note=secret")
	// The finish, and the finished page.
	seeOtherTo(t, "the finish", r.postForm("/projects/"+testThread+"/finish", nil, s), "/projects/"+testThread)
	fin := sampleSummary()
	fin.State, fin.Digest = GenesisStateFinished, strings.Repeat("7d", 32)
	g.set(GenesisVerbRound, &fin, nil)
	page = r.getWith("/projects/"+testThread, s).Body.String()
	for _, want := range []string{"7d7d7d7d7d7d", "Nothing has started, and no agent was given the brief yet.", `<q class="owners">`} {
		if !strings.Contains(page, want) {
			t.Errorf("the finished page lacks %q", want)
		}
	}
	if strings.Contains(page, "<form") || strings.Contains(page, "/change/") || strings.Contains(page, strings.Repeat("7d", 32)) {
		t.Error("the finished page holds a form, a change or the whole digest")
	}
	// A change page of a finished project goes to its page.
	g.set(GenesisVerbSummary, &fin, nil)
	seeOtherTo(t, "the change page of a finished project", r.getWith("/projects/"+testThread+"/change/kind", s), "/projects/"+testThread+"?note=done")
	// The discard step, then the discard.
	dp := r.getWith("/projects/"+testThread+"/discard", s).Body.String()
	if !strings.Contains(dp, `<form id="discard" action="/projects/`+testThread+`/discard" method="post">`) || !strings.Contains(dp, `<a id="keep" href="/projects/`+testThread+`">`) {
		t.Errorf("the discard page:\n%s", dp)
	}
	seeOtherTo(t, "the discard", r.postForm("/projects/"+testThread+"/discard", nil, s), "/projects/?note=discarded")
	if c := g.seen(); !reflect.DeepEqual(c[len(c)-1], GenesisCall{Verb: GenesisVerbDiscard, Thread: testThread}) {
		t.Errorf("the discard was %+v", c[len(c)-1])
	}
	g.set(GenesisVerbDiscard, nil, ErrGenesisUnknownThread)
	seeOtherTo(t, "a discard of an unknown project", r.postForm("/projects/"+testThread+"/discard", nil, s), "/projects/?note=unknown")
	// A discard of a finished project goes to its page, with the done note.
	g.set(GenesisVerbDiscard, nil, ErrGenesisDone)
	seeOtherTo(t, "a discard of a finished project", r.postForm("/projects/"+testThread+"/discard", nil, s), "/projects/"+testThread+"?note=done")
	if page := r.getWith("/projects/"+testThread+"?note=done", s).Body.String(); !strings.Contains(page, "This project is finished. Its answers can no longer change.") {
		t.Errorf("the finished page after a refused discard:\n%s", page)
	}
}

// A page of a project the daemon does not know, and the other errors of a
// page's read, are problem pages of fixed words.
func TestGenesisPageProblems(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		words  string
	}{
		{ErrGenesisUnknownThread, 404, "No such project"},
		{ErrBusy, 503, "The desk is busy"},
		{ErrDaemonUnavailable, 503, "The desk cannot be reached"},
		{ErrNotOffered, 503, "New projects are not offered"},
		{errors.New("MARKER"), 502, "Something went wrong"},
	} {
		r, g, s := genesisRig(t)
		g.set(GenesisVerbRound, nil, c.err)
		w := r.getWith("/projects/"+testThread, s)
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.words) || strings.Contains(w.Body.String(), "MARKER") {
			t.Errorf("%v: %d", c.err, w.Code)
		}
	}
	// A view of another thread is no page of this one.
	r, g, s := genesisRig(t)
	other := sampleRound()
	other.Thread = "fedcba9876543210"
	g.set(GenesisVerbRound, &other, nil)
	if w := r.getWith("/projects/"+testThread, s); w.Code != http.StatusBadGateway {
		t.Errorf("a view of another thread: %d", w.Code)
	}
}

// A page of a project is a read that keeps the session alive, as a card page
// is; the pages of one credential are bounded.
func TestGenesisPagesKeepTheSessionAndAreBounded(t *testing.T) {
	r, g, s := genesisRig(t)
	for i := 0; i < 3; i++ {
		r.clock.Advance(SessionIdle - time.Minute)
		if w := r.getWith("/projects/"+testThread, s); w.Code != 200 {
			t.Fatalf("after %d idle periods: %d", i+1, w.Code)
		}
	}
	// An idle hour ends the session, as for every page.
	r.clock.Advance(time.Hour)
	seeOtherTo(t, "after an idle hour", r.getWith("/projects/"+testThread, s), "/login/")
	s = r.login(t)
	before := len(g.seen())
	for i := 0; i < genesisRequests; i++ {
		if w := r.getWith("/projects/"+testThread, s); w.Code != 200 {
			t.Fatalf("page %d: %d", i, w.Code)
		}
	}
	w := r.getWith("/projects/"+testThread, s)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("a page past the bound: %d", w.Code)
	}
	seeOtherTo(t, "a post past the bound", r.postForm("/projects/"+testThread+"/finish", nil, s), "/projects/"+testThread+"?note=many")
	if n := len(g.seen()) - before; n > genesisRequests {
		t.Errorf("%d calls of the port", n)
	}
}

// The project pages are well formed for a phone and a screen reader: one
// level-one heading, the landmarks, a title of their own, every visible
// input with a label for it, no tabindex or autofocus that changes the
// order, and the controls in the order of reading (the questions, then the
// one button).
func TestGenesisPagesAreWellFormed(t *testing.T) {
	r, g, s := genesisRig(t)
	sum := sampleSummary()
	titles := map[string]string{}
	inputRe := regexp.MustCompile(`<input type="(radio|checkbox|text)" id="([^"]+)"`)
	for _, path := range []string{"/projects/", "/projects/" + testThread, "/projects/" + testThread + "/change/kind", "/projects/" + testThread + "/discard", "summary",
		"/projects/" + testThread + "/plan"} {
		get := path
		if path == "summary" {
			g.set(GenesisVerbRound, &sum, nil)
			get = "/projects/" + testThread
		}
		body := r.getWith(get, s).Body.String()
		for _, want := range []string{"<!doctype html>", `<html lang="en">`, `<meta name="viewport" content="width=device-width, initial-scale=1">`, `<main id="content">`, "</main>"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: lacks %s", path, want)
			}
		}
		if n := strings.Count(body, "<h1"); n != 1 {
			t.Errorf("%s: %d level-one headings", path, n)
		}
		m := regexp.MustCompile(`<title>([^<]+)</title>`).FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%s: no title", path)
			continue
		}
		if other, ok := titles[m[1]]; ok {
			t.Errorf("%s and %s share the title %q", path, other, m[1])
		}
		titles[m[1]] = path
		for _, in := range inputRe.FindAllStringSubmatch(body, -1) {
			if !strings.Contains(body, `for="`+in[2]+`"`) {
				t.Errorf("%s: the input %s has no label", path, in[2])
			}
		}
		if strings.Contains(body, "tabindex") || strings.Contains(body, "autofocus") {
			t.Errorf("%s: changes the order of the controls", path)
		}
		// The submit button comes after every input of its form.
		if i, j := strings.LastIndex(body, "<input"), strings.Index(body, `type="submit"`); i > j && j >= 0 {
			t.Errorf("%s: an input after the submit button", path)
		}
	}
}
