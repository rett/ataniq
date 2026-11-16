// Package controllerhandoff contains a synthetic, qualification-only model.
package controllerhandoff

const (
	maxStream      = 16 << 10
	maxChunk       = 4096
	maxEvents      = 2048
	maxRefusals    = 64
	maxLabel       = 512
	maxDiagnostics = 64 << 10
	maxGenerations = 4
	stepCap        = 8
	zeroCap        = 3
)

type identity struct {
	binding    string
	controller string
	context    string
	gi         int
	generation int
	azg        int
}

// attachment is synthetic oracle state, independent of controller assignment.
type attachment struct {
	name       string
	generation int
	authorized bool
}

type request struct {
	id        string
	identity  identity
	successor string
	mode      string
}
type writeID struct {
	identity identity
	request  request // zero value when no request existed at issuance
	id       int
	start    int
	end      int
}
type proof struct {
	origin string
	write  writeID
	j      int
}
type sinkEntry struct {
	sink  string
	write writeID
	j     int
	bytes string
	// proofOnly is an injected proof-source record, not physical sink bytes.
	proofOnly bool
	// at is the zero-based trace position at which these bytes reached the sink.
	at int
}
type ledger struct {
	k         int
	f         int
	w         int
	q         int
	u         int
	d         int
	known     bool
	contested bool
}
type event struct {
	kind       string
	identity   identity
	request    request
	write      writeID
	proof      proof
	bytes      string
	credit     int
	attachment attachment
}
type observation struct {
	event    event
	accepted bool
	ledger   ledger
}
type result struct {
	old                ledger
	sink               []sinkEntry
	trace              []observation
	refusals           int
	refusalsLowerBound bool
	outcome            string
	namespace          string
	native             string
	framing            string
	semantic           string
	incomplete         bool
	goneSinks          map[string]bool
	sinkBytes          map[string]string
}

// gate retains only observations delivered to it, never the sink's private log.
type gate struct {
	current               identity
	old                   ledger
	historical            []ledger
	historicalGenerations []int
	data                  string
	state                 string
	mode                  string
	winning               request
	pending               writeID
	history               map[int]writeID
	applied               map[int]int
	trace                 []observation
	refusals              int
	steps                 int
	zero                  int
	totalBytes            int
	generations           int
	attachments           map[string]attachment
	winningAttachment     attachment
	previousWinners       []request
	diagnosticBytes       int
	lower                 bool
	incomplete            bool
	invalid               bool
	invalidBinding        bool
	lostLedger            bool
	stopped               bool
	committed             bool
	opened                bool
	revoked               bool
	grant                 request
	settlement            ledger
}

// syntheticSink retains raw, independently observable outcomes. The gate sees
// only a delivered result or the proof source's validation at an event boundary.
type syntheticSink struct {
	entries []sinkEntry
	gone    map[string]bool
}
type model struct {
	gate *gate
	sink syntheticSink
}

func newModel(id identity) *model { return &model{gate: newGate(id)} }

func (s *syntheticSink) record(w writeID, data string, credit, at int) {
	n := w.end - w.start
	j := credit
	if j > n {
		j = n
	}
	s.entries = append(s.entries, sinkEntry{sink: w.identity.binding, write: w, j: j, bytes: data[w.start : w.start+j], at: at})
}
func (s *syntheticSink) validates(p proof) bool {
	for _, entry := range s.entries {
		if entry.write == p.write && entry.j == p.j {
			return true
		}
	}
	return false
}
func (s *syntheticSink) delivered(w writeID, credit int) bool {
	for _, entry := range s.entries {
		if !entry.proofOnly && entry.write == w && entry.j == credit {
			return true
		}
	}
	return false
}

// step is the synthetic event boundary. Issuance guards do not query sink truth.
func (m *model) step(e event) bool {
	valid := false
	if e.kind == "result" {
		valid = m.sink.delivered(e.write, e.credit)
	}
	if e.kind == "proof" {
		valid = m.sink.validates(e.proof)
	}
	oldBinding := m.gate.current.binding
	ok := m.gate.apply(e, valid)
	if ok && e.kind == "binding" {
		if m.sink.gone == nil {
			m.sink.gone = make(map[string]bool)
		}
		m.sink.gone[oldBinding] = true
	}
	if ok && e.kind == "issue" {
		m.sink.record(e.write, m.gate.data, e.credit, len(m.gate.trace)-1)
	}
	return ok
}
func (m *model) result() result {
	r := m.gate.result()
	r.sink = append([]sinkEntry(nil), m.sink.entries...)
	r.goneSinks = m.sink.gone
	r.sinkBytes = make(map[string]string)
	for _, entry := range m.sink.entries {
		if !entry.proofOnly {
			r.sinkBytes[entry.sink] += entry.bytes
		}
	}
	if len(m.sink.gone) > 0 {
		if _, exists := r.sinkBytes[m.gate.current.binding]; !exists {
			r.sinkBytes[m.gate.current.binding] = ""
		}
	}
	return r
}

func newGate(id identity) *gate {
	return &gate{current: id, old: ledger{known: true}, state: "steady", generations: 1, incomplete: !validCurrentIdentity(id), attachments: map[string]attachment{"a1": {name: "a1", generation: 1, authorized: true}, "b1": {name: "b1", generation: 1, authorized: true}}, history: make(map[int]writeID), applied: make(map[int]int)}
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
func validIdentity(i identity) bool {
	return len(i.binding) <= maxLabel && len(i.controller) <= maxLabel && len(i.context) <= maxLabel
}
func validCurrentIdentity(i identity) bool {
	return validIdentity(i) && i.binding != "" && i.controller != "" && i.context != "" && i.gi > 0 && i.generation > 0 && i.azg > 0
}

func validRequest(r request) bool {
	return len(r.id) <= maxLabel && len(r.successor) <= maxLabel && len(r.mode) <= maxLabel && validIdentity(r.identity)
}
func validEvent(e event) bool {
	return len(e.kind) <= maxLabel && len(e.bytes) <= maxChunk && len(e.proof.origin) <= maxLabel && len(e.attachment.name) <= maxLabel &&
		validIdentity(e.identity) && validRequest(e.request) && validIdentity(e.write.identity) &&
		validRequest(e.write.request) && validIdentity(e.proof.write.identity) && validRequest(e.proof.write.request)
}

// diagnosticSize counts all variable-length fields retained in an observation.
// Each field is individually bounded before this sum is evaluated.
func diagnosticSize(e event) int {
	identitySize := func(i identity) int { return len(i.binding) + len(i.controller) + len(i.context) }
	requestSize := func(r request) int { return len(r.id) + identitySize(r.identity) + len(r.successor) + len(r.mode) }
	writeSize := func(w writeID) int { return identitySize(w.identity) + requestSize(w.request) }
	return len(e.kind) + identitySize(e.identity) + requestSize(e.request) + writeSize(e.write) + len(e.proof.origin) + writeSize(e.proof.write) + len(e.bytes) + len(e.attachment.name)
}

func (g *gate) attachmentCurrent() bool {
	a, exists := g.attachments[g.winning.successor]
	return exists && a.authorized && a.generation > 0 && a == g.winningAttachment
}

const maxInt = int(^uint(0) >> 1)

func (g *gate) apply(e event, validated bool) bool {
	if g.incomplete {
		return false
	}
	if len(g.trace) >= maxEvents {
		g.stopped = true
		g.incomplete = true
		return false
	}
	if !validEvent(e) {
		g.stopped = true
		g.incomplete = true
		return false
	}
	size := diagnosticSize(e)
	if size > maxDiagnostics-g.diagnosticBytes {
		g.stopped = true
		g.incomplete = true
		return false
	}
	g.diagnosticBytes += size
	ok := false
	switch e.kind {
	case "accept":
		if len(e.bytes) > maxStream-g.totalBytes {
			g.stopped = true
			g.incomplete = true
			break
		}
		if g.state == "steady" && !g.stopped && e.identity == g.current && len(e.bytes) > 0 && len(e.bytes) <= maxChunk && len(e.bytes) <= maxStream-g.totalBytes {
			g.totalBytes += len(e.bytes)
			g.data += e.bytes
			g.old.k += len(e.bytes)
			g.old.q += len(e.bytes)
			ok = true
		} else {
			g.refuse()
		}
	case "request":
		r := e.request
		staleID := false
		for _, previous := range g.previousWinners {
			if previous.id == r.id {
				staleID = true
				break
			}
		}
		if staleID {
			g.refuse()
		} else if g.winning.id != "" && r == g.winning && r.identity == g.current && !g.revoked && !g.invalidBinding && !g.invalid && !g.lostLedger {
			ok = true
		} else if g.winning.id != "" || g.state != "steady" || g.stopped || r.id == "" || len(r.id) > maxLabel || r.identity != g.current || (r.mode != "hold" && r.mode != "drain") || r.successor == "" {
			g.refuse()
		} else {
			g.winning = r
			g.winningAttachment = g.attachments[r.successor]
			g.fence(r.mode)
			ok = true
		}
	case "issue":
		w := e.write
		_, exists := g.history[w.id]
		if !g.stopped && !g.invalid && !g.lostLedger && (g.state == "steady" || (g.state == "fenced" && g.mode == "drain")) && w.identity == g.current && w.request == g.winning && w.id > 0 && !exists && g.old.w == 0 && w.start == g.old.f && w.end > w.start && w.start >= 0 && w.end <= len(g.data) && w.end-w.start <= maxChunk && w.end-w.start == minChunk(g.old.q) && e.credit >= 0 {
			g.old.w = w.end - w.start
			g.old.q -= g.old.w
			g.pending = w
			g.history[w.id] = w
			ok = true
		} else {
			g.refuse()
		}
	case "result":
		if g.old.w > 0 && e.write == g.pending {
			j := e.credit
			if validated && j >= 0 && j <= g.old.w {
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
		if p.origin != "PS" || !exists || h != p.write || p.j < 0 || p.j > p.write.end-p.write.start || !validated {
			g.refuse()
		} else if old, exists := g.applied[p.write.id]; exists {
			if old != p.j {
				g.invalid = true
				g.stopped = true
				found := false
				for i, generation := range g.historicalGenerations {
					if generation == p.write.identity.generation {
						g.historical[i].contested = true
						found = true
					}
				}
				if !found {
					g.old.contested = true
				}
				g.state = "trace-invalid"
			}
			ok = true
		} else if g.old.u > 0 && g.pending == p.write {
			g.applied[p.write.id] = p.j
			g.old.f = p.write.start + p.j
			g.old.u = 0
			g.old.d = g.old.k - g.old.f
			if g.mode == "hold" || (g.mode == "drain" && g.old.f == g.old.k) {
				if !g.invalidBinding && !g.revoked && g.current == p.write.identity {
					g.state = "settled"
				}
			} else if !g.invalidBinding && g.current == p.write.identity {
				g.state = "drain-incomplete"
			}
			ok = true
		} else {
			g.refuse()
		}
	case "commit":
		if !g.incomplete && !g.invalid && !g.invalidBinding && !g.lostLedger && !g.revoked && g.state == "settled" && g.winning.id != "" && g.winning.identity == g.current && g.attachmentCurrent() && g.old.w == 0 && g.old.q == 0 && g.old.u == 0 {
			if g.generations >= maxGenerations || g.current.generation == maxInt {
				g.stopped = true
				g.incomplete = true
				break
			}
			g.grant = g.winning
			g.settlement = g.old
			g.current.generation++
			g.generations++
			g.current.controller = g.winning.successor
			g.committed = true
			g.state = "closed"
			ok = true
		} else {
			g.refuse()
		}
	case "open":
		if g.committed && !g.incomplete && !g.invalid && !g.invalidBinding && !g.lostLedger && !g.revoked && g.state == "closed" && g.current.binding == g.grant.identity.binding && g.current.gi == g.grant.identity.gi && g.current.azg == g.grant.identity.azg && g.current.context == g.grant.identity.context && g.current.generation == g.grant.identity.generation+1 && g.current.controller == g.grant.successor && g.attachmentCurrent() && g.old == g.settlement {
			g.opened = true
			g.historical = append(g.historical, g.old)
			g.historicalGenerations = append(g.historicalGenerations, g.grant.identity.generation)
			g.old = ledger{known: true}
			g.data = ""
			g.previousWinners = append(g.previousWinners, g.winning)
			g.winning = request{}
			g.pending = writeID{}
			g.mode = ""
			g.steps, g.zero = 0, 0
			g.stopped = false
			g.committed = false
			g.state = "steady"
			ok = true
		} else {
			g.refuse()
		}
	case "attachment":
		a := e.attachment
		if a.name == "" || a.generation <= 0 {
			g.stopped = true
			g.incomplete = true
			break
		}
		previous, exists := g.attachments[a.name]
		if !exists || a.generation <= previous.generation {
			g.refuse()
			break
		}
		g.attachments[a.name] = a
		ok = true
	case "revoke":
		if g.current.azg == maxInt {
			g.stopped = true
			g.incomplete = true
			break
		}
		g.current.azg++
		g.revoked = true
		g.stopped = true
		if g.state == "steady" || g.state == "fenced" {
			g.fence("hold")
		}
		ok = true
	case "binding":
		g.current.binding = e.identity.binding
		g.invalidBinding = true
		g.stopped = true
		g.state = "invalid-binding"
		ok = true
	case "restart-preserved":
		if g.current.gi == maxInt {
			g.stopped = true
			g.incomplete = true
			break
		}
		g.current.gi++
		g.stopped = true
		ok = true
	case "restart-lost":
		if g.current.gi == maxInt {
			g.stopped = true
			g.incomplete = true
			break
		}
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
	default:
		g.refuse()
	}
	g.trace = append(g.trace, observation{e, ok, g.old})
	return ok
}
func (g *gate) result() result {
	outcome := "MODEL_ACCOUNTED_NO_SUCCESSOR"
	switch {
	case g.incomplete:
		outcome = "INCOMPLETE"
	case g.invalid:
		outcome = "TRACE_INVALID"
	case g.lostLedger:
		outcome = "LEDGER_INCOMPLETE"
	case g.invalidBinding:
		outcome = "INVALID_BINDING"
	case g.opened:
		outcome = "MODEL_BYTE_SWITCH_CONSISTENT"
	case g.old.u > 0:
		outcome = "UNRESOLVED"
	case g.state == "drain-incomplete" && g.winning.id != "":
		outcome = "DRAIN_INCOMPLETE"
	}
	old := g.old
	if len(g.historical) > 0 {
		old = g.historical[0]
	}
	return result{old: old, trace: append([]observation(nil), g.trace...), refusals: g.refusals, refusalsLowerBound: g.lower, outcome: outcome, namespace: "synthetic", native: "UNAVAILABLE (unqualified)", framing: "NOT_ESTABLISHED", semantic: "NOT_ESTABLISHED", incomplete: g.incomplete}
}

func minChunk(q int) int {
	if q > maxChunk {
		return maxChunk
	}
	return q
}
