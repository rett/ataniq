package controllerhandoff

import "testing"

// These are small evidence checks, not the frozen scenario fixtures. They are
// source-only until the exact-source review permits execution.
func TestPartialEvidenceVerifier(t *testing.T) {
	id := identity{binding: "synthetic", controller: "a", context: "c", gi: 1, generation: 1, azg: 1}
	r := request{id: "r", identity: id, successor: "b", mode: "hold"}
	w := writeID{identity: id, id: 1, start: 0, end: 2}
	accepted := observation{event: event{kind: "accept", identity: id, bytes: "ab"}, accepted: true, ledger: ledger{k: 2, q: 2, known: true}}
	issued := observation{event: event{kind: "issue", write: w}, accepted: true, ledger: ledger{k: 2, w: 2, known: true}}
	for _, tc := range []struct {
		name      string
		trace     []observation
		sink      []sinkEntry
		truncated bool
		want      string
	}{
		{"incomplete", []observation{accepted, issued}, []sinkEntry{{write: w, j: 2, bytes: "ab", at: 1}}, false, "ASSERT_INCOMPLETE"},
		{"unissued sink bytes", []observation{accepted}, []sinkEntry{{write: w, j: 1, bytes: "a", at: 0}}, false, "ASSERT_VIOLATION(V1)"},
		{"held residue issue", []observation{accepted, {event: event{kind: "request", request: r}, accepted: true, ledger: ledger{k: 2, d: 2, known: true}}, issued}, nil, false, "ASSERT_VIOLATION(V5)"},
		{"commit with unresolved write", []observation{accepted, issued, {event: event{kind: "lost", write: w}, accepted: true, ledger: ledger{k: 2, u: 2, known: true}}, {event: event{kind: "commit"}, accepted: true, ledger: ledger{k: 2, u: 2, known: true}}}, nil, false, "ASSERT_VIOLATION(V8)"},
		{"truncated", []observation{accepted}, nil, true, "ASSERT_INCOMPLETE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inspectEvidence(tc.trace, tc.sink, tc.truncated)
			if got.assertion != tc.want {
				t.Errorf("assertion = %s, want %s", got.assertion, tc.want)
			}
		})
	}
}
