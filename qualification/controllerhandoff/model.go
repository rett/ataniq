// Package controllerhandoff contains a synthetic, qualification-only model.
package controllerhandoff

const (
	maxStream   = 16 << 10
	maxChunk    = 4096
	maxEvents   = 2048
	maxRefusals = 64
	maxLabel    = 512
	stepCap     = 8
	zeroCap     = 3
)

type identity struct {
	binding, controller, context string
	gi, generation, azg          int
}
type request struct {
	id              string
	identity        identity
	successor, mode string
}
type writeID struct {
	identity       identity
	request        request // zero value when no request existed at issuance
	id, start, end int
}
type proof struct {
	origin string
	write  writeID
	j      int
}
type sinkEntry struct {
	write writeID
	j     int
	bytes string
}
type ledger struct {
	k, f, w, q, u, d int
	known            bool
	contested        bool
}
type event struct {
	kind     string
	identity identity
	request  request
	write    writeID
	proof    proof
	bytes    string
	credit   int
}
type observation struct {
	event    event
	accepted bool
	ledger   ledger
}
type result struct {
	old                                  ledger
	sink                                 []sinkEntry
	trace                                []observation
	refusals                             int
	refusalsLowerBound                   bool
	outcome                              string
	namespace, native, framing, semantic string
	incomplete                           bool
}

type gate struct {
	current                                                         identity
	old                                                             ledger
	data                                                            string
	state, mode                                                     string
	winning                                                         request
	pending                                                         writeID
	history                                                         map[int]writeID
	applied                                                         map[int]int
	sink                                                            []sinkEntry
	trace                                                           []observation
	refusals, steps, zero                                           int
	lower, invalid, lostLedger, stopped, committed, opened, revoked bool
	grant                                                           request
	settlement                                                      ledger
}

func newGate(id identity) *gate {
	return &gate{current: id, old: ledger{known: true}, state: "steady", history: make(map[int]writeID), applied: make(map[int]int)}
}
func (g *gate) refuse() {
	if g.refusals < maxRefusals {
		g.refusals++
	} else {
		g.lower = true
	}
	if g.refusals == maxRefusals {
		g.lower = true
	}
}
func (g *gate) settle() {
	if g.state != "fenced" || g.old.w != 0 || g.old.u != 0 {
		return
	}
	if g.mode == "hold" {
		g.old.q = 0
		g.old.d = g.old.k - g.old.f
		g.state = "settled"
	} else if g.old.f == g.old.k {
		g.old.q = 0
		g.state = "settled"
	}
}
func (g *gate) terminateWrite() {
	g.stopped = true
	if g.old.w > 0 {
		g.old.u = g.old.w
		g.old.d = g.old.q
		g.old.w, g.old.q = 0, 0
		g.state = "unresolved"
	} else {
		g.old.d = g.old.q
		g.old.q = 0
		g.state = "drain-incomplete"
	}
}
func (g *gate) cap() {
	if g.state == "fenced" && (g.steps >= stepCap || g.zero >= zeroCap) {
		g.terminateWrite()
	}
}
func (g *gate) fence(mode string) { g.mode = mode; g.state = "fenced"; g.settle() }
func (g *gate) apply(e event) bool {
	if len(g.trace) >= maxEvents {
		g.stopped = true
		return false
	}
	ok := false
	switch e.kind {
	case "accept":
		if g.state == "steady" && !g.stopped && e.identity == g.current && len(e.bytes) > 0 && len(e.bytes) <= maxChunk && len(g.data)+len(e.bytes) <= maxStream {
			g.data += e.bytes
			g.old.k += len(e.bytes)
			g.old.q += len(e.bytes)
			ok = true
		} else {
			g.refuse()
		}
	case "request":
		r := e.request
		if g.winning.id != "" && r == g.winning {
			ok = true
		} else if g.winning.id != "" || g.state != "steady" || g.stopped || r.id == "" || len(r.id) > maxLabel || r.identity != g.current || (r.mode != "hold" && r.mode != "drain") || r.successor == "" {
			g.refuse()
		} else {
			g.winning = r
			g.fence(r.mode)
			ok = true
		}
	case "issue":
		w := e.write
		_, exists := g.history[w.id]
		if !g.stopped && !g.invalid && !g.lostLedger && (g.state == "steady" || (g.state == "fenced" && g.mode == "drain")) && w.identity == g.current && w.request == g.winning && w.id > 0 && !exists && g.old.w == 0 && w.start == g.old.f && w.end > w.start && w.end-w.start <= maxChunk && w.end <= g.old.f+g.old.q && e.credit >= 0 {
			g.old.w = w.end - w.start
			g.old.q -= g.old.w
			g.pending = w
			g.history[w.id] = w
			j := e.credit
			if j > g.old.w {
				j = g.old.w
			}
			g.sink = append(g.sink, sinkEntry{w, j, g.data[w.start : w.start+j]})
			ok = true
		} else {
			g.refuse()
		}
	case "result":
		if g.old.w > 0 && e.write == g.pending {
			j := -1
			for _, s := range g.sink {
				if s.write == e.write {
					j = s.j
					break
				}
			}
			if j >= 0 && e.credit == j {
				g.applied[e.write.id] = j
				g.old.f += j
				g.old.w = 0
				g.old.q = g.old.k - g.old.f
				if g.state == "fenced" {
					g.steps++
					if j == 0 {
						g.zero++
					} else {
						g.zero = 0
					}
					g.settle()
					g.cap()
				}
				ok = true
			} else {
				g.refuse()
			}
		} else {
			g.refuse()
		}
	case "lost":
		if g.old.w > 0 && e.write == g.pending {
			if g.state == "steady" {
				g.state = "fenced"
			}
			g.terminateWrite()
			ok = true
		} else {
			g.refuse()
		}
	case "tick":
		if g.state == "fenced" {
			g.steps++
			g.cap()
			ok = true
		} else {
			g.refuse()
		}
	case "proof":
		p := e.proof
		h, exists := g.history[p.write.id]
		truth := false
		for _, s := range g.sink {
			if s.write == p.write && s.j == p.j {
				truth = true
			}
		}
		if p.origin != "PS" || !exists || h != p.write || p.j < 0 || p.j > p.write.end-p.write.start || !truth {
			g.refuse()
		} else if old, exists := g.applied[p.write.id]; exists {
			if old != p.j {
				g.invalid = true
				g.stopped = true
				g.old.contested = true
				g.state = "trace-invalid"
			}
			ok = true
		} else if g.old.u > 0 && g.pending == p.write {
			g.applied[p.write.id] = p.j
			g.old.f = p.write.start + p.j
			g.old.u = 0
			g.old.d = g.old.k - g.old.f
			if g.mode == "hold" || (g.mode == "drain" && g.old.f == g.old.k) {
				g.state = "settled"
			} else {
				g.state = "drain-incomplete"
			}
			ok = true
		} else {
			g.refuse()
		}
	case "commit":
		if !g.invalid && !g.lostLedger && !g.revoked && g.state == "settled" && g.winning.id != "" && g.winning.identity == g.current && g.old.w == 0 && g.old.q == 0 && g.old.u == 0 {
			g.grant = g.winning
			g.settlement = g.old
			g.current.generation++
			g.current.controller = g.winning.successor
			g.committed = true
			g.state = "closed"
			ok = true
		} else {
			g.refuse()
		}
	case "open":
		if g.committed && !g.invalid && !g.lostLedger && !g.revoked && g.state == "closed" && g.current.binding == g.grant.identity.binding && g.current.gi == g.grant.identity.gi && g.current.azg == g.grant.identity.azg && g.current.context == g.grant.identity.context && g.current.generation == g.grant.identity.generation+1 && g.current.controller == g.grant.successor && g.old == g.settlement {
			g.opened = true
			g.state = "open"
			ok = true
		} else {
			g.refuse()
		}
	case "revoke":
		g.current.azg++
		g.revoked = true
		g.stopped = true
		if g.state == "steady" || g.state == "fenced" {
			g.fence("hold")
		}
		ok = true
	case "binding":
		g.current.binding = e.identity.binding
		g.stopped = true
		g.state = "invalid-binding"
		ok = true
	case "restart-preserved":
		g.current.gi++
		g.stopped = true
		ok = true
	case "restart-lost":
		g.current.gi++
		g.stopped = true
		g.lostLedger = true
		g.old = ledger{f: g.old.f}
		g.history = make(map[int]writeID)
		ok = true
	case "disposition":
		ok = true
	case "projection":
		g.refuse()
	}
	g.trace = append(g.trace, observation{e, ok, g.old})
	return ok
}
func (g *gate) result() result {
	outcome := "MODEL_ACCOUNTED_NO_SUCCESSOR"
	switch {
	case g.invalid:
		outcome = "TRACE_INVALID"
	case g.lostLedger:
		outcome = "LEDGER_INCOMPLETE"
	case g.opened:
		outcome = "MODEL_BYTE_SWITCH_CONSISTENT"
	case g.state == "invalid-binding":
		outcome = "INVALID_BINDING"
	case g.old.u > 0:
		outcome = "UNRESOLVED"
	case g.state == "drain-incomplete" && g.winning.id != "":
		outcome = "DRAIN_INCOMPLETE"
	}
	return result{old: g.old, sink: append([]sinkEntry(nil), g.sink...), trace: append([]observation(nil), g.trace...), refusals: g.refusals, refusalsLowerBound: g.lower, outcome: outcome, namespace: "synthetic", native: "UNAVAILABLE", framing: "NOT_ESTABLISHED", semantic: "NOT_ESTABLISHED", incomplete: len(g.trace) >= maxEvents}
}
