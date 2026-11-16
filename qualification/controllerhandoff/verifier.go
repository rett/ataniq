package controllerhandoff

// evidenceVerdict deliberately has no final ledger: after a violation it is
// not trustworthy, and this partial verifier cannot establish one on success.
type evidenceVerdict struct {
	assertion string
	event     int
	namespace string
	native    string
	framing   string
	semantic  string
}

// inspectEvidence reads the recorded observations and sink facts without
// calling the gate or its transition and decision functions. A non-violation
// is incomplete until the remaining rules have independent checks.
func inspectEvidence(trace []observation, sink []sinkEntry, truncated bool) evidenceVerdict {
	verdict := evidenceVerdict{assertion: "ASSERT_INCOMPLETE", event: -1,
		namespace: "synthetic", native: "UNAVAILABLE", framing: "NOT_ESTABLISHED", semantic: "NOT_ESTABLISHED"}
	violation := func(rule string, at int) evidenceVerdict {
		v := verdict
		v.assertion, v.event = "ASSERT_VIOLATION("+rule+")", at
		return v
	}
	if truncated || len(trace) > maxEvents || len(sink) > maxEvents {
		return verdict
	}
	type generation struct {
		binding string
		gi, g   int
	}
	key := func(i identity) generation { return generation{i.binding, i.gi, i.generation} }
	streams := make(map[generation]string)
	fenced := make(map[generation]bool)
	hold := make(map[generation]bool)
	stopped := make(map[generation]bool)
	issued := make(map[int]writeID)
	usedSink := make(map[int]bool)
	var pending writeID
	var previous ledger
	var current identity
	var winner request
	var sinkPos int
	for at, observed := range trace {
		e := observed.event
		if at == 0 {
			current = e.identity
		}
		switch e.kind {
		case "accept":
			if observed.accepted {
				k := key(e.identity)
				if fenced[k] || stopped[k] {
					return violation("V1", at)
				}
				streams[k] += e.bytes
				if len(streams[k]) > maxStream {
					return verdict
				}
			}
		case "request":
			if observed.accepted && winner.id == "" {
				winner = e.request
				k := key(e.request.identity)
				fenced[k], hold[k] = true, e.request.mode == "hold"
			}
		case "revoke":
			if observed.accepted {
				fenced[key(current)], hold[key(current)] = true, true
			}
		case "lost":
			if observed.accepted {
				k := key(e.write.identity)
				fenced[k], stopped[k] = true, true
				pending = writeID{}
			}
		case "issue":
			if observed.accepted {
				w := e.write
				k := key(w.identity)
				// Permanent residue and stop checks precede general range checks.
				if hold[k] || stopped[k] || (previous.known && previous.d > 0 && w.start >= previous.f+previous.u && w.start < previous.k) {
					return violation("V5", at)
				}
				if _, exists := issued[w.id]; exists || w.id <= 0 || pending.id != 0 || w.start != previous.f || w.end <= w.start || w.end-w.start > maxChunk {
					return violation("V2", at)
				}
				issued[w.id], pending = w, w
			}
		case "result":
			if observed.accepted && pending == e.write {
				pending = writeID{}
			}
		case "commit", "open":
			if observed.accepted && previous.known && previous.u > 0 {
				return violation("V8", at)
			}
		}
		// Every sink fact is checked at its retained position. A second fact
		// for the same issuance is not treated as extra physical bytes here.
		for sinkPos < len(sink) && sink[sinkPos].at == at {
			s := sink[sinkPos]
			w, exists := issued[s.write.id]
			data := streams[key(s.write.identity)]
			if !exists || w != s.write || usedSink[s.write.id] || s.j < 0 || s.j > w.end-w.start || w.start < 0 || w.start > len(data) || s.j > len(data)-w.start || s.bytes != data[w.start:w.start+s.j] {
				return violation("V1", at)
			}
			usedSink[s.write.id] = true
			sinkPos++
		}
		if sinkPos < len(sink) && sink[sinkPos].at < at {
			return verdict
		}
		l := observed.ledger
		if l.known && !l.contested {
			if l.k < 0 || l.f < 0 || l.w < 0 || l.q < 0 || l.u < 0 || l.d < 0 || (l.u == 0 && l.d == 0 && l.f+l.w+l.q != l.k) || ((l.u != 0 || l.d != 0) && (l.w != 0 || l.q != 0 || l.f+l.u+l.d != l.k)) {
				return violation("V3", at)
			}
		}
		previous = l
		if e.kind == "open" && observed.accepted {
			current = winner.identity
			current.generation++
			current.controller = winner.successor
			winner = request{}
		}
	}
	if sinkPos != len(sink) {
		return verdict
	}
	return verdict
}
