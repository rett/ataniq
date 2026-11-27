package seatapp

import (
	"net/http"
	"strings"
	"testing"
)

func TestMultiGateInstanceDirectory(t *testing.T) {
	left, right := newRig(t), newRig(t)
	host := func(r *rig) chatProjectDaemon {
		return chatProjectDaemon{genesisDaemon{r.daemon, &fakeGenesis{}}, sampleChat()}
	}
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	x, y := host(left), host(right)
	h, err := OpenGates(Config{Origin: testOrigin, RPID: testRPID}, []Gate{{"second", b, y}, {"first", a, x}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	r := &rig{h: h}
	login := func(id string) *http.Cookie {
		t.Helper()
		mount := "/g/" + id
		w := r.postWith(mount+"/api/login-challenge", "{}")
		c := cookieOf(w, LoginCookie+"-"+id)
		if c == nil {
			t.Fatalf("login challenge: %d %s", w.Code, w.Body)
		}
		w = r.postWith(mount+"/api/login", assertionBody(), c)
		s := cookieOf(w, SessionCookie+"-"+id)
		if s == nil {
			t.Fatalf("login: %d %s", w.Code, w.Body)
		}
		checkCookieFlags(t, "instance login", s)
		return s
	}
	al, bl := "/g/"+a, "/g/"+b
	one := login(a)
	if w := r.getWith(bl+cardsPath+testCardID, one); w.Code != 303 || w.Header().Get("Location") != bl+loginPath {
		t.Fatalf("cross-gate session: %d %s", w.Code, w.Body)
	}
	if w := r.getWith("/", one); !strings.Contains(w.Body.String(), al+cardsPath+testCardID) || strings.Contains(w.Body.String(), bl+cardsPath+testCardID) || right.daemon.reads != 0 {
		t.Fatal(w.Body)
	}
	two := login(b)
	if w := r.postWith(bl+"/api/login", assertionBody(), cookieOf(r.postWith(al+"/api/login-challenge", "{}"), LoginCookie+"-"+a)); w.Code == 200 || len(right.daemon.logins) != 1 {
		t.Fatal("a login challenge crossed instances")
	}
	for _, tc := range []struct{ section, detail string }{{"/", cardsPath + testCardID}, {projectsPath, projectsPath + testThread}, {"/leads/", "/leads/daemon/alpha"}} {
		w := r.getWith(tc.section, one, two)
		for _, m := range []string{al, bl} {
			if !strings.Contains(w.Body.String(), `href="`+m+tc.detail+`"`) {
				t.Fatalf("%s lacks %s: %s", tc.section, m+tc.detail, w.Body)
			}
		}
	}
	body := r.getWith(al+cardsPath+testCardID, one, two).Body.String()
	if len(left.daemon.shown) != 1 || len(right.daemon.shown) != 0 || !strings.Contains(body, `data-mount="`+al+`"`) || !strings.Contains(body, `href="`+al+cardsPath+`"`) {
		t.Fatalf("card routing: %s", body)
	}
	left.daemon.readErr = ErrDaemonUnavailable
	body = r.getWith("/", one, two).Body.String()
	if !strings.Contains(body, "Offline") || strings.Count(body, " · Stale") != 2 || strings.Contains(body, al+cardsPath+testCardID) || !strings.Contains(body, bl+cardsPath+testCardID) || strings.Count(body, "ataniq ·") != 4 {
		t.Fatalf("offline directory: %s", body)
	}
	x.g.errs = map[string]error{GenesisVerbList: ErrDaemonUnavailable}
	x.down = true
	for _, test := range []struct{ section, detail string }{{projectsPath, projectsPath + testThread}, {"/leads/", "/leads/daemon/alpha"}} {
		body = r.getWith(test.section, one, two).Body.String()
		if !strings.Contains(body, "Stale") || strings.Contains(body, al+test.detail) || !strings.Contains(body, bl+test.detail) {
			t.Fatalf("%s offline: %s", test.section, body)
		}
	}
	left.daemon.readErr = ErrNotOffered
	if body = r.getWith("/", one, two).Body.String(); !strings.Contains(body, "Not offered") || strings.Count(body, `data-instance="`+a+`"`) != 0 {
		t.Fatal("a refused list disclosed cached rows")
	}
	left.daemon.readErr = nil
	left.daemon.listing = Listing{}
	body = r.getWith("/", one, two).Body.String()
	if strings.Contains(body, "Stale") || strings.Contains(body, al+cardsPath+testCardID) || !strings.Contains(body, bl+cardsPath+testCardID) {
		t.Fatalf("recovery: %s", body)
	}
	r.postWith(bl+"/api/logout", "{}", one, two)
	if body = r.getWith("/", one, two).Body.String(); strings.Contains(body, testCardID) {
		t.Fatalf("logged out rows: %s", body)
	}
	for _, p := range []string{cardsPath + testCardID, "/g/unknown/cards/", al + "/../" + b + cardsPath, al + "%2fcards/"} {
		if w := r.getWith(p, one, two); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	w := r.getWith(pairPath)
	if !strings.Contains(w.Body.String(), `data-pair="`+al+pairPath+`"`) || !strings.Contains(w.Body.String(), `data-pair="`+bl+pairPath+`"`) {
		t.Fatal(w.Body)
	}
}

func TestMultiGateRefusesAmbiguousConfiguration(t *testing.T) {
	a := Gate{"one", strings.Repeat("a", 64), &fakeDaemon{}}
	b := Gate{"two", strings.Repeat("b", 64), &fakeDaemon{}}
	for _, gates := range [][]Gate{nil, {a, a}, {a, {a.Name, b.Instance, b.Daemon}}, {a, {b.Name, a.Instance, b.Daemon}}, {{"../gate", a.Instance, a.Daemon}}, {{a.Name, "short", a.Daemon}}, {{a.Name, a.Instance, nil}}} {
		if _, err := OpenGates(Config{Origin: testOrigin, RPID: testRPID}, gates); err == nil {
			t.Errorf("accepted %v", gates)
		}
	}
}
