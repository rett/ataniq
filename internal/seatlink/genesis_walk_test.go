package seatlink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// genesisBrowser is a phone logged in to a seat app whose link reaches a gate
// in front of a real thread store under catalog v1.
func genesisBrowser(t *testing.T) *browser {
	t.Helper()
	seat := newIdentity(t, "seat")
	store := newStoreDesk(t)
	var desk *fakeDesk
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"pair", "read", "login", "genesis"},
		func(ctx context.Context, peer seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
			switch op {
			case OpLoginChallenge, OpLogin:
				return desk.handle(ctx, peer, op, params)
			case "owner.list":
				return ownerwire.EncodeListing(ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}})
			}
			return store.handle(ctx, peer, op, params)
		})
	desk = newFakeDesk(g.id.Fingerprint())
	phone, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	desk.pair(t, phone)
	br := newBrowser(seatApp(t, dialPinned(t, g, seat)))
	if w := br.loginWith(t, phone); w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	return br
}

var (
	pageRound  = regexp.MustCompile(`<input type="hidden" id="round" name="round" value="([0-9]+)">`)
	pageNode   = regexp.MustCompile(`<input type="hidden" id="n([0-2])" name="n[0-2]" value="([^"]+)">`)
	pageOption = regexp.MustCompile(`name="([am])([0-2])" value="([^"~][^"]*)"`)
)

// formOf reads a question page as the owner does: its round, and each
// question with its options. It answers the kind and the sub-kind as given,
// every other question with its first option, and leaves the question blank
// names unanswered. It returns the form to post and the nodes the page asks,
// or a nil form for a page that is not a round.
func formOf(t *testing.T, page, kind, subKind, blank string) (url.Values, []string) {
	t.Helper()
	rm := pageRound.FindStringSubmatch(page)
	if rm == nil {
		return nil, nil
	}
	form := url.Values{"round": {rm[1]}}
	var nodes []string
	opts := map[string][]string{}
	name := map[string]string{}
	for _, m := range pageOption.FindAllStringSubmatch(page, -1) {
		opts[m[2]] = append(opts[m[2]], m[3])
		name[m[2]] = m[1]
	}
	for _, m := range pageNode.FindAllStringSubmatch(page, -1) {
		i, node := m[1], m[2]
		nodes = append(nodes, node)
		form.Set("n"+i, node)
		if node == blank {
			continue
		}
		choice := ""
		if len(opts[i]) > 0 {
			choice = opts[i][0]
		}
		switch {
		case node == "kind":
			choice = kind
		case strings.HasSuffix(node, ".sub_kind"):
			choice = subKind
		}
		if !slices.Contains(opts[i], choice) {
			t.Fatalf("the page does not offer %q for %s:\n%s", choice, node, page)
		}
		form.Set(name[i]+i, choice)
	}
	return form, nodes
}

// Interviews on the pages, led by the real walker: the phone reads each
// question page and answers what it asks, through the seat app, the link, the
// gate and a real thread store, until the summary, which links the draft plan;
// finishing keeps the plan as revision 1. The web app interview leaves the
// goal unanswered in round 2, as the phone trial did: it then ended at round 6
// with 13 of 14 answers and no plan, the review points never asked.
func TestSeatLinkGenesisWalkerLedInterviews(t *testing.T) {
	for _, p := range []struct{ name, kind, subKind, blank, title string }{
		{"web app, the goal left in round 2", "software", "web_app", "goal", "A full web app"},
		{"newsletter", "publishing", "newsletter", "", "A newsletter"},
		{"podcast", "media", "podcast", "", "A podcast"},
	} {
		t.Run(p.name, func(t *testing.T) {
			br := genesisBrowser(t)
			w := br.postForm("/projects/new", nil)
			thread := strings.TrimPrefix(w.Header().Get("Location"), "/projects/")
			if w.Code != http.StatusSeeOther || !seatapp.ValidThreadID(thread) {
				t.Fatalf("the start: %d to %q", w.Code, w.Header().Get("Location"))
			}
			blank := p.blank
			var asked [][]string
			for {
				page := br.do("GET", "/projects/"+thread, "")
				form, nodes := formOf(t, page.Body.String(), p.kind, p.subKind, blank)
				if form == nil {
					break
				}
				round := len(asked) + 1
				if page.Code != 200 || form.Get("round") != strconv.Itoa(round) || round > 6 || len(nodes) < 1 || len(nodes) > 3 ||
					!strings.Contains(page.Body.String(), "Round "+strconv.Itoa(round)+" of 6.") {
					t.Fatalf("round %d's page (%d), after %v:\n%s", round, page.Code, asked, page.Body)
				}
				if slices.Contains(nodes, blank) {
					blank = "" // left once, answered when asked again
				}
				asked = append(asked, nodes)
				if w := br.postForm("/projects/"+thread+"/answer", form); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/projects/"+thread {
					t.Fatalf("round %d's answer: %d to %q", round, w.Code, w.Header().Get("Location"))
				}
			}
			if p.blank != "" && (len(asked) < 3 || !slices.Contains(asked[2], p.blank)) {
				t.Fatalf("the question left in round 2 was not asked in round 3: %v", asked)
			}
			sum := br.do("GET", "/projects/"+thread, "").Body.String()
			if !strings.Contains(sum, "14 of 14 required answers given.") ||
				!strings.Contains(sum, `<a id="see-plan" href="/projects/`+thread+`/plan">See the draft plan</a>`) {
				t.Fatalf("the summary after %v:\n%s", asked, sum)
			}
			if w := br.postForm("/projects/"+thread+"/finish", nil); w.Code != http.StatusSeeOther {
				t.Fatalf("the finish: %d", w.Code)
			}
			fin := br.do("GET", "/projects/"+thread, "").Body.String()
			if !strings.Contains(fin, `<a id="see-plan" href="/projects/`+thread+`/plan">See the plan</a>`) {
				t.Fatalf("the finished page:\n%s", fin)
			}
			kept := br.do("GET", "/projects/"+thread+"/plan", "").Body.String()
			if !strings.Contains(kept, "<h1>Plan, revision 1</h1>") || !strings.Contains(kept, p.title) {
				t.Fatalf("the kept plan's page:\n%s", kept)
			}
		})
	}
}
