package seatapp

import (
	"strings"
	"testing"
)

func TestPairPageAndScript(t *testing.T) {
	r := newRig(t)
	page := r.do("GET", "/pair", nil, nil).Body.String()
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
	if q := r.do("GET", "/pair?code=SECRET", nil, nil).Body.String(); strings.Contains(q, "SECRET") {
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
