package seatapp

import "net/http"

// pairing holds the pairing flow's state.
type pairing struct {
	cfg Config
}

func newPairing(cfg Config) *pairing { return &pairing{cfg: cfg} }

// challenge and pair are filled in by the pairing step; until then they refuse
// with a fixed answer.
func (p *pairing) challenge(w http.ResponseWriter, r *http.Request) { notBuilt(w) }
func (p *pairing) pair(w http.ResponseWriter, r *http.Request)      { notBuilt(w) }

func notBuilt(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	w.Write([]byte("{\"error\":\"not_built\"}\n"))
}
