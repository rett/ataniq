package controllerhandoff

import (
	"fmt"
	"strings"
	"testing"
)

// Frozen analytical expectations: these tests must not be executed before
// independent review of this exact source and completion of the verifier.
type frozenRange struct{ start, end int }
type frozenWant struct {
	old                      ledger
	u, d                     frozenRange
	refusals                 int
	outcome, sink, assertion string
}
type frozenFixture struct {
	name    string
	events  []event
	want    frozenWant
	control string
}

var fixtureID = identity{binding: "syn-beta-1", controller: "a1", context: "syn-ctx-1", gi: 1, generation: 1, azg: 1}

func fixtureRequest(mode string) request {
	return request{id: "syn-req-1", identity: fixtureID, successor: "b1", mode: mode}
}
func fixtureWrite(id, start, end int, who identity, r request) writeID {
	return writeID{identity: who, request: r, id: id, start: start, end: end}
}
func fixtureAccept(id identity, bytes string) event {
	return event{kind: "accept", identity: id, bytes: bytes}
}
func fixtureIssue(w writeID, credit int) event { return event{kind: "issue", write: w, credit: credit} }
func fixtureResult(w writeID, credit int) event {
	return event{kind: "result", write: w, credit: credit}
}
func fixtureProof(w writeID, j int) event {
	return event{kind: "proof", proof: proof{origin: "PS", write: w, j: j}}
}
func fixtureSimple(kind string) event { return event{kind: kind} }
func fixtureP0() []event {
	w := fixtureWrite(1, 0, 2, fixtureID, request{})
	return []event{fixtureAccept(fixtureID, "ab"), fixtureIssue(w, 2), fixtureResult(w, 2)}
}
func fixturePU() []event {
	w := fixtureWrite(2, 2, 6, fixtureID, request{})
	return append(fixtureP0(), fixtureAccept(fixtureID, "cdef"), fixtureIssue(w, 2), fixtureAccept(fixtureID, "gh"), event{kind: "request", request: fixtureRequest("hold")}, event{kind: "lost", write: w})
}
func fixtureWant(k, f, w, q, u, d, refusals int, outcome, sink, assertion string) frozenWant {
	return frozenWant{old: ledger{k: k, f: f, w: w, q: q, u: u, d: d, known: true}, u: frozenRange{f, f + u}, d: frozenRange{f + u, f + u + d}, refusals: refusals, outcome: outcome, sink: sink, assertion: assertion}
}
func fixtureRows() []frozenFixture {
	id2 := fixtureID
	id2.generation = 2
	id2.controller = "b1"
	drain, hold := fixtureRequest("drain"), fixtureRequest("hold")
	pre := func(es ...event) []event { return append(fixtureP0(), es...) }
	req := func(r request) event { return event{kind: "request", request: r} }
	lost := func(w writeID) event { return event{kind: "lost", write: w} }
	commit, open := fixtureSimple("commit"), fixtureSimple("open")
	w2 := fixtureWrite(2, 2, 6, fixtureID, request{})
	wd2 := fixtureWrite(2, 2, 6, fixtureID, drain)
	wb2 := fixtureWrite(2, 0, 2, id2, request{})
	wb3 := fixtureWrite(3, 0, 2, id2, request{})
	w3 := fixtureWrite(3, 2, 6, fixtureID, drain)
	const pass = "ASSERT_PASS"
	const switched = "MODEL_BYTE_SWITCH_CONSISTENT"
	const accounted = "MODEL_ACCOUNTED_NO_SUCCESSOR"
	const unresolved = "UNRESOLVED"
	const incompleteDrain = "DRAIN_INCOMPLETE"
	l01 := pre(fixtureAccept(fixtureID, "cdef"), req(drain), fixtureAccept(fixtureID, "g"), fixtureIssue(wd2, 4), fixtureResult(wd2, 4), commit, open, fixtureAccept(id2, "01"), fixtureIssue(wb3, 2), fixtureResult(wb3, 2))
	w35 := fixtureWrite(3, 5, 6, fixtureID, drain)
	rows := []frozenFixture{
		{"L01", l01, fixtureWant(6, 6, 0, 0, 0, 0, 1, switched, "abcdef01", pass), ""},
		{"L02", pre(fixtureAccept(fixtureID, "cdef"), req(hold), commit, open, fixtureAccept(id2, "01"), fixtureIssue(wb2, 2), fixtureResult(wb2, 2)), fixtureWant(6, 2, 0, 0, 0, 4, 0, switched, "ab01", pass), ""},
		{"L03", pre(fixtureAccept(fixtureID, "cdef"), req(drain), fixtureIssue(wd2, 3), fixtureResult(wd2, 3), fixtureIssue(w35, 1), fixtureResult(w35, 1), commit, open), fixtureWant(6, 6, 0, 0, 0, 0, 0, switched, "abcdef", pass), ""},
		{"L04", pre(fixtureAccept(fixtureID, "cdef"), req(drain), fixtureIssue(wd2, 0), fixtureResult(wd2, 0), fixtureIssue(w3, 4), lost(w3), commit, fixtureProof(wd2, 0), fixtureProof(w3, 4), commit, open), fixtureWant(6, 6, 0, 0, 0, 0, 1, switched, "abcdef", pass), ""},
	}
	// Each record freezes the entire terminal result of one LOST matrix row.
	matrix := []struct {
		name, mode string
		queued     bool
		j          int
		want       frozenWant
		grant      bool
	}{
		{"L05", "drain", false, 0, fixtureWant(6, 2, 0, 0, 0, 4, 2, incompleteDrain, "ab", pass), false},
		{"L06", "drain", false, 2, fixtureWant(6, 4, 0, 0, 0, 2, 2, incompleteDrain, "abcd", pass), false},
		{"L07", "drain", false, 4, fixtureWant(6, 6, 0, 0, 0, 0, 1, switched, "abcdef", pass), true},
		{"L08", "drain", true, 0, fixtureWant(8, 2, 0, 0, 0, 6, 2, incompleteDrain, "ab", pass), false},
		{"L09", "drain", true, 2, fixtureWant(8, 4, 0, 0, 0, 4, 2, incompleteDrain, "abcd", pass), false},
		{"L10", "drain", true, 4, fixtureWant(8, 6, 0, 0, 0, 2, 2, incompleteDrain, "abcdef", pass), false},
		{"L11", "hold", false, 0, fixtureWant(6, 2, 0, 0, 0, 4, 1, switched, "ab", pass), true},
		{"L12", "hold", false, 2, fixtureWant(6, 4, 0, 0, 0, 2, 1, switched, "abcd", pass), true},
		{"L13", "hold", false, 4, fixtureWant(6, 6, 0, 0, 0, 0, 1, switched, "abcdef", pass), true},
		{"L14", "hold", true, 0, fixtureWant(8, 2, 0, 0, 0, 6, 1, switched, "ab", pass), true},
		{"L15", "hold", true, 2, fixtureWant(8, 4, 0, 0, 0, 4, 1, switched, "abcd", pass), true},
		{"L16", "hold", true, 4, fixtureWant(8, 6, 0, 0, 0, 2, 1, switched, "abcdef", pass), true},
	}
	for _, row := range matrix {
		es := pre(fixtureAccept(fixtureID, "cdef"), fixtureIssue(w2, row.j))
		if row.queued {
			es = append(es, fixtureAccept(fixtureID, "gh"))
		}
		es = append(es, req(fixtureRequest(row.mode)), lost(w2), commit, fixtureProof(w2, row.j), commit)
		if row.grant {
			es = append(es, open)
		}
		rows = append(rows, frozenFixture{row.name, es, row.want, ""})
	}
	es17 := pre(fixtureAccept(fixtureID, "cdef"), fixtureIssue(w2, 2), fixtureAccept(fixtureID, "gh"), req(drain))
	for i := 0; i < 8; i++ {
		es17 = append(es17, fixtureSimple("tick"))
	}
	es17 = append(es17, commit)
	w4 := fixtureWrite(4, 2, 6, fixtureID, drain)
	wrong := w2
	wrong.identity.controller = "b1"
	fabricated := fixtureProof(w2, 2)
	fabricated.proof.origin = "gate self-report"
	contested := fixtureWant(8, 4, 0, 0, 0, 4, 1, "TRACE_INVALID", "abcd", pass)
	contested.old.contested = true
	rows = append(rows,
		frozenFixture{"L17", es17, fixtureWant(8, 2, 0, 0, 4, 2, 1, unresolved, "abcd", pass), ""},
		frozenFixture{"L18", pre(fixtureAccept(fixtureID, "cdef"), req(drain), fixtureIssue(wd2, 0), fixtureResult(wd2, 0), fixtureIssue(w3, 0), fixtureResult(w3, 0), fixtureIssue(w4, 0), fixtureResult(w4, 0), commit), fixtureWant(6, 2, 0, 0, 0, 4, 1, incompleteDrain, "ab", pass), ""},
		frozenFixture{"L19", append(fixturePU(), fixtureProof(w2, 2), fixtureProof(w2, 2), commit, open), fixtureWant(8, 4, 0, 0, 0, 4, 0, switched, "abcd", pass), ""},
		frozenFixture{"L20", append(fixturePU(), fixtureProof(wrong, 2), commit), fixtureWant(8, 2, 0, 0, 4, 2, 2, unresolved, "abcd", pass), ""},
		frozenFixture{"L21", append(fixturePU(), fabricated, commit), fixtureWant(8, 2, 0, 0, 4, 2, 2, unresolved, "abcd", pass), ""},
		frozenFixture{"L22", append(fixturePU(), fixtureProof(w2, 4), commit), fixtureWant(8, 2, 0, 0, 4, 2, 2, unresolved, "abcd", pass), ""},
		frozenFixture{"L23", append(fixturePU(), fixtureProof(w2, 2), event{kind: "inject-proof", write: w2, credit: 3}, fixtureProof(w2, 3), commit), contested, ""},
	)
	changed := drain
	changed.mode = "hold"
	newBinding := fixtureID
	newBinding.binding = "syn-beta-2"
	newRequest := hold
	newRequest.id = "syn-req-2"
	lostPrefix := pre(fixtureAccept(fixtureID, "cdef"), fixtureIssue(w2, 2), req(hold), lost(w2))
	l27 := frozenWant{old: ledger{f: 2}, refusals: 2, outcome: "LEDGER_INCOMPLETE", sink: "abcd", assertion: pass}
	rows = append(rows,
		frozenFixture{"L24", pre(fixtureAccept(fixtureID, "cdef"), fixtureIssue(w2, 3), fixtureSimple("revoke"), fixtureAccept(fixtureID, "g"), fixtureResult(w2, 3), req(hold)), fixtureWant(6, 5, 0, 0, 0, 1, 2, accounted, "abcde", pass), ""},
		frozenFixture{"L25", pre(fixtureAccept(fixtureID, "cdef"), fixtureIssue(w2, 2), req(drain), lost(w2), event{kind: "binding", identity: newBinding}, fixtureAccept(fixtureID, "g"), commit, fixtureProof(w2, 2), commit), fixtureWant(6, 4, 0, 0, 0, 2, 3, "INVALID_BINDING", "abcd", pass), ""},
		frozenFixture{"L26", append(append([]event{}, lostPrefix...), fixtureSimple("restart-preserved"), fixtureAccept(fixtureID, "g"), commit, fixtureProof(w2, 2), commit), fixtureWant(6, 4, 0, 0, 0, 2, 3, accounted, "abcd", pass), ""},
		frozenFixture{"L27", append(append([]event{}, lostPrefix...), fixtureSimple("restart-lost"), fixtureProof(w2, 2), commit), l27, ""},
		frozenFixture{"L28", pre(fixtureAccept(fixtureID, "cdef"), fixtureIssue(w2, 2), fixtureAccept(fixtureID, "gh"), lost(w2), fixtureAccept(fixtureID, "i"), fixtureProof(w2, 2), fixtureAccept(fixtureID, "j")), fixtureWant(8, 4, 0, 0, 0, 4, 2, accounted, "abcd", pass), ""},
		frozenFixture{"L29", append(fixturePU(), fixtureSimple("disposition"), commit, req(newRequest)), fixtureWant(8, 2, 0, 0, 4, 2, 2, unresolved, "abcd", pass), ""},
		frozenFixture{"L30", pre(fixtureAccept(fixtureID, "cdef"), fixtureIssue(w2, 2), fixtureSimple("revoke"), lost(w2), fixtureProof(w2, 2), fixtureAccept(fixtureID, "g"), req(hold)), fixtureWant(6, 4, 0, 0, 0, 2, 2, accounted, "abcd", pass), ""},
		frozenFixture{"L31", pre(fixtureAccept(fixtureID, "cdef"), req(drain), req(drain), req(changed), fixtureIssue(wd2, 4), fixtureResult(wd2, 4), commit, open), fixtureWant(6, 6, 0, 0, 0, 0, 1, switched, "abcdef", pass), ""},
		frozenFixture{"L32", append(append([]event{}, l01...), fixtureSimple("projection")), fixtureWant(6, 6, 0, 0, 0, 0, 2, switched, "abcdef01", pass), ""},
	)
	c02 := append([]event{}, l01...)
	c02[5] = fixtureAccept(fixtureID, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	c09 := pre(fixtureAccept(fixtureID, "cdef"), fixtureIssue(w2, 2), fixtureAccept(fixtureID, "gh"), req(drain), lost(w2), commit, fixtureProof(w2, 2))
	c04 := pre(fixtureAccept(fixtureID, "cdef"), fixtureIssue(w2, 2), fixtureAccept(fixtureID, "gh"), req(drain), lost(w2))
	rows = append(rows,
		frozenFixture{"C01", l01, fixtureWant(6, 6, 0, 0, 0, 0, 1, switched, "abcdefg01", "ASSERT_VIOLATION(V1)"), "leak-g"},
		frozenFixture{"C02", c02, fixtureWant(6, 6, 0, 0, 0, 0, 1, switched, "abcdefABCDEFGHIJKLMNOPQRSTUVWXYZ01", "ASSERT_VIOLATION(V1)"), "leak-alphabet"},
		frozenFixture{"C03", c09, fixtureWant(8, 4, 0, 0, 0, 4, 1, incompleteDrain, "abcdefgh", "ASSERT_VIOLATION(V5)"), "residue"},
		frozenFixture{"C04", c04, fixtureWant(8, 2, 0, 0, 4, 2, 0, unresolved, "abcd01", "ASSERT_VIOLATION(V8)"), "commit"},
	)
	return rows
}

// Controls modify observations after the lawful base has run. Their ledger,
// refusal and outcome expectations refer to the trusted pre-violation state.
func mutateFixtureEvidence(kind string, r *result) {
	id2 := fixtureID
	id2.generation, id2.controller = 2, "b1"
	switch kind {
	case "leak-g", "leak-alphabet":
		bytes := "g"
		if kind == "leak-alphabet" {
			bytes = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		}
		// The extra bytes arrive immediately after I2's delivered result.
		extra := sinkEntry{sink: fixtureID.binding, write: fixtureWrite(99, 6, 6+len(bytes), fixtureID, request{}), j: len(bytes), bytes: bytes, at: 7}
		r.sink = append(r.sink[:2], append([]sinkEntry{extra}, r.sink[2:]...)...)
	case "residue":
		w := fixtureWrite(3, 4, 8, fixtureID, fixtureRequest("drain"))
		at := len(r.trace)
		l := ledger{k: 8, f: 4, w: 4, known: true}
		r.trace = append(r.trace, observation{event: fixtureIssue(w, 4), accepted: true, ledger: l}, observation{event: fixtureResult(w, 4), accepted: true, ledger: ledger{k: 8, f: 8, known: true}}, observation{event: fixtureSimple("commit"), accepted: true, ledger: ledger{k: 8, f: 8, known: true}}, observation{event: fixtureSimple("open"), accepted: true, ledger: ledger{known: true}})
		r.sink = append(r.sink, sinkEntry{sink: w.identity.binding, write: w, j: 4, bytes: "efgh", at: at})
	case "commit":
		at := len(r.trace)
		old := ledger{k: 8, f: 2, u: 4, d: 2, known: true}
		w := fixtureWrite(3, 0, 2, id2, request{})
		r.trace = append(r.trace, observation{event: fixtureSimple("commit"), accepted: true, ledger: old}, observation{event: fixtureSimple("open"), accepted: true, ledger: ledger{known: true}}, observation{event: fixtureAccept(id2, "01"), accepted: true, ledger: ledger{k: 2, q: 2, known: true}}, observation{event: fixtureIssue(w, 2), accepted: true, ledger: ledger{k: 2, w: 2, known: true}}, observation{event: fixtureResult(w, 2), accepted: true, ledger: ledger{k: 2, f: 2, known: true}})
		r.sink = append(r.sink, sinkEntry{sink: w.identity.binding, write: w, j: 2, bytes: "01", at: at + 3})
	}
}

func TestFrozenControllerHandoff(t *testing.T) {
	rows := fixtureRows()
	seen := make(map[string]bool)
	for _, row := range rows {
		if seen[row.name] {
			t.Errorf("duplicate fixture ID %s", row.name)
		}
		seen[row.name] = true
	}
	if len(rows) != 36 || len(seen) != 36 {
		t.Errorf("fixture count: rows=%d unique=%d, want 36", len(rows), len(seen))
	}
	for i := 1; i <= 32; i++ {
		if !seen[fmt.Sprintf("L%02d", i)] {
			t.Errorf("missing L%02d", i)
		}
	}
	for i := 1; i <= 4; i++ {
		if !seen[fmt.Sprintf("C%02d", i)] {
			t.Errorf("missing C%02d", i)
		}
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			m := newModel(fixtureID)
			for _, e := range row.events {
				if e.kind == "inject-proof" {
					// L23 adds a second trusted outcome, not a physical write.
					m.sink.entries = append(m.sink.entries, sinkEntry{sink: e.write.identity.binding, write: e.write, j: e.credit, proofOnly: true, at: len(m.gate.trace)})
					continue
				}
				m.step(e)
			}
			got := m.result()
			if got.old != row.want.old || got.refusals != row.want.refusals || got.refusalsLowerBound || got.outcome != row.want.outcome {
				t.Errorf("pre-violation result: ledger=%+v refusals=%d lower=%t outcome=%s; want %+v refusals=%d outcome=%s", got.old, got.refusals, got.refusalsLowerBound, got.outcome, row.want.old, row.want.refusals, row.want.outcome)
			}
			if got.old.known {
				u := frozenRange{got.old.f, got.old.f + got.old.u}
				d := frozenRange{got.old.f + got.old.u, got.old.f + got.old.u + got.old.d}
				if u != row.want.u || d != row.want.d {
					t.Errorf("ranges U=%+v D=%+v; want U=%+v D=%+v", u, d, row.want.u, row.want.d)
				}
			}
			mutateFixtureEvidence(row.control, &got)
			var sink strings.Builder
			for _, s := range got.sink {
				if !s.proofOnly {
					sink.WriteString(s.bytes)
				}
			}
			if sink.String() != row.want.sink {
				t.Errorf("sink=%q want %q", sink.String(), row.want.sink)
			}
			v := inspectEvidence(fixtureID, got.trace, got.sink, got, got.incomplete)
			if v.assertion != row.want.assertion {
				t.Errorf("assertion=%s want %s", v.assertion, row.want.assertion)
			}
			if got.namespace != "synthetic" || got.native != "UNAVAILABLE (unqualified)" || got.framing != "NOT_ESTABLISHED" || got.semantic != "NOT_ESTABLISHED" || v.namespace != "synthetic" || v.native != "UNAVAILABLE (unqualified)" || v.framing != "NOT_ESTABLISHED" || v.semantic != "NOT_ESTABLISHED" {
				t.Error("qualification fields changed")
			}
		})
	}
}
