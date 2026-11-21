package leadctl

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadwire"
)

// constStrings returns the string value of every constant of the Go file whose
// name has the prefix, read from the source so that a constant added later is
// held too. The files read here carry build tags, which the parser ignores.
func constStrings(t *testing.T, path, prefix string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(path), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if i >= len(vs.Values) || !strings.HasPrefix(n.Name, prefix) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// callCodes returns the string literal that is the first argument of every
// call of the named functions in the Go file: the daemon's canary core makes
// some of its codes inline, as refusal("not-running", ...).
func callCodes(t *testing.T, path string, funcs ...string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(path), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || len(c.Args) == 0 {
			return true
		}
		id, ok := c.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		for _, name := range funcs {
			if id.Name != name {
				continue
			}
			if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

// daemonCodes is every reason code a receipt of the daemon can carry, with
// the places they are read from: the constants of the lead extension, the
// canary core's inline codes, the message admission's codes, the holder's
// refusals the daemon prefixes with "holder-", the code a restart gives, and
// bad-message, the one code the canary core makes in a variable (for a
// message refused with an error that is not an admission's).
func daemonCodes(t *testing.T) []string {
	t.Helper()
	const dir = "../../leaddaemon/"
	var all []string
	all = append(all, leadwire.ReasonCodes()...)
	all = append(all, constStrings(t, dir+"lead_ext_linux.go", "code")...)
	all = append(all, constStrings(t, dir+"canary_ext_linux.go", "code")...)
	all = append(all, callCodes(t, dir+"canary_ext_linux.go", "refusal", "failure")...)
	all = append(all, constStrings(t, "../../leadcanary/admission.go", "Code")...)
	for _, c := range constStrings(t, "../../leadholder/wire/control.go", "Code") {
		all = append(all, "holder-"+c)
	}
	all = append(all, constStrings(t, "../../leadcontrol/table.go", "CodeInterrupted")...)
	all = append(all, "bad-message")
	sort.Strings(all)
	out := all[:0]
	for i, c := range all {
		if i == 0 || c != all[i-1] {
			out = append(out, c)
		}
	}
	if len(out) < 50 {
		t.Fatalf("read %d codes: the daemon's sources were not read", len(out))
	}
	return out
}

func TestEveryReasonCodeHasWords(t *testing.T) {
	for _, c := range daemonCodes(t) {
		if _, ok := reasonWords[c]; !ok {
			t.Errorf("the daemon sends the code %q and the view has no words for it", c)
		}
	}
	// The codes of leadwire's error answers, and of leadwire's own list.
	for _, c := range append(leadwire.ErrorCodes(), leadwire.ReasonCodes()...) {
		if _, ok := reasonWords[c]; !ok {
			t.Errorf("leadwire names the code %q and the view has no words for it", c)
		}
	}
}

// TestNoWordsForACodeNothingSends holds that every code of the table is one
// that is sent: by the daemon, by leadwire's error answers or by the
// console's adapter (not-listed, conflict and invalid-request, which it makes
// itself from an error of the client; see refusedCode in cmd/ataniq-console).
func TestNoWordsForACodeNothingSends(t *testing.T) {
	sent := map[string]bool{"not-listed": true, "conflict": true, "invalid-request": true}
	for _, c := range daemonCodes(t) {
		sent[c] = true
	}
	for _, c := range leadwire.ErrorCodes() {
		sent[c] = true
	}
	for c := range reasonWords {
		if !sent[c] {
			t.Errorf("the view has words for the code %q and nothing sends it", c)
		}
	}
}

// TestReasonWordsAreShortASCII holds that each text is plain printable ASCII
// of one line and short enough for a line of the view.
func TestReasonWordsAreShortASCII(t *testing.T) {
	for c, w := range reasonWords {
		if w == "" || len(w) > 80 {
			t.Errorf("%s: %d bytes: %q", c, len(w), w)
		}
		for i := 0; i < len(w); i++ {
			if w[i] < 0x20 || w[i] > 0x7e {
				t.Errorf("%s: %q is not printable ASCII", c, w)
				break
			}
		}
		if len(c) > 64 || safeID(c, 64) != c {
			t.Errorf("the code %q is not a short code", c)
		}
	}
}

// A holder that says it is busy is delivering something the daemon has no
// record of, and the daemon keeps the epoch's message spent after it, so the
// words of that refusal must not say the epoch is free or that nothing was sent
// at all, and the other holder refusals do not say the epoch is spent.
func TestTheHolderBusyWordsSayTheEpochsMessageIsSpent(t *testing.T) {
	busy := reasonWords["holder-busy"]
	if !strings.Contains(busy, "spent") || strings.Contains(busy, "free") || strings.Contains(busy, "nothing was written") {
		t.Errorf("holder-busy: %q", busy)
	}
	for _, c := range []string{"holder-disabled", "holder-wrong-epoch", "holder-bad-request"} {
		if strings.Contains(reasonWords[c], "spent") {
			t.Errorf("%s: %q", c, reasonWords[c])
		}
	}
}

// constNames returns, for each constant of the Go file whose value is a
// string literal, its name and its value, read from the source.
func constNames(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(path), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					out[n.Name] = s
				}
			}
		}
	}
	return out
}

// holderResults reads the combinations of outcome, reason and written that
// the holder's wire carries: the validInject table of the holder's wire
// package, and its sets of words. The file carries no tag, and is read as
// source so that a combination added later is held too.
func holderResults(t *testing.T) (combos []deliveryKey, outcomes, reasons, writtens []string) {
	t.Helper()
	const path = "../../leadholder/wire/inject.go"
	names := constNames(t, path)
	f, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(path), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for n, v := range names {
		switch {
		case strings.HasPrefix(n, "InjectOutcome"):
			outcomes = append(outcomes, v)
		case strings.HasPrefix(n, "InjectReason") && v != "":
			reasons = append(reasons, v)
		case strings.HasPrefix(n, "InjectWritten"):
			writtens = append(writtens, v)
		}
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			if len(vs.Names) != 1 || vs.Names[0].Name != "validInject" || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatal("validInject is not a composite literal")
			}
			for _, e := range lit.Elts {
				el, ok := e.(*ast.CompositeLit)
				if !ok || len(el.Elts) != 3 {
					t.Fatal("a validInject entry is not three positional words")
				}
				var w [3]string
				for i, x := range el.Elts {
					id, ok := x.(*ast.Ident)
					if !ok {
						t.Fatal("a validInject word is not a constant")
					}
					v, ok := names[id.Name]
					if !ok {
						t.Fatalf("validInject names %s, which is not a constant of its file", id.Name)
					}
					w[i] = v
				}
				combos = append(combos, deliveryKey{w[0], w[1], w[2]})
			}
		}
	}
	if len(combos) < 10 || len(outcomes) != 4 || len(reasons) != 6 || len(writtens) != 4 {
		t.Fatalf("read %d combinations, %d outcomes, %d reasons, %d amounts: the holder's wire was not read",
			len(combos), len(outcomes), len(reasons), len(writtens))
	}
	return combos, outcomes, reasons, writtens
}

// TestEveryResultTheHolderCanSendHasWords holds that each valid combination
// of outcome, reason and written has its own fixed text, that the view's
// constants for the holder's words are the holder's, and that the table has
// no text for a combination the holder does not send.
func TestEveryResultTheHolderCanSendHasWords(t *testing.T) {
	combos, outcomes, reasons, writtens := holderResults(t)
	have := map[deliveryKey]bool{}
	for _, c := range combos {
		have[c] = true
		tx, ok := deliveryWords[c]
		if !ok {
			t.Errorf("the holder sends %q, %q, %q and the view has no words for it", c.outcome, c.reason, c.written)
			continue
		}
		if tx.fate == fateUnknown || tx.text == "" {
			t.Errorf("%v: the words say nothing of what happened to the message", c)
		}
		if got := deliveryOf(c.outcome, c.reason, c.written); got != tx {
			t.Errorf("%v: looked up as %v", c, got)
		}
		// What the words claim agrees with the holder's own: nothing is
		// said of a message that was not written unless the holder says
		// nothing was.
		switch tx.fate {
		case fateNotDelivered:
			if c.written != WrittenNothing || !strings.Contains(tx.text, "nothing was written") {
				t.Errorf("%v: drawn as not delivered", c)
			}
		case fateDelivered:
			if c.outcome != OutcomeSubmitted || c.written != WrittenWhole {
				t.Errorf("%v: drawn as delivered", c)
			}
		}
	}
	for c := range deliveryWords {
		if !have[c] {
			t.Errorf("the view has words for %q, %q, %q and the holder does not send it", c.outcome, c.reason, c.written)
		}
	}
	// The words of an outcome alone, for an adapter that passes no reason.
	for _, o := range outcomes {
		if _, ok := outcomeOnlyWords[o]; !ok {
			t.Errorf("the outcome %q has no words of its own", o)
		}
	}
	for o := range outcomeOnlyWords {
		if !slices.Contains(outcomes, o) {
			t.Errorf("the view has words for the outcome %q and the holder does not send it", o)
		}
	}
	// The exported words are the holder's.
	for want, got := range map[string][]string{
		"outcome": {OutcomeSubmitted, OutcomeNotNow, OutcomeRefused, OutcomeUncertain},
		"reason":  {ReasonNotReady, ReasonBlocked, ReasonUntested, ReasonText, ReasonNotSubmitted, ReasonDied},
		"written": {WrittenNothing, WrittenPartial, WrittenWhole, WrittenUnknown},
	} {
		src := map[string][]string{"outcome": outcomes, "reason": reasons, "written": writtens}[want]
		a, b := slices.Clone(src), slices.Clone(got)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			t.Errorf("the view's %s words are %v, the holder's are %v", want, b, a)
		}
	}
}

// TestResultWordsAreShortASCIIAndSayWhetherTheEpochIsSpent holds that each text
// is plain printable ASCII on one line, and that every result the view has words
// for says what the daemon does with the epoch's message: it is free again after
// a message that was not delivered, and spent after any other.
func TestResultWordsAreShortASCIIAndSayWhetherTheEpochIsSpent(t *testing.T) {
	check := func(name string, tx deliveryText) {
		if tx.text == "" || len(tx.text) > 130 {
			t.Errorf("%s: %d bytes", name, len(tx.text))
		}
		for i := 0; i < len(tx.text); i++ {
			if tx.text[i] < 0x20 || tx.text[i] > 0x7e {
				t.Errorf("%s: %q is not printable ASCII", name, tx.text)
				break
			}
		}
	}
	for c, tx := range deliveryWords {
		check(c.outcome+"/"+c.reason+"/"+c.written, tx)
		r := Receipt{Op: OpInject, Status: Done, Outcome: c.outcome, Reason: c.reason, Written: c.written}
		if tx.fate == fateNotDelivered {
			// Free only when the row says so; spent when it says that; and
			// neither claimed when no row has been read since the answer.
			if line := receiptLine(r, heldFree); !strings.Contains(line, "The epoch's message is free") || strings.Contains(line, "spent") {
				t.Errorf("%v: %q does not say the epoch's message is free", c, line)
			}
			if line := receiptLine(r, heldSpent); !strings.Contains(line, "holds this epoch's message spent") || strings.Contains(line, "is free") {
				t.Errorf("%v: %q does not say the daemon holds the epoch's message spent", c, line)
			}
			if line := receiptLine(r, heldUnknown); !strings.Contains(line, "free unless the list says it is spent") {
				t.Errorf("%v: %q does not leave the list to say", c, line)
			}
			continue
		}
		for _, h := range []held{heldUnknown, heldFree, heldSpent} {
			if line := receiptLine(r, h); !strings.Contains(line, "This epoch's one message is spent") {
				t.Errorf("%v: %q does not say the epoch's message is spent", c, line)
			}
		}
	}
	for o, tx := range outcomeOnlyWords {
		check(o, tx)
	}
}

// TestTheDaemonSpendsTheMessageWhenTheRequestIsWrittenUnlessItWasNotDelivered
// holds the sentences the view says of every done inject: the daemon marks the
// epoch's message spent once the request is written to the holder, whatever the
// holder answers, except that a lead of scratch mode whose message the holder
// answered as not delivered (and whose result line is in the evidence) keeps it
// free. It reads the daemon's source; a change there is a change of the view's
// words.
func TestTheDaemonSpendsTheMessageWhenTheRequestIsWrittenUnlessItWasNotDelivered(t *testing.T) {
	src, err := os.ReadFile(filepath.FromSlash("../../leaddaemon/canary_ext_linux.go"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{
		"resp, wrote, err := callHolderThen(",
		"\t\tif wrote && !release {\n\t\t\tx.spent[epoch] = true\n\t\t}",
		"x.scratch && leadcanary.ReleasesMessage(result)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the daemon's source no longer holds %q: check what it spends and change the view's words", want)
		}
	}
}

// A lead left unknown by a launch that was never settled: the list never
// settles it, so the words say what does (a daemon restart after the launch
// window) and not "read the list".
func TestAnUnsettledLaunchSaysWhatSettlesIt(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[1].State, b.listing.Leads[1].StateReason = "unknown", "launch-unsettled"
	b.listing.Leads[1].Epoch = 1
	b.answer = func(c call) (Receipt, error) {
		return Receipt{CommandID: "x1", Op: Op(c.op), Status: Unavailable, Code: "intent-pending"}, nil
	}
	r := newRig(t, b, 150, 30)
	r.key("j")
	// The row says it before any command is sent.
	r.seen("beta: unknown (launch-unsettled): launch not settled; a daemon restart after the 40 s launch window settles it")
	r.key("x")
	r.key("y")
	r.seen("stop: unavailable: launch not settled; a daemon restart after the 40 s launch window settles it")
	r.notSeen("read the list")
	r.key("s")
	r.seen("start: unavailable: launch not settled; a daemon restart after the 40 s launch window settles it")
}

func TestAMaxRunningRefusalNamesAnUnknownLeadThatHoldsASlot(t *testing.T) {
	refuse := func(call) (Receipt, error) {
		return Receipt{CommandID: "x1", Op: OpStart, Status: Refused, Code: "max-running"}, nil
	}
	// With a lead unknown in the list the refusal says what holds the slot.
	b := twoLeads(t)
	b.listing.Leads[1].State, b.listing.Leads[1].StateReason = "unknown", "launch-unsettled"
	b.answer = refuse
	r := newRig(t, b, 220, 30)
	r.key("s")
	r.seen("start: refused: the limit of running leads is reached. An unknown lead holds a slot",
		"only a daemon restart after the 40 s launch window does")
	// Without one the refusal is the plain line.
	b = twoLeads(t)
	b.answer = refuse
	r = newRig(t, b, 220, 30)
	r.key("s")
	r.seen("start: refused: the limit of running leads is reached")
	r.notSeen("unknown lead", "launch window")
	// Another refusal is left alone even with an unknown lead listed.
	b = twoLeads(t)
	b.listing.Leads[1].State = "unknown"
	b.answer = func(call) (Receipt, error) {
		return Receipt{CommandID: "x1", Op: OpStart, Status: Refused, Code: "role-busy"}, nil
	}
	r = newRig(t, b, 220, 30)
	r.key("s")
	r.notSeen("An unknown lead")
}

// The receipt of a refused ticket carries only ticket-refused (the daemon logs
// the holder's code), so the words say that a writer may already be attached
// without claiming it is.
func TestARefusedTicketSaysAWriterMayAlreadyBeAttached(t *testing.T) {
	for _, key := range []string{"w", "v"} {
		b := twoLeads(t)
		b.answer = func(call) (Receipt, error) {
			return Receipt{CommandID: "t1", Op: OpTicket, Status: Refused, Code: "ticket-refused"}, nil
		}
		r := newRig(t, b, 150, 30)
		r.key("j")
		r.key(key)
		r.seen("attach ticket: refused: the holder refused the ticket; a writer may already be attached")
		if len(r.actions) != 0 {
			t.Fatalf("%s attached: %+v", key, r.actions)
		}
	}
}
