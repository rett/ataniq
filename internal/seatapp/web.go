package seatapp

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

//go:embed assets
var assetFS embed.FS

//go:embed templates
var templateFS embed.FS

// pageData is what a page template is given.
type pageData struct {
	Title string
	// RPID and RPName are the passkey's relying party, for the pairing page.
	RPID, RPName string
}

// pageNames are the pages, one template file each; each defines "content"
// inside the shared layout.
var pageNames = []string{"home", "pair", "login", "cards", "card", "paths", "problem"}

var pageTitles = map[string]string{
	"home":  "Authority seat",
	"pair":  "Pair this phone",
	"login": "Log in",
	"cards": "Cards",
	"card":  "Card",
	"paths": "Changed paths",
}

// The most characters of each kind of text a page shows; Text says how many
// more there were.
const (
	maxName = 64   // a project, a role, a verb, a feature, a class
	maxID   = 64   // an id, a state
	maxLine = 1024 // the host's line
	maxPath = 1024 // a path or a ref's name
	maxText = 4000 // a line of the plan or of the note
)

// fits reports whether the Card page shows a lead's text whole: Text cuts
// none of it at max.
func fits(l Lead, max int) bool { return fitsText(l.str(), max) }

// fitsText reports whether Text cuts none of s at max.
func fitsText(s string, max int) bool { return utf8.RuneCountInString(s) <= max }

// allFit reports whether every one of ls fits at max.
func allFit(ls []Lead, max int) bool {
	for _, l := range ls {
		if !fits(l, max) {
			return false
		}
	}
	return true
}

// rowsWhole reports whether every path of rows is shown whole.
func rowsWhole(rows []PathRow) bool {
	for _, r := range rows {
		if !fits(r.Path, maxPath) {
			return false
		}
	}
	return true
}

// wholeOf says which parts of c the Card page shows whole, with the caps
// the template cuts them at.
func wholeOf(c Card) Whole {
	w := AllWhole
	for _, n := range []string{c.Project, c.Role, c.Verb, c.Feature, c.Target, c.Class} {
		w.Names = w.Names && fitsText(n, maxName)
	}
	w.Plan = allFit(c.Plan, maxText)
	w.Note = allFit(c.Note, maxText)
	if c.Diff != nil {
		w.Rows = rowsWhole(c.Diff.Rows)
	}
	for _, l := range c.Lists {
		w.Lists = w.Lists && allFit(l.Paths, maxPath)
	}
	for _, r := range c.Refs {
		w.Refs = w.Refs && fits(r.Ref, maxPath) && (r.MergedInto == nil || fits(r.MergedInto.Ref, maxPath))
	}
	for _, f := range c.Flagged {
		w.Flagged = w.Flagged && fits(f.Path, maxPath) && fits(f.Pattern, maxPath)
	}
	if d := c.Differs; d != nil {
		w.Differs = allFit(d.PathsAdded, maxPath) && allFit(d.PathsRemoved, maxPath) &&
			allFit(d.RefsAdded, maxPath) && allFit(d.RefsRemoved, maxPath)
	}
	return w
}

// funcs are the template functions. The pages show text that the app did not
// write only through text (a string) and lead (a [Lead]), both of which are
// [Text]; the rest format the app's own values. A test holds every template
// to that.
var funcs = template.FuncMap{
	"text":      Text,
	"lead":      func(l Lead, max int) string { return Text(l.str(), max) },
	"when":      when,
	"until":     until,
	"num":       num,
	"short":     short,
	"cardHref":  cardHref,
	"pathsHref": pathsHref,
	"maxName":   func() int { return maxName },
	"maxID":     func() int { return maxID },
	"maxLine":   func() int { return maxLine },
	"maxPath":   func() int { return maxPath },
	"maxText":   func() int { return maxText },
}

// when writes a time as the phone shows it, in UTC to the minute.
func when(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format("2006-01-02 15:04") + " UTC"
}

// until writes how long until t, from now.
func until(now, t time.Time) string {
	d := t.Sub(now)
	switch {
	case t.IsZero():
		return "unknown"
	case d <= 0:
		return "ended"
	case d < time.Minute:
		return "in less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("in %d min", int(d/time.Minute))
	}
	if m := int(d % time.Hour / time.Minute); m != 0 {
		return fmt.Sprintf("in %d h %d min", int(d/time.Hour), m)
	}
	return fmt.Sprintf("in %d h", int(d/time.Hour))
}

// short writes the first 12 characters of an object id or a digest, the
// host's, through Text.
func short(s string) string {
	if r := []rune(s); len(r) > 12 {
		s = string(r[:12])
	}
	if s == "" {
		return "none"
	}
	return Text(s, 12)
}

// num writes a whole number.
func num(v any) string {
	switch n := v.(type) {
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	}
	return "?"
}

// rpName is the relying party name shown by the phone's passkey dialog.
const rpName = "Authority seat"

// pages holds one parsed template set per page. A page that does not parse is
// a bug of the build, so the failure is a panic at start.
var pages = func() map[string]*template.Template {
	m := map[string]*template.Template{}
	for _, name := range pageNames {
		t := template.New("layout.html").Funcs(funcs)
		template.Must(t.ParseFS(templateFS, "templates/layout.html", "templates/parts.html", "templates/"+name+".html"))
		m[name] = t
	}
	return m
}()

// page returns the handler of a page: the template is executed into a buffer
// first so that a failure sends nothing half-written.
func (a *app) page(name string) http.HandlerFunc {
	data := pageData{Title: pageTitles[name], RPID: a.cfg.RPID, RPName: rpName}
	return func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if err := pages[name].Execute(&buf, data); err != nil {
			writeText(w, http.StatusInternalServerError, "internal error\n")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(buf.Bytes())
	}
}

// render answers a page made from data, with status, and reports whether
// the page was written: executed, and handed whole to the connection
// (written and flushed without an error). The template is executed into a
// buffer first, so that a failure sends nothing half-written. What a page
// counts for (a read of the card, a page of its path list) is noted only
// once render reported it written.
func (a *app) render(w http.ResponseWriter, name string, status int, data any) bool {
	var buf bytes.Buffer
	if err := pages[name].Execute(&buf, data); err != nil {
		writeText(w, http.StatusInternalServerError, "internal error\n")
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil {
		return false
	}
	// A writer that cannot flush is taken at its Write.
	if err := http.NewResponseController(w).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	return true
}

// passkeyPage is page for a page that asks the phone for a passkey assertion:
// its Permissions-Policy allows publickey-credentials-get to the app's own
// origin. Every other response forbids it.
func (a *app) passkeyPage(name string) http.HandlerFunc {
	h := a.page(name)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Permissions-Policy", permissionsPolicy("(self)"))
		h(w, r)
	}
}

// asset returns the handler of one embedded file.
func (a *app) asset(path, contentType string) http.HandlerFunc {
	body, err := assetFS.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Write(body)
	}
}
