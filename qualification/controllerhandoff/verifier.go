package controllerhandoff

// evidenceVerdict carries only qualification metadata and the first violation.
type evidenceVerdict struct {
	assertion                            string
	event                                int
	namespace, native, framing, semantic string
}

// evidenceSize independently checks every retained variable-length field.
func evidenceSize(e event) (int, bool) {
	size := 0
	add := func(s string, limit int) bool { size += len(s); return len(s) <= limit }
	ident := func(i identity) bool {
		return add(i.binding, maxLabel) && add(i.controller, maxLabel) && add(i.context, maxLabel)
	}
	var req func(request) bool
	req = func(r request) bool {
		return add(r.id, maxLabel) && ident(r.identity) && add(r.successor, maxLabel) && add(r.mode, maxLabel)
	}
	write := func(w writeID) bool { return ident(w.identity) && req(w.request) }
	ok := add(e.kind, maxLabel) && ident(e.identity) && req(e.request) && write(e.write) && add(e.proof.origin, maxLabel) && write(e.proof.write) && add(e.bytes, maxChunk) && add(e.attachment.name, maxLabel)
	return size, ok
}

// inspectEvidence derives a shadow ledger from initial identity and raw events.
// It is deliberately limited to the one-switch, bounded synthetic slice.
func inspectEvidence(initial identity, trace []observation, sink []sinkEntry, res result, truncated bool) evidenceVerdict {
	v := evidenceVerdict{assertion: "ASSERT_INCOMPLETE", event: -1, namespace: "synthetic", native: "UNAVAILABLE (unqualified)", framing: "NOT_ESTABLISHED", semantic: "NOT_ESTABLISHED"}
	bad := func(rule string, at int) evidenceVerdict {
		x := v
		x.assertion = "ASSERT_VIOLATION(" + rule + ")"
		x.event = at
		return x
	}
	if truncated || res.incomplete || len(trace) > maxEvents || len(sink) > maxEvents || initial.binding == "" || initial.controller == "" || initial.context == "" || initial.gi <= 0 || initial.generation != 1 || initial.azg <= 0 {
		return v
	}
	if len(initial.binding) > maxLabel || len(initial.controller) > maxLabel || len(initial.context) > maxLabel {
		return v
	}
	// Sink evidence is independently bounded, including labels embedded in writes.
	for _, entry := range sink {
		_, bounded := evidenceSize(event{write: entry.write, bytes: entry.bytes})
		if !bounded || len(entry.sink) > maxLabel {
			return v
		}
	}
	cur := initial
	l := ledger{known: true}
	var historical ledger
	hasHistorical := false
	state, mode := "steady", ""
	var winner, grant request
	var pending writeID
	issued := map[int]writeID{}
	issueAt := map[int]int{}
	credits := map[int]int{}
	applied := map[int]int{}
	attachments := map[string]attachment{"a1": {name: "a1", generation: 1, authorized: true}, "b1": {name: "b1", generation: 1, authorized: true}}
	var winningAttachment attachment
	data := ""
	total := 0
	steps, zero, refusals := 0, 0, 0
	requests, diagnostics := 0, 0
	lower, stopped, revoked, invalidBinding, lostLedger, invalid, committed, opened := false, false, false, false, false, false, false, false
	gone := map[string]bool{}
	physical := map[string]string{}
	used := map[int]bool{}
	sinkPos := 0
	settle := func() {
		if lostLedger {
			return
		}
		if state == "fenced" && l.w == 0 && l.u == 0 {
			if mode == "hold" {
				l.q = 0
				l.d = l.k - l.f
				state = "settled"
			} else if l.f == l.k {
				l.q = 0
				state = "settled"
			}
		}
	}
	terminate := func() {
		stopped = true
		if lostLedger {
			return
		}
		if l.w > 0 {
			l.u = l.w
			l.d = l.q
			l.w = 0
			l.q = 0
			state = "unresolved"
		} else {
			l.d = l.q
			l.q = 0
			state = "drain-incomplete"
		}
	}
	capSteps := func() {
		if state == "fenced" && (steps >= stepCap || zero >= zeroCap) {
			terminate()
		}
	}
	attachmentCurrent := func() bool {
		a, ok := attachments[winner.successor]
		return ok && a.authorized && a.generation > 0 && a == winningAttachment
	}
	proofValid := func(p proof, at int) bool {
		for _, s := range sink {
			if s.at <= at && s.write == p.write && s.j == p.j {
				return true
			}
		}
		return false
	}
	delivered := func(w writeID, j, at int) bool {
		for _, s := range sink {
			if !s.proofOnly && s.at <= at && s.write == w && s.j == j {
				return true
			}
		}
		return false
	}
	for at, o := range trace {
		e := o.event
		size, bounded := evidenceSize(e)
		if !bounded || size > maxDiagnostics-diagnostics {
			return v
		}
		diagnostics += size
		if e.kind == "request" {
			requests++
			if requests > 3 {
				return v
			}
		}
		// Grant payloads and attachment changes are outside the reviewed slice.
		if (e.kind == "commit" || e.kind == "open" || e.kind == "revoke" || e.kind == "restart-preserved" || e.kind == "restart-lost" || e.kind == "disposition") && (e.identity != (identity{}) || e.request != (request{})) {
			return v
		}
		if e.kind == "attachment" || (invalid && e.kind == "proof") {
			return v
		}
		if opened && e.kind != "accept" && e.kind != "issue" && e.kind != "result" && e.kind != "projection" {
			return v
		}
		before := l
		priorState := state
		ok := false
		rule := "V3"
		// V1 and V5 take precedence over the more general identity and issue rules.
		if o.accepted && e.kind == "accept" && (state != "steady" || stopped || revoked || invalidBinding || lostLedger || invalid || len(e.bytes) == 0 || len(e.bytes) > maxChunk || e.identity != cur) {
			return bad("V1", at)
		}
		if o.accepted && e.kind == "issue" && (stopped || lostLedger || (state == "fenced" && mode == "hold") || (l.d > 0 && e.write.start >= l.f+l.u && e.write.start < l.k)) {
			return bad("V5", at)
		}
		if o.accepted && e.kind == "issue" {
			w := e.write
			_, exists := issued[w.id]
			n := l.q
			if n > maxChunk {
				n = maxChunk
			}
			if l.w != 0 || w.id <= 0 || exists || w.start != l.f || w.end-w.start != n || n <= 0 || w.identity != cur || w.request != winner || e.credit < 0 {
				return bad("V2", at)
			}
		}
		if o.accepted && e.kind == "result" {
			rule = "V4"
		}
		if e.kind == "proof" {
			rule = "V6"
		}
		if e.kind == "tick" {
			rule = "V7"
		}
		if e.kind == "commit" || e.kind == "open" {
			rule = "V8"
		}
		if e.kind == "request" {
			rule = "V9"
		}
		if o.accepted && e.kind == "request" && winner.id != "" && e.request != winner {
			return bad("V9", at)
		}
		if o.accepted && ((e.kind == "issue" && e.write.identity != cur) || (e.kind == "request" && e.request.identity != cur)) {
			return bad("V10", at)
		}
		switch e.kind {
		case "accept":
			rule = "V1"
			if len(e.bytes) > maxStream-total {
				return v
			}
			if state == "steady" && !stopped && e.identity == cur && len(e.bytes) > 0 && len(e.bytes) <= maxChunk {
				data += e.bytes
				total += len(e.bytes)
				l.k += len(e.bytes)
				l.q += len(e.bytes)
				ok = true
			}
		case "request":
			r := e.request
			if winner.id != "" && r == winner && r.identity == cur && !revoked && !invalidBinding && !invalid && !lostLedger {
				ok = true
			} else if winner.id == "" && state == "steady" && !stopped && r.id != "" && r.identity == cur && (r.mode == "hold" || r.mode == "drain") && r.successor != "" {
				winner = r
				winningAttachment = attachments[r.successor]
				mode = r.mode
				state = "fenced"
				settle()
				ok = true
			}
		case "issue":
			rule = "V2"
			w := e.write
			_, exists := issued[w.id]
			n := l.q
			if n > maxChunk {
				n = maxChunk
			}
			if !stopped && !invalid && !lostLedger && (state == "steady" || (state == "fenced" && mode == "drain")) && w.identity == cur && w.request == winner && w.id > 0 && !exists && l.w == 0 && w.start == l.f && n > 0 && w.end-w.start == n && w.start >= 0 && w.end <= len(data) && e.credit >= 0 {
				l.w = n
				l.q -= n
				pending = w
				issued[w.id] = w
				issueAt[w.id] = at
				credits[w.id] = e.credit
				ok = true
			}
		case "result":
			if l.w > 0 && e.write == pending && e.credit >= 0 && e.credit <= l.w && delivered(e.write, e.credit, at) {
				applied[e.write.id] = e.credit
				l.f += e.credit
				l.w = 0
				l.q = l.k - l.f
				if state == "fenced" {
					steps++
					if e.credit == 0 {
						zero++
					} else {
						zero = 0
					}
					settle()
					capSteps()
				}
				ok = true
			}
		case "lost":
			rule = "V5"
			if l.w > 0 && e.write == pending {
				if state == "steady" {
					state = "fenced"
				}
				terminate()
				ok = true
			}
		case "tick":
			if state == "fenced" {
				steps++
				capSteps()
				ok = true
			}
		case "proof":
			p := e.proof
			w, exists := issued[p.write.id]
			if p.origin == "PS" && exists && w == p.write && p.j >= 0 && p.j <= p.write.end-p.write.start && proofValid(p, at) {
				if j, done := applied[p.write.id]; done {
					if j != p.j {
						invalid = true
						stopped = true
						if hasHistorical && p.write.identity.generation == initial.generation {
							historical.contested = true
						} else {
							l.contested = true
						}
						state = "trace-invalid"
					}
					ok = true
				} else if l.u > 0 && pending == p.write {
					applied[p.write.id] = p.j
					l.f = p.write.start + p.j
					l.u = 0
					l.d = l.k - l.f
					if mode == "hold" || (mode == "drain" && l.f == l.k) {
						if !invalidBinding && !revoked && cur == p.write.identity {
							state = "settled"
						}
					} else if !invalidBinding && cur == p.write.identity {
						state = "drain-incomplete"
					}
					ok = true
				}
			}
		case "commit":
			if !invalid && !invalidBinding && !lostLedger && !revoked && state == "settled" && winner.id != "" && winner.identity == cur && attachmentCurrent() && l.w == 0 && l.q == 0 && l.u == 0 {
				if opened || cur.generation != 1 {
					return v
				}
				grant = winner
				cur.generation++
				cur.controller = winner.successor
				committed = true
				state = "closed"
				ok = true
			}
		case "open":
			expected := grant.identity
			expected.generation++
			expected.controller = grant.successor
			if committed && !invalid && !invalidBinding && !lostLedger && !revoked && state == "closed" && cur == expected && attachmentCurrent() {
				opened = true
				requests = 0
				historical = l
				hasHistorical = true
				l = ledger{known: true}
				data = ""
				winner = request{}
				pending = writeID{}
				mode = ""
				steps = 0
				zero = 0
				stopped = false
				committed = false
				state = "steady"
				ok = true
			}
		case "attachment":
			a := e.attachment
			old, exists := attachments[a.name]
			if a.name == "" || a.generation <= 0 {
				return v
			}
			if exists && a.generation > old.generation {
				attachments[a.name] = a
				ok = true
			}
		case "revoke":
			cur.azg++
			revoked = true
			stopped = true
			if state == "steady" || state == "fenced" {
				mode = "hold"
				state = "fenced"
				settle()
			}
			ok = true
		case "binding":
			gone[cur.binding] = true
			cur.binding = e.identity.binding
			invalidBinding = true
			stopped = true
			state = "invalid-binding"
			ok = true
		case "restart-preserved":
			cur.gi++
			stopped = true
			ok = true
		case "restart-lost":
			cur.gi++
			stopped = true
			lostLedger = true
			l = ledger{f: l.f}
			issued = map[int]writeID{}
			ok = true
			rule = "V12"
		case "disposition":
			ok = true
		case "projection":
			rule = "V11"
		default:
			return v
		}
		if ok != o.accepted {
			return bad(rule, at)
		}
		if !ok {
			if refusals < maxRefusals {
				refusals++
			}
			if refusals == maxRefusals {
				lower = true
			}
		}
		// V7 includes cap effects and refusal accounting; V6 includes proof effects.
		if o.ledger != l {
			if e.kind == "result" {
				rule = "V4"
			} else if e.kind == "proof" {
				rule = "V6"
			} else if e.kind == "tick" {
				rule = "V7"
			} else if e.kind == "restart-lost" || lostLedger {
				rule = "V12"
			}
			if rule != "V3" {
				return bad(rule, at)
			}
		}
		for sinkPos < len(sink) && sink[sinkPos].at == at {
			s := sink[sinkPos]
			sinkPos++
			if s.proofOnly {
				w, exists := issued[s.write.id]
				if !exists || w != s.write || s.sink != w.identity.binding || s.j < 0 || s.j > w.end-w.start || s.bytes != "" {
					return bad("V6", at)
				}
				continue
			}
			w, exists := issued[s.write.id]
			n := w.end - w.start
			j := credits[w.id]
			if j > n {
				j = n
			}
			if !exists || w != s.write || used[w.id] || issueAt[w.id] != at || s.sink != w.identity.binding || s.j != j || w.start < 0 || s.j < 0 || w.start+s.j > len(data) || s.bytes != data[w.start:w.start+s.j] {
				return bad("V1", at)
			}
			used[w.id] = true
			physical[s.sink] += s.bytes
		}
		if sinkPos < len(sink) && sink[sinkPos].at < at {
			return v
		}
		if e.kind == "issue" && ok && !used[e.write.id] {
			return bad("V1", at)
		}
		// V3: arithmetic, monotone progress, frozen K and permanent D range.
		if o.ledger != l {
			return bad("V3", at)
		}
		if !l.known && (l.k != 0 || l.w != 0 || l.q != 0 || l.u != 0 || l.d != 0) {
			return bad("V12", at)
		}
		if l.known && !l.contested {
			if l.k < 0 || l.f < 0 || l.w < 0 || l.q < 0 || l.u < 0 || l.d < 0 || (l.u == 0 && l.d == 0 && l.f+l.w+l.q != l.k) || ((l.u != 0 || l.d != 0) && (l.w != 0 || l.q != 0 || l.f+l.u+l.d != l.k)) {
				return bad("V3", at)
			}
			if e.kind != "open" && before.known && (l.f < before.f || (priorState != "steady" && l.k != before.k) || (before.d > 0 && l.d > 0 && l.f+l.u+l.d != before.f+before.u+before.d)) {
				return bad("V3", at)
			}
		}
	}
	if sinkPos != len(sink) {
		return v
	}
	for id := range issueAt {
		if !used[id] {
			return bad("V1", issueAt[id])
		}
	}
	// V11: qualification fields and supported terminal outcomes.
	if res.namespace != v.namespace || res.native != v.native || res.framing != v.framing || res.semantic != v.semantic {
		return bad("V11", len(trace))
	}
	outcome := "MODEL_ACCOUNTED_NO_SUCCESSOR"
	switch {
	case invalid:
		outcome = "TRACE_INVALID"
	case lostLedger:
		outcome = "LEDGER_INCOMPLETE"
	case invalidBinding:
		outcome = "INVALID_BINDING"
	case opened:
		outcome = "MODEL_BYTE_SWITCH_CONSISTENT"
	case l.u > 0:
		outcome = "UNRESOLVED"
	case state == "drain-incomplete" && winner.id != "":
		outcome = "DRAIN_INCOMPLETE"
	}
	if res.outcome != outcome {
		return bad("V11", len(trace))
	}
	old := l
	if hasHistorical {
		old = historical
	}
	if res.old != old {
		return bad("V3", len(trace))
	}
	if res.refusals != refusals || res.refusalsLowerBound != lower {
		return bad("V7", len(trace))
	}
	if len(gone) != len(res.goneSinks) {
		return bad("V1", len(trace))
	}
	for label := range gone {
		if !res.goneSinks[label] {
			return bad("V1", len(trace))
		}
		for _, s := range sink {
			if !s.proofOnly && s.sink == cur.binding {
				return bad("V1", len(trace))
			}
		}
	}
	if len(gone) > 0 {
		if _, exists := physical[cur.binding]; !exists {
			physical[cur.binding] = ""
		}
	}
	if len(physical) != len(res.sinkBytes) {
		return bad("V1", len(trace))
	}
	for label, bytes := range physical {
		if got, exists := res.sinkBytes[label]; !exists || got != bytes {
			return bad("V1", len(trace))
		}
	}
	// Unsupported terminal contexts do not confer a broad correctness claim.
	if !invalid && !lostLedger && !invalidBinding && !opened && l.u == 0 && (!stopped || l.w != 0 || l.q != 0 || state == "closed" || (state == "settled" && winner.id != "")) {
		return v
	}
	v.assertion = "ASSERT_PASS"
	return v
}
