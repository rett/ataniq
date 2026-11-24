package seatapp

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The pages of the guided new-project interview: the project list, the
// question page of a round, the summary, the page that changes one answer,
// the discard step, the finished page and the plan page (genesisplan.go,
// which has no form), all made on the server, with no script but the
// logout's (the plan page has none at all). Each act is a plain HTML form posted to the app, which checks it
// as it checks every changing request (formOrigin), calls the [Genesis] port
// once, and answers with a redirect to a page (post/redirect/get), never
// with a page of its own: a reload never sends a form again. A refusal is
// one of the fixed notes below, named in the redirect's query; nothing the
// owner typed is ever in a redirect, a page, or a log line, and a refusal is
// never retried. The pages need a session, as the card pages do, and hold no
// approve or decide control: an answer fills a field of the project's brief
// and nothing else.

// projectsPath is the project list.
const projectsPath = "/projects/"

// The limits of the project pages.
const (
	// genesisRequests and genesisWindow bound the project pages and posts
	// of one credential's sessions. They do not bound the gate's calls: the
	// port keeps the interview to its own room at the gate (package
	// seatlink: 12 calls in 80 seconds), and a call past it is the note
	// "The desk is busy".
	genesisRequests = 30
	genesisWindow   = time.Minute
	// maxNodeID is the longest node id.
	maxNodeID = 48
)

// validNodeID reports whether s is a node id of the catalog's grammar: a
// lower-case letter and lower-case letters, digits, "_" and ".", at most
// maxNodeID bytes. An id that is not one is not asked of the daemon.
func validNodeID(s string) bool {
	if len(s) < 1 || len(s) > maxNodeID || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// projectHref is the address of a project's page, or of a part of it
// (answer, change, finish, discard, plan): the app's own words and a thread
// id it checked.
func projectHref(thread, part string) string {
	if !ValidThreadID(thread) {
		return projectsPath
	}
	switch part {
	case "":
		return projectsPath + thread
	case "answer", "change", "finish", "discard", "plan":
		return projectsPath + thread + "/" + part
	}
	return projectsPath
}

// changeHref is the address of the page that changes the answer of a node.
func changeHref(thread, node string) string {
	if !ValidThreadID(thread) || !validNodeID(node) {
		return projectsPath
	}
	return projectsPath + thread + "/change/" + node
}

// The notes a page shows after a post, each named by a word in the
// redirect's query. Each is fixed text: a page never shows what the owner
// sent.
var genesisNotes = map[string]string{
	"bad":         "That answer was not accepted. Look at the question again.",
	"secret":      "That looks like a password or key. Never type those here; say that a credential is needed instead.",
	"over-cap":    "That answer would take the project past what it can hold, so it was not taken. Give a shorter one.",
	"long":        "Your own words were too long, so nothing was sent. Keep them to about 200 letters (accents and emoji take more room) and send again.",
	"unknown":     "That project is not open. It may have been finished, discarded or left for 14 days.",
	"done":        "This project is finished. Its answers can no longer change.",
	"stale":       "That round was already answered.",
	"open":        "Two projects are open already. Finish or discard one before you start another.",
	"busy":        "The desk is busy. Try again in a minute.",
	"unavailable": "The desk cannot be reached. Try again in a moment.",
	"failed":      "Something went wrong. Look at the project list again.",
	"many":        "This phone made too many requests in a minute. Wait a minute, then try again.",
	"discarded":   "The project was discarded. None of its answers are kept.",
	"changed":     "Your answer was changed.",
}

// noteOf is the note of a request's query: the fixed text of a known word,
// and "" for any other.
func noteOf(r *http.Request) string {
	return genesisNotes[r.URL.Query().Get("note")]
}

// noteWord is the word of the note for an error of the port.
func noteWord(err error) string {
	switch {
	case errors.Is(err, ErrGenesisBadAnswer):
		return "bad"
	case errors.Is(err, ErrGenesisSecretShaped):
		return "secret"
	case errors.Is(err, errOwnWordsTooLong):
		return "long"
	case errors.Is(err, ErrGenesisOverCap):
		return "over-cap"
	case errors.Is(err, ErrGenesisUnknownThread):
		return "unknown"
	case errors.Is(err, ErrGenesisDone):
		return "done"
	case errors.Is(err, ErrGenesisStaleRound):
		return "stale"
	case errors.Is(err, ErrGenesisBusy):
		return "open"
	case errors.Is(err, ErrBusy):
		return "busy"
	case errors.Is(err, ErrDaemonUnavailable):
		return "unavailable"
	}
	return "failed"
}

// The problems of the project pages, each with fixed words.
var (
	problemGenesisNotOffered = problem{http.StatusServiceUnavailable, "New projects are not offered", "The desk does not run new projects from the phone now."}
	problemNoProject         = problem{http.StatusNotFound, "No such project", "The desk has no open project of this name. It may have been finished, discarded or left for 14 days."}
	problemNoChange          = problem{http.StatusNotFound, "This answer cannot be changed here", "This phone cannot show that question now. Discard the project and start again, or finish it as it is."}
	problemTooManyProjects   = problem{http.StatusTooManyRequests, "Too many requests", "This phone made too many requests in a minute. Wait a minute, then try again."}
)

// genesisProblem maps an error of the port on a page's read to its problem.
func genesisProblem(err error) problem {
	switch {
	case errors.Is(err, ErrGenesisUnknownThread):
		return problemNoProject
	case errors.Is(err, ErrNotOffered):
		return problemGenesisNotOffered
	case errors.Is(err, ErrBusy):
		return problemBusy
	case errors.Is(err, ErrDaemonUnavailable):
		return problemUnavailable
	}
	return problemFailed
}

// The data of the project pages. Each has the Title the layout shows.
type (
	// projectsPart is the project list as the card list and the project
	// list show it.
	projectsPart struct {
		// Threads are the open projects; Listed is false when the list
		// could not be read, and then the part says so.
		Threads []GenesisThread
		Listed  bool
		// CanStart says a new project may be started: fewer than two are
		// open, or the list could not be read.
		CanStart bool
	}
	projectsData struct {
		Title    string
		Note     string
		Projects projectsPart
	}
	// questionView is one question of a form, with its place in the form:
	// the fields are named a<i> (one choice), m<i> (several), t<i> (own
	// words) and n<i> (the node).
	questionView struct {
		I     int
		Multi bool
		Q     GenesisQuestion
	}
	questionData struct {
		Title     string
		Note      string
		Reset     int
		View      GenesisView
		Questions []questionView
	}
	summaryData struct {
		Title string
		Note  string
		Reset int
		View  GenesisView
		// Groups are the three groups, each with its heading.
		Groups []rowGroup
	}
	rowGroup struct {
		Heading, Empty string
		Rows           []GenesisRow
	}
	changeData struct {
		Title    string
		Note     string
		Thread   string
		Row      GenesisRow
		Question questionView
	}
	discardData struct {
		Title  string
		Thread string
	}
)

// genesisPort returns the app's Genesis port, if its Daemon is one.
func (a *app) genesisPort() (Genesis, bool) {
	g, ok := a.cfg.Daemon.(Genesis)
	return g, ok
}

// sameOrigin answers a request that changes something and does not come
// from the app's own pages: its Host header is not the origin's host, its
// Sec-Fetch-Site not same-origin, or its Origin header not the app's origin.
// It reports whether the request passed. It is the check of a script's POST
// (post), whose fetch sets its own referrer policy and so always sends the
// Origin header; a form's post is checked by formOrigin.
func (a *app) sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	return a.originCheck(w, r, false)
}

// formOrigin is sameOrigin for a plain HTML form's post, which a browser
// sends with the page's own referrer policy (same-origin, see
// securityHeaders): an Origin header equal to the app's origin, the case of
// every browser in use, or, for a browser that leaves the header out of a
// form's post, no Origin header at all. The browser's own Sec-Fetch-Site,
// which no page can set, must say same-origin in both cases, and the Host
// header must be the origin's host. "Origin: null", which a browser sends for
// a post under the no-referrer policy, from a sandboxed frame or after a
// cross-origin redirect, is refused, as is any other origin.
func (a *app) formOrigin(w http.ResponseWriter, r *http.Request) bool {
	return a.originCheck(w, r, true)
}

// originCheck is the check of sameOrigin and formOrigin: absentOK lets a
// request with no Origin header at all pass on its Sec-Fetch-Site alone.
func (a *app) originCheck(w http.ResponseWriter, r *http.Request, absentOK bool) bool {
	origin, sent := r.Header["Origin"]
	originOK := sent && len(origin) == 1 && origin[0] == a.cfg.Origin
	if !sent && absentOK {
		originOK = true
	}
	if !originOK || r.Host != a.host || r.Header.Get("Sec-Fetch-Site") != "same-origin" {
		writeText(w, http.StatusForbidden, "forbidden\n")
		return false
	}
	return true
}

// form wraps the handler of a plain HTML form's post with the checks of
// every changing request (formOrigin), the form's content type and a
// session: a post without a live session goes to the login page, and the
// handler is not run. The body is cut at MaxBody by the guard.
func (a *app) form(h func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.formOrigin(w, r) {
			return
		}
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "application/x-www-form-urlencoded" {
			writeText(w, http.StatusUnsupportedMediaType, "unsupported media type\n")
			return
		}
		s, ok := a.session(r)
		if !ok {
			w.Header().Set("Location", loginPath)
			writeText(w, http.StatusSeeOther, "log in first\n")
			return
		}
		if err := r.ParseForm(); err != nil {
			var mb *http.MaxBytesError
			if errors.As(err, &mb) {
				writeText(w, http.StatusRequestEntityTooLarge, "request too large\n")
				return
			}
			writeText(w, http.StatusBadRequest, "bad request\n")
			return
		}
		h(w, r, s)
	}
}

// seeOther answers a post with a redirect to a page of the app (303), so
// that the browser loads the page with a GET and a reload never sends the
// form again. to is made of the app's own words and ids it checked; note is
// one of the words of genesisNotes, or "".
func seeOther(w http.ResponseWriter, to, note string) {
	if note != "" {
		to += "?note=" + url.QueryEscape(note)
	}
	w.Header().Set("Location", to)
	writeText(w, http.StatusSeeOther, "see other\n")
}

// genesisCall checks a session's bound of the project pages and gives the
// context of a call of the port. It reports false when the port is missing
// or the bound is met; what was answered then is the caller's to say: a page
// answers a problem, a post a redirect.
func (a *app) genesisCall(r *http.Request, s Session) (Genesis, context.Context, context.CancelFunc, error) {
	g, ok := a.genesisPort()
	if !ok {
		return nil, nil, nil, ErrNotOffered
	}
	if !a.genesisAllow(s.Credential) {
		return nil, nil, nil, errTooMany
	}
	ctx, cancel := context.WithTimeout(r.Context(), daemonTimeout)
	return g, ctx, cancel, nil
}

// genesisLogEvery is the least time between two journal lines about the
// project pages' limit; the refusals in between are counted in the next.
const genesisLogEvery = 30 * time.Second

// genesisAllow counts a request of a credential against the project pages'
// limit and reports whether it is within it. A request past the limit is
// told to cfg.Logf, in a line of fixed text with no credential and nothing
// of the request, at most one in genesisLogEvery, and the refusals between
// two lines are counted in the second. A nil Logf is no line.
func (a *app) genesisAllow(credential string) bool {
	now := a.cfg.Now()
	if a.geneses.allow(credential, now) {
		return true
	}
	if a.cfg.Logf == nil {
		return false
	}
	a.glog.mu.Lock()
	if !a.glog.last.IsZero() && now.Sub(a.glog.last) < genesisLogEvery {
		a.glog.held++
		a.glog.mu.Unlock()
		return false
	}
	more := a.glog.held
	a.glog.last, a.glog.held = now, 0
	a.glog.mu.Unlock()
	if more > 0 {
		a.cfg.Logf("ataniq-seat: the project pages' limit of %d requests a minute was reached for one credential; %d more since the last line\n", genesisRequests, more)
		return false
	}
	a.cfg.Logf("ataniq-seat: the project pages' limit of %d requests a minute was reached for one credential\n", genesisRequests)
	return false
}

// errTooMany is the error of a request past a session's bound.
var errTooMany = errors.New("seatapp: too many requests")

// pageCall is genesisCall for a page: a problem page answers what failed.
func (a *app) pageCall(w http.ResponseWriter, r *http.Request, s Session, c GenesisCall) (GenesisView, bool) {
	g, ctx, cancel, err := a.genesisCall(r, s)
	if err != nil {
		if errors.Is(err, errTooMany) {
			a.problem(w, problemTooManyProjects)
		} else {
			a.problem(w, problemGenesisNotOffered)
		}
		return GenesisView{}, false
	}
	defer cancel()
	v, err := g.Genesis(ctx, c)
	if err != nil {
		a.problem(w, genesisProblem(err))
		return GenesisView{}, false
	}
	return v, true
}

// postCall is genesisCall for a post. It reports false, having answered a
// redirect, when the call was not made or failed: to the project list when
// the interview is not offered or the thread is unknown, to the thread's own
// page when it is finished or the round was already answered (the page then
// shows where the thread stands), and otherwise to back with the error's
// note.
func (a *app) postCall(w http.ResponseWriter, r *http.Request, s Session, c GenesisCall, back string) (GenesisView, bool) {
	g, ctx, cancel, err := a.genesisCall(r, s)
	if err != nil {
		if errors.Is(err, errTooMany) {
			seeOther(w, back, "many")
		} else {
			seeOther(w, projectsPath, "")
		}
		return GenesisView{}, false
	}
	defer cancel()
	v, err := g.Genesis(ctx, c)
	switch {
	case err == nil:
		return v, true
	case errors.Is(err, ErrNotOffered):
		seeOther(w, projectsPath, "")
	case errors.Is(err, ErrGenesisUnknownThread), errors.Is(err, ErrGenesisBusy):
		seeOther(w, projectsPath, noteWord(err))
	case errors.Is(err, ErrGenesisDone), errors.Is(err, ErrGenesisStaleRound):
		seeOther(w, projectHref(c.Thread, ""), noteWord(err))
	default:
		seeOther(w, back, noteWord(err))
	}
	return GenesisView{}, false
}

// listProjects reads the open projects for the card list: ok false when the
// interview is not offered, and a part that says it could not be read when
// the call failed otherwise.
func (a *app) listProjects(ctx context.Context) (projectsPart, bool) {
	g, ok := a.genesisPort()
	if !ok {
		return projectsPart{}, false
	}
	v, err := g.Genesis(ctx, GenesisCall{Verb: GenesisVerbList})
	switch {
	case errors.Is(err, ErrNotOffered):
		return projectsPart{}, false
	case err != nil || v.State != GenesisStateThreads:
		return projectsPart{CanStart: true}, true
	}
	return projectsPart{Threads: openThreads(v.Threads), Listed: true, CanStart: len(v.Threads) < MaxGenesisThreads}, true
}

// openThreads keeps the threads a page may link to: of a valid id, at most
// MaxGenesisThreads.
func openThreads(ts []GenesisThread) []GenesisThread {
	out := []GenesisThread{}
	for _, t := range ts {
		if ValidThreadID(t.Thread) && len(out) < MaxGenesisThreads {
			out = append(out, t)
		}
	}
	return out
}

// projectList answers GET /projects/: the open projects and the start.
func (a *app) projectList(w http.ResponseWriter, r *http.Request, s Session) {
	v, ok := a.pageCall(w, r, s, GenesisCall{Verb: GenesisVerbList})
	if !ok {
		return
	}
	if v.State != GenesisStateThreads {
		a.problem(w, problemFailed)
		return
	}
	part := projectsPart{Threads: openThreads(v.Threads), Listed: true, CanStart: len(v.Threads) < MaxGenesisThreads}
	a.render(w, "projects", http.StatusOK, projectsData{Title: pageTitles["projects"], Note: noteOf(r), Projects: part})
}

// projectStart answers POST /projects/new: a new thread, and its page.
func (a *app) projectStart(w http.ResponseWriter, r *http.Request, s Session) {
	v, ok := a.postCall(w, r, s, GenesisCall{Verb: GenesisVerbStart}, projectsPath)
	if !ok {
		return
	}
	if !ValidThreadID(v.Thread) {
		seeOther(w, projectsPath, "failed")
		return
	}
	seeOther(w, projectHref(v.Thread, ""), "")
}

// resetOf is the count of answers a change cleared, from a request's query:
// 1 to MaxGenesisRows, and 0 for anything else.
func resetOf(r *http.Request) int {
	q := r.URL.Query().Get("reset")
	n, err := strconv.Atoi(q)
	if err != nil || n < 1 || n > MaxGenesisRows || strconv.Itoa(n) != q {
		return 0
	}
	return n
}

// projectPage answers GET /projects/{thread}: the round the thread is at,
// its summary, or the finished brief.
func (a *app) projectPage(w http.ResponseWriter, r *http.Request, s Session) {
	thread := r.PathValue("thread")
	if !ValidThreadID(thread) {
		a.problem(w, problemNotFound)
		return
	}
	v, ok := a.pageCall(w, r, s, GenesisCall{Verb: GenesisVerbRound, Thread: thread})
	if !ok {
		return
	}
	if v.Thread != thread {
		a.problem(w, problemFailed)
		return
	}
	note, reset := noteOf(r), resetOf(r)
	switch v.State {
	case GenesisStateQuestion:
		d := questionData{Title: pageTitles["question"], Note: note, Reset: reset, View: v}
		for i, q := range v.Questions {
			if i >= MaxGenesisQuestions {
				break
			}
			d.Questions = append(d.Questions, questionView{I: i, Multi: q.Type == GenesisTypeMulti, Q: q})
		}
		if len(d.Questions) == 0 {
			a.problem(w, problemFailed)
			return
		}
		a.render(w, "question", http.StatusOK, d)
	case GenesisStateSummary:
		a.render(w, "summary", http.StatusOK, summaryData{Title: pageTitles["summary"], Note: note, Reset: reset, View: v, Groups: groupsOf(v)})
	case GenesisStateFinished:
		a.render(w, "finished", http.StatusOK, summaryData{Title: pageTitles["finished"], Note: note, View: v, Groups: groupsOf(v)})
	default:
		a.problem(w, problemFailed)
	}
}

// groupsOf are the three groups of a summary, with their headings.
func groupsOf(v GenesisView) []rowGroup {
	return []rowGroup{
		{Heading: "What you chose", Empty: "Nothing yet.", Rows: v.YouChose},
		{Heading: "What was decided for you", Empty: "Nothing.", Rows: v.DecidedForYou},
		{Heading: "Defaults", Empty: "None.", Rows: v.Defaults},
	}
}

// changePage answers GET /projects/{thread}/change/{node}: the question of
// one answer of the summary, to answer again.
func (a *app) changePage(w http.ResponseWriter, r *http.Request, s Session) {
	thread, node := r.PathValue("thread"), r.PathValue("node")
	if !ValidThreadID(thread) || !validNodeID(node) {
		a.problem(w, problemNotFound)
		return
	}
	v, ok := a.pageCall(w, r, s, GenesisCall{Verb: GenesisVerbSummary, Thread: thread})
	if !ok {
		return
	}
	if v.Thread != thread {
		a.problem(w, problemFailed)
		return
	}
	if v.State == GenesisStateFinished {
		seeOther(w, projectHref(thread, ""), "done")
		return
	}
	for _, g := range [][]GenesisRow{v.YouChose, v.DecidedForYou, v.Defaults} {
		for _, row := range g {
			if row.Node != node {
				continue
			}
			if row.Question == nil || row.Question.ID != node {
				a.problem(w, problemNoChange)
				return
			}
			q := *row.Question
			a.render(w, "change", http.StatusOK, changeData{Title: pageTitles["change"], Note: noteOf(r), Thread: thread, Row: row,
				Question: questionView{I: 0, Multi: q.Type == GenesisTypeMulti, Q: q}})
			return
		}
	}
	a.problem(w, problemNoChange)
}

// discardPage answers GET /projects/{thread}/discard: the step that asks
// whether to discard, with no call of the daemon.
func (a *app) discardPage(w http.ResponseWriter, r *http.Request, s Session) {
	thread := r.PathValue("thread")
	if !ValidThreadID(thread) {
		a.problem(w, problemNotFound)
		return
	}
	if _, ok := a.genesisPort(); !ok {
		a.problem(w, problemGenesisNotOffered)
		return
	}
	if !a.genesisAllow(s.Credential) {
		a.problem(w, problemTooManyProjects)
		return
	}
	a.render(w, "discard", http.StatusOK, discardData{Title: pageTitles["discard"], Thread: thread})
}

// The values of a question's controls that are not an option's id. An
// option id starts with a letter or a digit, so none is one of these.
const (
	pickOther = "~other"
	pickHost  = "~host"
	pickSkip  = "~skip"
)

// errNoPick is the error of a question of the form left unanswered.
var errNoPick = errors.New("seatapp: no answer")

// errOwnWordsTooLong is the error of own words past [MaxGenesisOther] bytes,
// which the page tells apart from a brief past its cap: what the owner typed
// was too long, and nothing was sent. The field's maxlength is 200 too, but a
// browser counts it in UTF-16 code units, not in bytes, so text with accents
// or emoji can be cut short of it and still be too long here.
var errOwnWordsTooLong = errors.New("seatapp: own words past their cap")

// pickOf reads the answer to question i of a posted form: the option ids of
// a<i> (one choice) or m<i> (several), the owner's own words t<i>, or one of
// the three other picks. Own words with no pick made are an "other". It
// returns errNoPick when nothing was given, errOwnWordsTooLong for own words
// past their cap, and ErrGenesisBadAnswer for a form that does not agree with
// itself (two kinds at once, own words with an option, words that are not
// plain text). Nothing of the form is in an error.
func pickOf(form url.Values, i int) (GenesisPick, error) {
	n := strconv.Itoa(i)
	one, many := form["a"+n], form["m"+n]
	text := strings.TrimSpace(form.Get("t" + n))
	if len(form["t"+n]) > 1 {
		return GenesisPick{}, ErrGenesisBadAnswer
	}
	if text != "" {
		if len(text) > MaxGenesisOther {
			return GenesisPick{}, errOwnWordsTooLong
		}
		if !plainText(text) {
			return GenesisPick{}, ErrGenesisBadAnswer
		}
	}
	if len(one) > 0 && len(many) > 0 {
		return GenesisPick{}, ErrGenesisBadAnswer
	}
	picks, kind := one, GenesisKindChoice
	if len(many) > 0 {
		picks, kind = many, GenesisKindChoices
	}
	if len(picks) == 0 {
		if text == "" {
			return GenesisPick{}, errNoPick
		}
		return GenesisPick{Kind: GenesisKindOther, Text: text}, nil
	}
	if len(picks) > MaxGenesisOptions {
		return GenesisPick{}, ErrGenesisBadAnswer
	}
	for _, p := range picks {
		if !strings.HasPrefix(p, "~") {
			continue
		}
		// One of the other picks stands alone.
		if len(picks) != 1 {
			return GenesisPick{}, ErrGenesisBadAnswer
		}
		switch p {
		case pickOther:
			if text == "" {
				return GenesisPick{}, ErrGenesisBadAnswer
			}
			return GenesisPick{Kind: GenesisKindOther, Text: text}, nil
		case pickHost:
			if text != "" {
				return GenesisPick{}, ErrGenesisBadAnswer
			}
			return GenesisPick{Kind: GenesisKindYouDecide}, nil
		case pickSkip:
			if text != "" {
				return GenesisPick{}, ErrGenesisBadAnswer
			}
			return GenesisPick{Kind: GenesisKindSkip}, nil
		}
		return GenesisPick{}, ErrGenesisBadAnswer
	}
	if text != "" || (kind == GenesisKindChoice && len(picks) != 1) {
		return GenesisPick{}, ErrGenesisBadAnswer
	}
	return GenesisPick{Kind: kind, IDs: append([]string(nil), picks...)}, nil
}

// plainText reports whether s is valid UTF-8 with no control character.
func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// projectAnswer answers POST /projects/{thread}/answer: the answers of the
// round the form showed, then the thread's page.
func (a *app) projectAnswer(w http.ResponseWriter, r *http.Request, s Session) {
	thread := r.PathValue("thread")
	if !ValidThreadID(thread) {
		seeOther(w, projectsPath, "unknown")
		return
	}
	back := projectHref(thread, "")
	form := r.PostForm
	round, err := strconv.Atoi(form.Get("round"))
	if err != nil || round < 1 || round > MaxGenesisRounds || strconv.Itoa(round) != form.Get("round") {
		seeOther(w, back, "bad")
		return
	}
	c := GenesisCall{Verb: GenesisVerbAnswer, Thread: thread, Round: round}
	seen := map[string]bool{}
	for i := 0; i < MaxGenesisQuestions; i++ {
		node := form.Get("n" + strconv.Itoa(i))
		if node == "" {
			continue
		}
		if !validNodeID(node) || seen[node] {
			seeOther(w, back, "bad")
			return
		}
		seen[node] = true
		p, err := pickOf(form, i)
		if errors.Is(err, errNoPick) {
			continue
		}
		if err != nil {
			seeOther(w, back, noteWord(err))
			return
		}
		c.Answers = append(c.Answers, GenesisAnswer{Node: node, Pick: p})
	}
	if len(c.Answers) == 0 {
		seeOther(w, back, "bad")
		return
	}
	if _, ok := a.postCall(w, r, s, c, back); !ok {
		return
	}
	seeOther(w, back, "")
}

// projectChange answers POST /projects/{thread}/change: the new answer of
// one node, then the thread's page, which says how many later answers the
// change cleared.
func (a *app) projectChange(w http.ResponseWriter, r *http.Request, s Session) {
	thread, node := r.PathValue("thread"), r.PostForm.Get("node")
	if !ValidThreadID(thread) {
		seeOther(w, projectsPath, "unknown")
		return
	}
	if !validNodeID(node) {
		seeOther(w, projectHref(thread, ""), "bad")
		return
	}
	back := changeHref(thread, node)
	p, err := pickOf(r.PostForm, 0)
	if errors.Is(err, errNoPick) {
		err = ErrGenesisBadAnswer
	}
	if err != nil {
		seeOther(w, back, noteWord(err))
		return
	}
	v, ok := a.postCall(w, r, s, GenesisCall{Verb: GenesisVerbChange, Thread: thread, Node: node, Pick: p}, back)
	if !ok {
		return
	}
	to := projectHref(thread, "")
	if v.Reset > 0 && v.Reset <= MaxGenesisRows {
		seeOther(w, to+"?reset="+strconv.Itoa(v.Reset), "")
		return
	}
	seeOther(w, to, "changed")
}

// projectFinish answers POST /projects/{thread}/finish: the brief frozen,
// then the thread's page, which shows it finished.
func (a *app) projectFinish(w http.ResponseWriter, r *http.Request, s Session) {
	thread := r.PathValue("thread")
	if !ValidThreadID(thread) {
		seeOther(w, projectsPath, "unknown")
		return
	}
	back := projectHref(thread, "")
	if _, ok := a.postCall(w, r, s, GenesisCall{Verb: GenesisVerbFinish, Thread: thread}, back); !ok {
		return
	}
	seeOther(w, back, "")
}

// projectDiscard answers POST /projects/{thread}/discard: the thread and its
// answers dropped, then the project list.
func (a *app) projectDiscard(w http.ResponseWriter, r *http.Request, s Session) {
	thread := r.PathValue("thread")
	if !ValidThreadID(thread) {
		seeOther(w, projectsPath, "unknown")
		return
	}
	if _, ok := a.postCall(w, r, s, GenesisCall{Verb: GenesisVerbDiscard, Thread: thread}, projectHref(thread, "")); !ok {
		return
	}
	seeOther(w, projectsPath, "discarded")
}
