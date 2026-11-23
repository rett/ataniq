package seatapp

import (
	"regexp"
	"strings"
	"testing"
)

// TestSeatDecideScriptKeepsEveryOfferOffWhileStale: while the document is
// marked stale, no path of decide.js turns an offer control (the controls the
// page marks with data-offer) on: back() does not enable the confirm button
// when the page went stale at the confirm step, and refresh() keeps it off as
// it does Approve and Deny. The repository has no JavaScript engine, so this
// reads the text: every assignment to the disabled property of an offer
// control is true or follows stale().
func TestSeatDecideScriptKeepsEveryOfferOffWhileStale(t *testing.T) {
	r := newRig(t)
	dec := r.do("GET", "/static/decide.js", nil, nil).Body.String()
	assigned := 0
	for _, m := range regexp.MustCompile(`(\w+)\.disabled = ([^;]+);`).FindAllStringSubmatch(dec, -1) {
		ctl, val := m[1], m[2]
		switch ctl {
		case "cancelBtn":
			// Cancel is no offer: it carries no data-offer (checked below).
		case "approveBtn", "denyBtn", "goBtn":
			assigned++
			if val != "true" && !strings.Contains(val, "stale()") {
				t.Errorf("decide.js sets %s.disabled = %s, which may turn an offer on while stale", ctl, val)
			}
		default:
			t.Errorf("decide.js sets disabled on %s: this test names the controls it knows", ctl)
		}
	}
	if assigned < 4 {
		t.Errorf("decide.js assigns disabled of an offer %d times: this test needs to follow", assigned)
	}
	if !strings.Contains(dec, "goBtn.disabled = stale();") {
		t.Error("back() does not take the confirm button's state from stale()")
	}
	if !strings.Contains(dec, "if (stale()) { goBtn.disabled = true; }") {
		t.Error("refresh() does not keep the confirm button off on a stale page")
	}
	// Cancel carries no data-offer, so the live script never turns it off.
	r.daemon.card = sampleCard()
	page := r.getWith("/cards/"+testCardID, r.login(t)).Body.String()
	cancel := regexp.MustCompile(`<button id="confirm-cancel"[^>]*>`).FindString(page)
	if cancel == "" || strings.Contains(cancel, "data-offer") {
		t.Errorf("the cancel button is missing, or carries data-offer: %q", cancel)
	}
}
