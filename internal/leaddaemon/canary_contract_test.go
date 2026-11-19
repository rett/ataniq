//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
)

// The daemon layer of the canary slice's end-to-end tests (plan, section 6).
// The daemon runs with a canary config, the in-memory unit manager and a fake
// control server; the test drives define, start, ticket, inject and stop over
// the client socket, and compares the control requests the fake server
// received with the golden files that the holder layer replays
// (cmd/ataniq-holder/canary_e2e_test.go). The files and their README are in
// internal/leadholder/wire/testdata/canary.

// goldenDir is the folder of the golden requests.
const goldenDir = "../leadholder/wire/testdata/canary"

// The message of the golden inject request.
const (
	goldenMessageID = "golden-msg-1"
	goldenBody      = "canary-token: 0123456789abcdef-golden\n"
)

// maskedTicket is what the golden ticket request holds in place of the random
// ticket.
const maskedTicket = "0000000000000000000000000000000000000000000000000000000000000000"

// rawHolder is a fake control server of the holder that keeps the payload of
// each request frame as it came, besides the decoded request, and answers with
// answer.
type rawHolder struct {
	ln     net.Listener
	answer holderAnswer
	wg     sync.WaitGroup

	mu       sync.Mutex
	payloads [][]byte
	reqs     []wire.Request
}

func newRawHolder(t *testing.T, dir string, answer holderAnswer) *rawHolder {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(dir, controlSocketName))
	if err != nil {
		t.Fatal(err)
	}
	h := &rawHolder{ln: ln, answer: answer}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			h.wg.Add(1)
			go func() {
				defer h.wg.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(30 * time.Second))
				f, err := wire.NewReader(conn, wire.ControlRequests).Next()
				if err != nil {
					return
				}
				req, err := wire.DecodeRequest(f.Payload)
				if err != nil {
					return
				}
				h.mu.Lock()
				h.payloads = append(h.payloads, append([]byte(nil), f.Payload...))
				h.reqs = append(h.reqs, req)
				h.mu.Unlock()
				h.answer(req, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		h.wg.Wait()
	})
	return h
}

// seen returns the payloads and the decoded requests received so far.
func (h *rawHolder) seen() ([][]byte, []wire.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]byte(nil), h.payloads...), append([]wire.Request(nil), h.reqs...)
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCanaryContractGoldenRequests drives the whole flow of the slice and
// checks the registry states, the evidence lines and the exact bytes of the
// requests the holder was sent.
func TestCanaryContractGoldenRequests(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	holder := newRawHolder(t, c.holderDir, submitted)

	// define
	if d := c.define(); d.Role != canaryRoleText || d.State != "stopped" {
		t.Fatalf("define: %+v", d)
	}
	if state, reason, epoch, found := c.shown(); !found || state != "stopped" || reason != "defined" || epoch != 0 {
		t.Fatalf("after define: %q %q %d %v", state, reason, epoch, found)
	}

	// start: the first epoch, which the golden requests are for
	if s := c.startRole(); s.State != "starting" || s.Epoch != 1 {
		t.Fatalf("start: %+v", s)
	}
	if state, _, epoch, _ := c.shown(); state != "starting" || epoch != 1 {
		t.Fatalf("after start: %q %d", state, epoch)
	}

	// ticket, then inject: the order the golden command ids stand for
	var tk CanaryTicketResult
	c.mustCall(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &tk)
	if tk.Epoch != 1 || tk.TicketFile != filepath.Join(c.holderDir, TicketFileName) {
		t.Fatalf("ticket: %+v", tk)
	}
	res, err := c.inject(goldenMessageID, goldenBody)
	if err != nil {
		t.Fatal(err)
	}
	if res.Epoch != 1 || res.MessageID != goldenMessageID || res.Result.Outcome != wire.InjectOutcomeSubmitted ||
		res.Result.Written != wire.InjectWrittenWhole {
		t.Fatalf("inject: %+v", res)
	}

	// stop
	if st := c.stopRole(); st.State != "stopped" || st.Proof == "" {
		t.Fatalf("stop: %+v", st)
	}
	if state, reason, epoch, _ := c.shown(); state != "stopped" || reason != "stopped" || epoch != 1 {
		t.Fatalf("after stop: %q %q %d", state, reason, epoch)
	}

	// The evidence, in order, with the fields the owner reads.
	if got, want := strings.Join(c.events(), " "), "daemon-start define start ticket inject-sent inject-result stop"; got != want {
		t.Fatalf("evidence events %q, want %q", got, want)
	}
	lines := c.evidence()
	for _, i := range []int{3, 4, 5, 6} {
		if lines[i]["epoch"] != float64(1) {
			t.Errorf("evidence line %d: %v", i, lines[i])
		}
	}
	if m := lines[4]; m["message_id"] != goldenMessageID {
		t.Errorf("inject-sent line: %v", m)
	}
	if m := lines[5]; m["message_id"] != goldenMessageID || m["outcome"] != "submitted" || m["written"] != "whole" {
		t.Errorf("inject-result line: %v", m)
	}
	ev, err := os.ReadFile(c.cfg.EvidencePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ev), "0123456789abcdef-golden") {
		t.Error("the body is in the evidence")
	}

	// The requests: these two and no other, byte for byte the golden files.
	payloads, reqs := holder.seen()
	if len(payloads) != 2 || reqs[0].Op != wire.OpTicket || reqs[1].Op != wire.OpInject {
		t.Fatalf("the holder was sent %d requests %+v, want a ticket request and an inject request", len(payloads), reqs)
	}
	ticket := reqs[0].Ticket
	if err := wire.CheckTicket(ticket); err != nil || ticket == maskedTicket {
		t.Fatalf("the ticket sent is %q (%v)", ticket, err)
	}
	if got, _ := os.ReadFile(tk.TicketFile); string(got) != ticket {
		t.Fatalf("the ticket file holds %q, the holder was sent %q", got, ticket)
	}
	if n := bytes.Count(payloads[0], []byte(ticket)); n != 1 {
		t.Fatalf("the ticket occurs %d times in its request", n)
	}
	masked := bytes.Replace(payloads[0], []byte(ticket), []byte(maskedTicket), 1)
	if want := readGolden(t, "ticket-request.json"); !bytes.Equal(masked, want) {
		t.Errorf("the ticket request, with its ticket masked, is\n%s\nthe golden file is\n%s", masked, want)
	}
	if want := readGolden(t, "inject-request.json"); !bytes.Equal(payloads[1], want) {
		t.Errorf("the inject request is\n%s\nthe golden file is\n%s", payloads[1], want)
	}
}
