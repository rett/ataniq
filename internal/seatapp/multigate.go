package seatapp

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
)

// Gate binds a display name and the fingerprint of a pinned gate to its port.
// Only the fingerprint is used to address objects and sessions.
type Gate struct {
	Name, Instance string
	Daemon         Daemon
}

var instanceID = regexp.MustCompile("^[0-9a-f]{64}$")

// OpenGates mounts independent seat apps at /g/{fingerprint}/ and serves
// their combined directories. Each gate authenticates its own browser session.
func OpenGates(cfg Config, gates []Gate) (*App, error) {
	if len(gates) == 0 || cfg.Daemon != nil || cfg.Notifier != nil {
		return nil, errors.New("seatapp: a gate directory needs gates and no default daemon or notifier")
	}
	d := &gateDirectory{}
	names := map[string]bool{}
	mux := http.NewServeMux()
	for _, g := range gates {
		if !instanceID.MatchString(g.Instance) || !chatKey.MatchString(g.Name) || names[g.Name] {
			return nil, errors.New("seatapp: invalid instance fingerprint or invalid or duplicate gate name")
		}
		for _, existing := range d.gates {
			if existing.g.Instance == g.Instance {
				return nil, errors.New("seatapp: duplicate gate fingerprint")
			}
		}
		names[g.Name] = true
		cfg.Daemon = g.Daemon
		a, h, err := newApp(cfg)
		if err != nil {
			return nil, err
		}
		a.mount = "/g/" + g.Instance
		a.cookieSuffix = "-" + g.Instance
		a.pages = pageTemplates(a.mount, g.Name)
		d.gates = append(d.gates, directoryGate{g, a})
		mux.Handle(a.mount+"/", http.StripPrefix(a.mount, h))
	}
	// Only static assets and the health probe are shared by the mounts.
	first := d.gates[0].app
	for _, route := range first.routes() {
		if strings.HasPrefix(route.pattern, "GET /static/") || route.pattern == "GET /healthz" {
			mux.HandleFunc(route.pattern, route.handler)
		}
	}
	for _, p := range []string{"/{$}", "/cards/{$}", "/projects/{$}", "/leads/{$}", "/login/{$}", "/pair/{$}"} {
		mux.HandleFunc("GET "+p, d.page)
	}
	mux.HandleFunc("GET /static/choose-gate.js", first.asset("assets/choose-gate.js", "text/javascript; charset=utf-8"))
	// ServeMux cleans paths with a redirect. Never let that change the
	// addressed authority (or turn an escaped slash into a separator).
	d.Handler = protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		clean := path.Clean(p)
		if r.URL.EscapedPath() != p || (p != clean && (clean == "/" || p != clean+"/")) {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	return &App{Handler: d, stop: func() {}}, nil
}

type directoryGate struct {
	g   Gate
	app *app
}
type gateDirectory struct {
	http.Handler
	gates []directoryGate
}

func (d *gateDirectory) endStreams() {
	for _, g := range d.gates {
		g.app.streams.end()
	}
}

// These reduced rows live only for the session that read them. They contain
// no transcript, decision frame or credential. Stale rows have no controls.
type directoryRow struct{ Path, ID, Project, State string }
type directoryMemory struct {
	mu   sync.Mutex
	rows map[string][]directoryRow
}
type instanceRows struct {
	Name, Mount, Instance, Status string
	Rows                          []directoryRow
	Stale, LoggedIn               bool
}

var directoryPage = template.Must(template.New("directory.html").Funcs(funcs).ParseFS(templateFS, "templates/directory.html"))

func (d *gateDirectory) page(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeText(w, http.StatusMethodNotAllowed, "method not allowed\n")
		return
	}
	section := r.URL.Path
	if section == "/" {
		section = cardsPath
	}
	data := struct {
		Section string
		Gates   []instanceRows
	}{section, make([]instanceRows, len(d.gates))}
	var reads sync.WaitGroup
	for i, g := range d.gates {
		data.Gates[i] = instanceRows{Name: g.g.Name, Mount: g.app.mount, Instance: g.g.Instance}
		if section == pairPath || section == loginPath {
			continue
		}
		reads.Go(func() { g.list(r, section, &data.Gates[i]) })
	}
	reads.Wait()
	// A sibling's read can outlast a logout or the session's expiry.
	for i, g := range d.gates {
		v := &data.Gates[i]
		if v.LoggedIn {
			_, _, live := g.app.sess.look(cookieValue(r, SessionCookie+g.app.cookieSuffix))
			if !live {
				v.LoggedIn = false
				v.Rows = nil
			}
		}
	}
	var b bytes.Buffer
	if err := directoryPage.Execute(&b, data); err != nil {
		writeText(w, 500, "internal error\n")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b.Bytes())
}

func (g directoryGate) list(r *http.Request, section string, out *instanceRows) {
	a := g.app
	s, ok := a.session(r)
	if !ok {
		return
	}
	out.LoggedIn = true
	m := s.directory
	if !m.mu.TryLock() {
		out.Status = "Refresh in progress"
		return
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), daemonTimeout)
	defer cancel()
	rows, err := a.directoryRows(ctx, section, s, r)
	if _, _, live := a.sess.look(cookieValue(r, SessionCookie+a.cookieSuffix)); !live {
		out.LoggedIn = false
		return
	}
	switch {
	case err == nil:
		m.rows[section] = rows
	case errors.Is(err, ErrDaemonUnavailable):
		out.Status, out.Stale = "Offline", true
	case errors.Is(err, ErrBusy):
		out.Status = "Busy"
		return
	default:
		delete(m.rows, section)
		out.Status = "Cannot list this instance"
		if errors.Is(err, ErrNotOffered) {
			out.Status = "Not offered"
		}
	}
	out.Rows = m.rows[section]
}

func (a *app) directoryRows(ctx context.Context, section string, s Session, r *http.Request) ([]directoryRow, error) {
	var rows []directoryRow
	switch section {
	case cardsPath:
		if !a.reads.allow(s.Credential, a.cfg.Now()) {
			return nil, ErrBusy
		}
		l, err := a.cfg.Daemon.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range l.Pending {
			rows = append(rows, directoryRow{a.mount + cardHref(item.ID), item.ID, item.Project, "Waiting"})
		}
		for _, item := range l.Recent {
			rows = append(rows, directoryRow{a.mount + cardHref(item.ID), item.ID, item.Project, item.State})
		}
	case projectsPath:
		g, ok := a.genesisPort()
		if !ok {
			return nil, ErrNotOffered
		}
		if !a.genesisAllow(s.Credential) {
			return nil, ErrBusy
		}
		v, err := g.Genesis(ctx, GenesisCall{Verb: GenesisVerbList})
		if err != nil {
			return nil, err
		}
		if v.State != GenesisStateThreads {
			return nil, errors.New("invalid project list")
		}
		for _, t := range openThreads(v.Threads) {
			rows = append(rows, directoryRow{a.mount + projectHref(t.Thread, ""), t.Thread, "Project", t.State})
		}
	case "/leads/":
		p, ok := a.cfg.Daemon.(Chat)
		if !ok {
			return nil, ErrNotOffered
		}
		if !a.chats.allow(s.Credential, a.cfg.Now()) {
			return nil, ErrBusy
		}
		_, gone, _ := a.sess.look(cookieValue(r, SessionCookie+a.cookieSuffix))
		c := ChatCall{Verb: ChatLeads, SessionDone: gone}
		v, err := p.Chat(ctx, c)
		if err != nil {
			return nil, err
		}
		if !v.ValidFor(c) {
			return nil, errChatInvalid
		}
		label := "Lead"
		if v.StandIn {
			label = "STAND-IN lead"
		}
		for _, l := range v.Rows {
			link := ""
			if l.Chat != "off" {
				link = a.mount + "/leads/daemon/" + strings.TrimPrefix(l.Lead, "daemon:")
			}
			state := l.State
			if l.Chat == "off" {
				state = "Chat off"
			}
			rows = append(rows, directoryRow{link, l.Lead, label, state})
		}
	}
	return rows, nil
}
