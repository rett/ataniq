package gatehost

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
)

// seatFP is the key fingerprint of the test's seat, and otherFP a key that
// is not pinned.
var (
	seatFP  = strings.Repeat("ab12", 16)
	otherFP = strings.Repeat("cd34", 16)
)

// The values the fake answers with, each one its encoder takes.
const (
	pendingID    = "ap-feature-x"
	reviewDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	// pairCode is a pairing code of the right form. It is the secret the log
	// must never hold.
	pairCode = "MARKERCDE23456789ABC"
)

// credID is a credential id the log must never hold either; its base64 is
// looked for too.
var credID = []byte("marker-credential-id-0123456789")

// words is a fingerprint of the right length, ownerwire.PairWords words of
// three lower-case letters each ("aaa", "bbb", ...), derived from the
// constant so a change of it needs no change here.
var words = func() []string {
	w := make([]string, ownerwire.PairWords)
	for i := range w {
		w[i] = strings.Repeat(string(rune('a'+i%26)), 3)
	}
	return w
}()

// call is one call the fake received.
type call struct {
	op   string
	src  ownerchannel.Source
	pair ownerwire.SeatPairParams
	id   string
	page ownerwire.PageParams
}

// fakePort is the service as the handler sees it. err, when set, is what
// every call answers; otherwise a call answers a good result. The pairing is
// modelled: open says a pairing waits for the phone, code is its code, and
// seat.pair is refused ErrPairRefused unless the code matches an open
// pairing, as the service does.
type fakePort struct {
	mu    sync.Mutex
	calls []call
	err   error

	open bool
	code string

	// showErr, when set, is what owner.show answers after it was
	// recorded: a refusal of what the peer named, as the service gives.
	showErr error

	// badResult makes every result one its encoder refuses.
	badResult bool
}

func (f *fakePort) record(c call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return f.err
}

func (f *fakePort) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fakePort) PairPresent(ctx context.Context, src ownerchannel.Source, p ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error) {
	if err := f.record(call{op: ownerwire.OpSeatPair, src: src, pair: p}); err != nil {
		return ownerwire.SeatPairResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open || subtle.ConstantTimeCompare([]byte(p.Code), []byte(f.code)) != 1 {
		return ownerwire.SeatPairResult{}, ownerchannel.ErrPairRefused
	}
	f.open = false
	if f.badResult {
		return ownerwire.SeatPairResult{Words: []string{"x"}}, nil
	}
	return ownerwire.SeatPairResult{Words: append([]string(nil), words...)}, nil
}

func (f *fakePort) List(ctx context.Context) (ownerwire.Listing, error) {
	if err := f.record(call{op: ownerwire.OpList}); err != nil {
		return ownerwire.Listing{}, err
	}
	if f.badResult {
		return ownerwire.Listing{Pending: []ownerwire.PendingItem{{ID: "not an id"}}}, nil
	}
	return ownerwire.Listing{Count: ownerwire.Count{Requests: 3, WouldMeet: 2}}, nil
}

func (f *fakePort) Show(ctx context.Context, id string) (ownerwire.ShowResult, error) {
	if err := f.record(call{op: ownerwire.OpShow, id: id}); err != nil {
		return ownerwire.ShowResult{}, err
	}
	f.mu.Lock()
	showErr := f.showErr
	f.mu.Unlock()
	if showErr != nil {
		return ownerwire.ShowResult{}, showErr
	}
	if f.badResult {
		return ownerwire.ShowResult{}, nil
	}
	return ownerwire.ShowResult{Record: ownerwire.NewRecord([]byte(`{"record":1}`))}, nil
}

func (f *fakePort) Paths(ctx context.Context, p ownerwire.PageParams) (ownerwire.PathsPage, error) {
	if err := f.record(call{op: ownerwire.OpPaths, page: p}); err != nil {
		return ownerwire.PathsPage{}, err
	}
	if f.badResult {
		return ownerwire.PathsPage{}, nil
	}
	return ownerwire.PathsPage{ID: p.ID, ReviewDigest: p.ReviewDigest, Page: 0, Pages: 1}, nil
}

func (f *fakePort) Patch(ctx context.Context, p ownerwire.PageParams) (ownerwire.PatchPage, error) {
	if err := f.record(call{op: ownerwire.OpPatch, page: p}); err != nil {
		return ownerwire.PatchPage{}, err
	}
	if f.badResult {
		return ownerwire.PatchPage{}, nil
	}
	return ownerwire.PatchPage{ID: p.ID, ReviewDigest: p.ReviewDigest, Page: 0, Pages: 1}, nil
}

// logBook collects the handler's log lines.
type logBook struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBook) logf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, a...))
}

func (l *logBook) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// pinnedOnly is a pin check that knows the one seat key.
func pinnedOnly(fp string) bool { return fp == seatFP }

// newHandler makes a handler over f (a fakePort, or a waitingPort that can wait) with the seat's key pinned.
func newHandler(t *testing.T, f Port) (*Handler, *logBook) {
	t.Helper()
	lb := &logBook{}
	h, err := New(f, pinnedOnly, lb.logf)
	if err != nil {
		t.Fatal(err)
	}
	return h, lb
}

// validParams are params each operation's decoder takes.
func validParams(t testing.TB, op string) json.RawMessage {
	t.Helper()
	var b []byte
	var err error
	switch op {
	case ownerwire.OpSeatPair:
		b, err = ownerwire.EncodeSeatPairParams(ownerwire.SeatPairParams{Code: pairCode, CredentialID: credID, PublicKey: []byte("marker-public-key")})
	case ownerwire.OpList:
		return nil
	case ownerwire.OpShow:
		b, err = ownerwire.EncodeShowParams(ownerwire.ShowParams{ID: pendingID})
	case ownerwire.OpPaths, ownerwire.OpPatch:
		b, err = ownerwire.EncodePageParams(ownerwire.PageParams{ID: pendingID, ReviewDigest: reviewDigest})
	default:
		t.Fatalf("no params for %s", op)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// codeOfErr is the code a handler's error carries, or "" if it is not coded.
func codeOfErr(err error) string {
	var ce seatgate.CodedError
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return ""
}

// seat is the test's peer.
var seat = seatgate.Peer{Fingerprint: seatFP}
