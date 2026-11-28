package gatehost

import (
	"errors"

	"ataniq/internal/approval/ownerwire"
)

// genesisCodes are the gate words a [GenesisPort] may refuse with: the nine
// words of seat.genesis, and bad-params for params the port itself refuses.
var genesisCodes = map[string]bool{
	ownerwire.CodeGenesisBusy:          true,
	ownerwire.CodeGenesisUnknownThread: true,
	ownerwire.CodeGenesisBadAnswer:     true,
	ownerwire.CodeGenesisOverCap:       true,
	ownerwire.CodeGenesisSecretShaped:  true,
	ownerwire.CodeGenesisDone:          true,
	ownerwire.CodeGenesisStaleRound:    true,
	ownerwire.CodeGenesisNotReady:      true,
	ownerwire.CodeGenesisNoTemplate:    true,
	CodeBadParams:                      true,
}

// errGenesisWord is the cause logged for a refusal of the port that carries a
// word that is not one of seat.genesis. The word itself is not logged: the log
// holds only texts the handler chose.
var errGenesisWord = errors.New("the genesis port refused with a word that is not one of seat.genesis")

// genesisAnswer turns the port's answer into the handler's outcome: its result
// encoded, a refusal that carries one of the words of seat.genesis as that word,
// and any other error as the generic answers of [answer] (unavailable for a
// cancelled call, deadline-exceeded for an expired one, failed with the cause
// in the log for the rest).
func genesisAnswer(r ownerwire.SeatGenesisResult, err error) outcome {
	if err != nil {
		var g interface{ GateCode() string }
		if errors.As(err, &g) {
			if c := g.GateCode(); genesisCodes[c] {
				return outcome{code: c}
			}
			return outcome{code: CodeFailed, cause: errGenesisWord}
		}
	}
	return answer(r, err, ownerwire.EncodeSeatGenesisResult)
}
