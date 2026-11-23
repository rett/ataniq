package seatapp

import (
	"regexp"
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
		`data-rp-id="seat.example"`, "<script src=\"/static/words.js\"></script>\n<script src=\"/static/pair.js\"></script>", `id="compare"`, `id="mismatch"`, `id="start"`,
		"Compare every word, in order, with the desk console, then confirm there. Nothing is trusted until you confirm at the desk.",
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
		`name: "owner"`, "SeatWords.fingerprint(cred.rawId, key)", "SeatWords.count", "mismatch", "/api/pair-challenge", "/api/pair", "client_data_json", "credential_id",
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

// TestPageShowsItsOwnWordsAndWarnsOnADifference checks the structure the
// script relies on: the comparison prompt and the warning are separate
// elements, the warning is hidden until the script shows it, and the script
// loads the word list before it runs. What the script does with them needs a
// real browser.
func TestPageShowsItsOwnWordsAndWarnsOnADifference(t *testing.T) {
	r := newRig(t)
	page := r.do("GET", "/pair/", nil, nil).Body.String()
	if !regexp.MustCompile(`(?s)<p id="mismatch"[^>]* hidden>[^<]*do not match[^<]*</p>`).MatchString(page) {
		t.Error("the page lacks the hidden mismatch warning")
	}
	if strings.Index(page, "/static/words.js") > strings.Index(page, "/static/pair.js") || !strings.Contains(page, "/static/words.js") {
		t.Error("words.js must load before pair.js")
	}
	js := r.do("GET", "/static/pair.js", nil, nil).Body.String()
	for _, want := range []string{`getElementById("mismatch")`, `getElementById("compare")`, "mismatch.hidden", "compare.hidden"} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
}

// TestScriptsMeetTrustedTypes: the policy requires Trusted Types for script
// sinks, so the scripts may reach no sink that takes markup, a script address
// or code: they set text with textContent and nothing else. (This is a check
// of the source; that the browser accepts the page needs a real browser.)
func TestScriptsMeetTrustedTypes(t *testing.T) {
	r := newRig(t)
	csp := r.do("GET", "/pair/", nil, nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "require-trusted-types-for 'script'") {
		t.Fatalf("CSP %q", csp)
	}
	sinks := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|createContextualFragment|srcdoc|\.setAttribute\(|\.src\s*=|\.href\s*=|\.action\s*=|new Function|eval\(|setTimeout\(\s*["']|setInterval\(\s*["']|importScripts|createElement\(\s*["']script|DOMParser|\.text\s*=|\.innerText`)
	for _, path := range []string{"/static/pair.js", "/static/words.js"} {
		js := r.do("GET", path, nil, nil).Body.String()
		if m := sinks.FindString(js); m != "" {
			t.Errorf("%s reaches the sink %q", path, m)
		}
	}
}

// TestScriptMessagesTellWhatToDo checks which failures the script lets the
// owner retry and what it says: a failure before the daemon was asked leaves
// the code unused, so the same page can try again, and a refusal of the
// site's name is not a cancellation.
func TestScriptMessagesTellWhatToDo(t *testing.T) {
	r := newRig(t)
	js := r.do("GET", "/static/pair.js", nil, nil).Body.String()
	for _, want := range []string{
		`user_verification_required: ["Set a screen lock on this phone, then try again.", true]`,
		`key_invalid: [`, `credential_invalid: [`, `client_data_invalid: [`, `authenticator_data_invalid: [`, `bad_request: [`,
		`"The browser refused this site's name for a passkey.`,
		`e.name === "SecurityError"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	// Each of the pre-daemon codes is retryable: its entry ends in true.
	for _, code := range []string{"key_invalid", "credential_invalid", "client_data_invalid", "authenticator_data_invalid", "bad_request"} {
		m := regexp.MustCompile(code + `: \[".*", (true|false)\]`).FindStringSubmatch(js)
		if m == nil || m[1] != "true" {
			t.Errorf("%s is not retryable in the script", code)
		}
	}
	// A refusal of the site's name and a cancellation are two messages.
	re := regexp.MustCompile(`(?m)^\s*(\w+): \["(.*)", (true|false)\],?$`)
	texts := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(js, -1) {
		texts[m[1]] = m[2]
	}
	if texts["site_refused"] == "" || texts["cancelled"] == "" || texts["site_refused"] == texts["cancelled"] {
		t.Errorf("site_refused %q, cancelled %q", texts["site_refused"], texts["cancelled"])
	}
	// With script off, the page says to close the tab so the code leaves the
	// address bar.
	page := r.do("GET", "/pair/", nil, nil).Body.String()
	if !regexp.MustCompile(`(?s)<noscript>.*[Cc]lose this tab.*address bar.*</noscript>`).MatchString(page) {
		t.Error("the noscript text does not say to close the tab")
	}
}
