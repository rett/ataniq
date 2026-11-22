package seatapp

import (
	"strings"
	"testing"
)

func TestPairPageAndScript(t *testing.T) {
	r := newRig(t)
	// The desk's QR opens /pair/ with the code in the fragment, so that exact
	// path is the page; /pair redirects to it.
	pw := r.do("GET", "/pair/", nil, nil)
	if pw.Code != 200 {
		t.Fatalf("GET /pair/: %d", pw.Code)
	}
	page := pw.Body.String()
	rd := r.do("GET", "/pair", nil, nil)
	if rd.Code != 308 || rd.Header().Get("Location") != "/pair/" {
		t.Errorf("GET /pair: %d, Location %q, want 308 /pair/", rd.Code, rd.Header().Get("Location"))
	}
	checkHeaders(t, "GET /pair", rd)
	for _, want := range []string{
		`data-rp-id="seat.example"`, `<script src="/static/pair.js"></script>`, `id="start"`,
		"Compare these six words with the desk console, then confirm there. Nothing is trusted until you confirm at the desk.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// The page is the same whatever the path or query holds: the code is in
	// the fragment, which the browser never sends.
	if q := r.do("GET", "/pair/?code=SECRET", nil, nil).Body.String(); strings.Contains(q, "SECRET") {
		t.Error("the query is echoed")
	}
	js := r.do("GET", "/static/pair.js", nil, nil).Body.String()
	for _, want := range []string{
		"location.hash", "history.replaceState", `userVerification: "required"`, `residentKey: "preferred"`,
		`attestation: "none"`, "alg: -7", "timeout: 120000", "getPublicKey()", "getAuthenticatorData()",
		`name: "owner"`, "/api/pair-challenge", "/api/pair", "client_data_json", "credential_id",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	for _, bad := range []string{"innerHTML", "outerHTML", "document.write", "eval(", "insertAdjacentHTML", "http://", "https://", "localStorage", "document.cookie"} {
		if strings.Contains(js, bad) {
			t.Errorf("the script holds %q", bad)
		}
	}
}

// TestScriptFetchPolicyKeepsTheOrigin pins the request policy of the script's
// POSTs. The browser rule (Fetch Standard, "append a request Origin header"):
// for a request that is neither GET nor HEAD and is not a CORS request, which
// a same-origin fetch is not, the Origin header is "null" when the request's
// referrer policy is no-referrer. The page's Referrer-Policy response header
// is no-referrer, which makes that the default policy of every request the
// page makes, and the app's exact Origin check (see TestPairWrongOriginHeader,
// which refuses "null") would then refuse the phone's own POST with a 403. So
// the shared fetch helper sets the policy itself: strict-origin keeps Origin
// on a same-origin request, and an empty referrer sends no Referer header.
// What a browser does with it cannot be run here: it needs a real browser.
func TestScriptFetchPolicyKeepsTheOrigin(t *testing.T) {
	r := newRig(t)
	js := r.do("GET", "/static/pair.js", nil, nil).Body.String()
	for _, want := range []string{`referrerPolicy: "strict-origin"`, `referrer: ""`} {
		if strings.Count(js, want) != 1 {
			t.Errorf("the script must hold %s once, in its one fetch helper", want)
		}
	}
	if strings.Count(js, "fetch(") != 1 {
		t.Errorf("the script holds %d fetch calls, want the one helper", strings.Count(js, "fetch("))
	}
	if strings.Contains(js, "no-referrer") {
		t.Error("the script names the no-referrer policy, which makes a same-origin POST carry Origin: null")
	}
	// The response header stays: the page itself leaks no referrer.
	if got := r.do("GET", "/pair/", nil, nil).Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy %q", got)
	}
}
