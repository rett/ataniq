package gatehost

import (
	"sync"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
)

// The bound on the refusals of one peer. A peer whose requests were refused
// RefusalBound times within RefusalWindow, for what it sent, is refused
// too-many-refusals, without the service and without a log line, until the
// oldest of those refusals is RefusalWindow old. RefusalBound is the owner
// connection's bound on the refused writes of one connection (the daemon's
// DecisionRefusalBound).
const (
	RefusalBound  = 16
	RefusalWindow = 10 * time.Minute
)

// CodeTooManyRefusals answers every request of a peer that is past the
// bound.
const CodeTooManyRefusals = "too-many-refusals"

// counted are the codes that count against the bound: the refusals of what
// the peer sent (an operation, params, a credential, a card it named) or of
// the key it holds. The pairing codes are counted codes, but a refusal of
// seat.pair itself, the call that meets most of them, does not count (see
// countedFor); key-unknown counts where a decision meets it. A refusal for
// the daemon's own state (unavailable, deadline-exceeded, failed,
// door-unavailable, journal-reopened, keys-unavailable, keys-full) does not
// count: a seat that polls while the door is down is not shut out for it.
//
// Of the words of seat.genesis only genesis-bad-answer counts, with
// bad-params as everywhere: an answer the question cannot take (an unknown
// node, an option the question does not hold, a form of answer it does not
// take, a round the thread has not reached) is what the peer sent, the seat's
// own pages never send one, and a seat that keeps sending them is the misuse
// the bound is for. None of the others counts. genesis-stale-round (a round
// already taken), genesis-busy (a third open thread), genesis-done (a call to
// a finished thread), genesis-unknown-thread (a thread that expired, was
// discarded, or is not the store's), genesis-not-ready (a plan of a brief that
// is not complete) and genesis-no-template (a plan of a kind no template
// serves) answer the state of the daemon's store, which a seat in good order
// meets: a double tap, a second tab, a post the browser sent again, a start
// after two threads were left open, a page left open past a discard, a link
// to the plan of a thread the owner finished early or of a kind of the
// owner's own words. genesis-secret-shaped and genesis-over-cap answer what
// the owner typed, which the owner may get wrong a few times on the way to a
// good answer. None of them says anything against the peer, and a few taps
// must not shut the owner out of approvals for ten minutes. They stay bounded
// elsewhere: by the seat's own 30 project requests a minute for each
// credential, by the seat link's room of 12 interview calls, by the gate's 60
// requests a minute, and by the store's own limits (two open threads, eight
// files). They are kept out of this table by being absent.
//
// Of seat.chat, none of its three words counts; bad-params does, as
// everywhere, and so does unknown-op from a handler that serves no chat.
// chat-off (a lead whose switch is off), chat-unknown-lead (a lead the host
// does not know, such as one removed while a page was open) and chat-busy (a
// host that cannot serve the read now) answer the state of the leads and of
// the host, which a seat in good order meets: a page left open while the desk
// turned a lead's chat off, a link to a lead that is gone, a reload while the
// host is busy. A read is a read: the seat may send it as often as its own
// room allows, and a few reloads must not shut the owner out of approvals for
// ten minutes. Params the wire's decoder refuses (a verb that sends, a lead of
// another form, a limit past 16) are bad-params and count: the seat's own
// pages never send them. The chat words are kept out of this table by being
// absent; they stay bounded by the seat's own 30 chat pages a minute for each
// credential, by the seat link's own chat room of 5 calls in 80 seconds,
// which Cards reads never use, and by the gate's 60 requests a minute.
//
// wait-full, the answer to a seat.wait past the peer's two held waits or the
// service's own limit, does not count either, and must not: it is a load
// answer, not a misuse. A seat that reconnects while its old wait is still
// unwinding meets it with no fault of its own, and counting it would let a
// reconnect loop, or a few seats' waits held by the service, shut a good seat
// out for ten minutes with too-many-refusals. It is kept out of this table by
// being absent, and its log line is limited by its own clock instead (see
// waitSlots.mayLogFull).
var counted = func() map[string]bool {
	m := map[string]bool{
		CodeUnknownOp:         true,
		CodeBadParams:         true,
		CodeNotPinned:         true,
		CodePairingRefused:    true,
		CodeCredentialInvalid: true,
		CodePairingOpen:       true,
		CodePairingNone:       true,
		CodePairingExpired:    true,
		CodePairingChanged:    true,
		CodeKeyUnknown:        true,
		CodeKeyAmbiguous:      true,
		// An assertion that does not verify is what the peer sent.
		ownerwire.CodeAssertionInvalid: true,
		// The one refusal of seat.genesis that counts: an answer the
		// question cannot take. None of the others: see above.
		ownerwire.CodeGenesisBadAnswer: true,
	}
	for c := range passedRefusals {
		m[string(c)] = true
	}
	return m
}()

// countedFor reports whether a refusal of op with code counts against the
// bound. No refusal of a pairing or a login counts, but that of an operation
// the handler does not serve (unknown-op) or of a key that is not pinned: the
// seat relays seat.pair for anyone who reaches its pairing page and a login
// for anyone who reaches its login page, and a stranger must not shut the
// owner out. Wrong pairing codes stay bounded by the service, which burns the
// code after five wrong tries. Of a decision's challenge and decide,
// challenge-stale, challenges-full, key-revoked and kind-refused come of
// normal use (a double tap, a slow owner, a card that changed, "phone off" at
// the desk) and are not codes that count; bad-params, key-unknown,
// assertion-invalid and the record codes passed on count, as everywhere.
func countedFor(op, code string) bool {
	switch op {
	case ownerwire.OpSeatPair, ownerwire.OpSeatLoginChallenge, ownerwire.OpSeatLogin:
		if code != CodeUnknownOp && code != CodeNotPinned {
			return false
		}
	}
	return counted[code]
}

// maxPeers is how many peers the bound keeps before it drops those with no
// refusal inside the window. Only keys that passed the gate's pin reach the
// handler, so the peers are as many as the keys ever pinned.
const maxPeers = 64

// bound counts the refusals of each peer, by its key fingerprint.
type bound struct {
	mu    sync.Mutex
	peers map[string]*peerCount
}

// peerCount is one peer's refusals inside the window, oldest first, at most
// RefusalBound of them, and whether it is shut out and was logged so.
type peerCount struct {
	times []time.Time
	shut  bool
}

// prune drops the refusals that are RefusalWindow old.
func (p *peerCount) prune(now time.Time) {
	i := 0
	for i < len(p.times) && now.Sub(p.times[i]) >= RefusalWindow {
		i++
	}
	p.times = p.times[i:]
}

// admit reports whether the peer named key may be served now, and, when it
// may not, whether this is the first request refused since it was shut out.
func (b *bound) admit(key string, now time.Time) (ok, first bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.peers[key]
	if p == nil {
		return true, false
	}
	p.prune(now)
	if len(p.times) < RefusalBound {
		p.shut = false
		return true, false
	}
	first = !p.shut
	p.shut = true
	return false, first
}

// count counts one refusal of the peer named key and reports whether it is
// inside the bound, and so logged. Requests that were admitted together may
// all be refused after the bound is reached; the peer keeps its newest
// RefusalBound refusals, so its shut-out runs from the latest of them.
func (b *bound) count(key string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.peers == nil {
		b.peers = map[string]*peerCount{}
	}
	p := b.peers[key]
	if p == nil {
		if len(b.peers) >= maxPeers {
			b.sweep(now)
		}
		p = &peerCount{}
		b.peers[key] = p
	}
	p.prune(now)
	inside := len(p.times) < RefusalBound
	p.times = append(p.times, now)
	if n := len(p.times); n > RefusalBound {
		p.times = append(p.times[:0], p.times[n-RefusalBound:]...)
	}
	return inside
}

// sweep drops the peers with no refusal inside the window; b.mu is held.
func (b *bound) sweep(now time.Time) {
	for k, p := range b.peers {
		p.prune(now)
		if len(p.times) == 0 {
			delete(b.peers, k)
		}
	}
}

// boundKey is the name the bound keeps a peer under: its fingerprint, or one
// name for every peer without a valid one (which the gate never hands over).
func boundKey(peer seatgate.Peer) string {
	if !seatgate.ValidFingerprint(peer.Fingerprint) {
		return "-"
	}
	return peer.Fingerprint
}
