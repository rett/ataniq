//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
)

// ownerDocBans finds the sentences of a package comment that keep approvals
// for another protocol name: a sentence that speaks of approvals and also says
// that a later version, another protocol name or a new one is where they
// belong. It returns the offending sentences.
func ownerDocBans(doc string) []string {
	// A package comment is lines of "// text"; join them and split on the
	// end of a sentence.
	var text strings.Builder
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))
		text.WriteString(line + " ")
	}
	sentences := regexp.MustCompile(`[.!?]\s+`).Split(text.String(), -1)
	reserves := regexp.MustCompile(`(?i)(later version|another protocol|different protocol|new protocol|other protocol|protocol name)`)
	var bad []string
	for _, s := range sentences {
		if strings.Contains(strings.ToLower(s), "approval") && reserves.MatchString(s) {
			bad = append(bad, strings.TrimSpace(s))
		}
	}
	return bad
}

// TestOwnerTheOwnerOperationsRideTheClientProtocol: owner.* rides
// ataniq.client/1, and the package comment of the client protocol does not
// keep approvals for another protocol name. The test reads the comment (the
// sentence the canary slice rewrote) so that a later edit that reserves
// approvals again fails here.
func TestOwnerTheOwnerOperationsRideTheClientProtocol(t *testing.T) {
	b, err := os.ReadFile("../clientapi/doc.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	if bad := ownerDocBans(doc); len(bad) != 0 {
		t.Fatalf("internal/clientapi/doc.go keeps approvals for another protocol name:\n%s", strings.Join(bad, "\n"))
	}
	for _, want := range []string{`"ataniq.client/1"`, "extension"} {
		if !strings.Contains(doc, want) {
			t.Errorf("internal/clientapi/doc.go does not say %s", want)
		}
	}
	if clientapi.Protocol != "ataniq.client/1" {
		t.Fatalf("the protocol is %q", clientapi.Protocol)
	}
	// Every owner operation is served on that protocol: a request that names
	// it reaches the operation, and the operations are listed for it.
	r := newOwnerRig(t)
	c := r.dial()
	h, err := c.Hello(ownerCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Protocols) != 1 || h.Protocols[0] != "ataniq.client/1" {
		t.Fatalf("protocols %v", h.Protocols)
	}
	for _, op := range ownerwire.Operations() {
		if !strings.HasPrefix(op, "owner.") || !strings.Contains(strings.Join(h.Operations, " "), op) {
			t.Errorf("%s is not listed by hello", op)
		}
	}
}

// TestOwnerTheDocumentCheckCanFail: the check above fails on the sentence it
// was written to catch, in a few spellings, and passes on the comment as it is
// and on the sentences about other things. A check that cannot fail proves
// nothing.
func TestOwnerTheDocumentCheckCanFail(t *testing.T) {
	for _, bad := range []string{
		"// A later version that adds commands, approvals or events is a different protocol name.",
		"// Approvals need\n// a new protocol name.",
		"// Approvals belong to another protocol.",
		"// Approvals are for a later version of the protocol.",
	} {
		if len(ownerDocBans(bad)) == 0 {
			t.Errorf("not caught: %q", bad)
		}
	}
	for _, good := range []string{
		"// A server may register extension operations; hello then lists them. No release build registers one.",
		"// The owner connection's approvals ride this protocol as extension operations.",
		"// Version 1 has a protocol name. It speaks of nothing else.",
		"",
	} {
		if bad := ownerDocBans(good); len(bad) != 0 {
			t.Errorf("caught: %q", bad)
		}
	}
}
