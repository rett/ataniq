package seatapp

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
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
var pageNames = []string{"home", "pair", "login"}

var pageTitles = map[string]string{
	"home":  "Authority seat",
	"pair":  "Pair this phone",
	"login": "Log in",
}

// rpName is the relying party name shown by the phone's passkey dialog.
const rpName = "Authority seat"

// pages holds one parsed template set per page. A page that does not parse is
// a bug of the build, so the failure is a panic at start.
var pages = func() map[string]*template.Template {
	m := map[string]*template.Template{}
	for _, name := range pageNames {
		t := template.New("layout.html").Funcs(template.FuncMap{"text": Text})
		template.Must(t.ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html"))
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
