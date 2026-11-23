package ownerwire

// The operations of the owner connection. Each name is one prefix and one
// part of lower-case letters, digits and dashes around exactly one dot: the
// client protocol's seam for added operations refuses a name with a second
// dot. The prefix is "owner." for what the owner's console asks, and "seat."
// for the calls of the seat app (the phone's side): [OpSeatPair], which the
// socket serves too, and the five of [GateOperations].
const (
	// OpList reads the listing: what is pending, what was decided lately,
	// the standing rules and the count. Params: none. Result: [Listing].
	OpList = "owner.list"
	// OpShow reads one pending record as its bytes, with what a card needs
	// beside it. Params: [ShowParams]. Result: [ShowResult].
	OpShow = "owner.show"
	// OpPaths reads one page of the full path list of a review. Params:
	// [PageParams]. Result: [PathsPage].
	OpPaths = "owner.paths"
	// OpPatch reads one page of the patch of a review. Params:
	// [PageParams]. Result: [PatchPage].
	OpPatch = "owner.patch"
	// OpDecider asks whether this connection is the one that may decide.
	// Params: [DeciderParams]. Result: [DeciderResult].
	OpDecider = "owner.decider"
	// OpDecide sends the owner's answer to one card. Params:
	// [DecisionFrame]. Result: [DecideResult].
	OpDecide = "owner.decide"
	// OpBeat says the owner is active at the surface. Params: none.
	// Result: nothing.
	OpBeat = "owner.beat"
	// OpLimit reads the daily card limit of a project. Params:
	// [LimitParams]. Result: [LimitView].
	OpLimit = "owner.limit"
	// OpLimitSet changes the daily card limit of a project. Params:
	// [LimitSetParams]. Result: [LimitView].
	OpLimitSet = "owner.limit-set"
	// OpLooked moves the "since he last looked" mark to now. Params: none.
	// Result: nothing.
	OpLooked = "owner.looked"
	// OpPairStart starts the pairing of a phone and answers its one-time
	// code, which is in no other answer. It needs the decider. Params:
	// [PairStartParams]. Result: [PairStartResult].
	OpPairStart = "owner.pair-start"
	// OpPairStatus reads where the pairing stands: none, waiting for the
	// phone, a credential pending at the desk (with its eight words), or
	// expired; a pending one has a pending id. Params: none. Result:
	// [PairStatus].
	OpPairStatus = "owner.pair-status"
	// OpPairConfirm is the owner at the desk saying the eight words are the
	// same on the phone: the pending credential joins the list of deciding
	// keys. It needs the decider, and names the credential it means by its
	// pending id. Params: [PairPendingParams]. Result: [KeyItem].
	OpPairConfirm = "owner.pair-confirm"
	// OpPairReject drops the pending credential and ends the pairing. It
	// needs the decider, and names the credential it means by its pending
	// id. Params: [PairPendingParams]. Result: nothing.
	OpPairReject = "owner.pair-reject"
	// OpKeys reads the list of deciding keys, revoked ones included.
	// Params: none. Result: [KeysView].
	OpKeys = "owner.keys"
	// OpKeyRevoke revokes one credential. It needs the decider. Params:
	// [KeyRevokeParams]. Result: [KeysView].
	OpKeyRevoke = "owner.key-revoke"
	// OpKeysOff is "phone off": it revokes every credential. It needs the
	// decider. Params: none. Result: [KeysView].
	OpKeysOff = "owner.keys-off"
	// OpSeatPair is the seat app's call, relayed: the pairing code and the
	// credential the phone made. Params: [SeatPairParams]. Result:
	// [SeatPairResult]. The daemon answers it only to a seat source, and
	// no build has one yet.
	OpSeatPair = "seat.pair"
)

// The operations only the seat gate serves: a login and a decision from the
// owner's phone, and the long wait that tells the seat when the cards changed. The daemon's Unix socket neither serves nor announces them,
// so they are not in [Operations]; [GateOperations] lists them.
const (
	// OpSeatLoginChallenge draws a login challenge. Params: none. Result:
	// [SeatLoginChallengeResult].
	OpSeatLoginChallenge = "seat.login-challenge"
	// OpSeatLogin takes an assertion over a login challenge and answers the
	// credential that made it. It decides nothing. Params: [SeatLoginParams].
	// Result: [SeatLoginResult].
	OpSeatLogin = "seat.login"
	// OpSeatChallenge draws the challenge for one decision of the owner on
	// the phone. Params: [SeatChallengeParams]. Result:
	// [SeatChallengeResult].
	OpSeatChallenge = "seat.challenge"
	// OpSeatDecide takes the owner's decision with an assertion over its
	// challenge. Params: [SeatDecideParams]. Result: [DecideResult].
	OpSeatDecide = "seat.decide"
	// OpSeatWait is the seat's long wait: it asks whether the set of pending
	// cards differs from the one a version names, and the gate holds the
	// answer until it does or its hold ends. It decides nothing and carries
	// no card text. Its capability is "watch". Params:
	// [SeatWaitParams]. Result: [SeatWaitResult].
	OpSeatWait = "seat.wait"
)

// Operations returns the names of every operation of the owner connection
// that the daemon's Unix socket serves and announces, in the order of the
// table in the package comment. The operations only the seat gate serves are
// not among them; see [GateOperations]. The list is a new one on each call,
// so a caller may keep or change it.
func Operations() []string {
	return []string{
		OpList, OpShow, OpPaths, OpPatch, OpDecider,
		OpDecide, OpBeat, OpLimit, OpLimitSet, OpLooked,
		OpPairStart, OpPairStatus, OpPairConfirm, OpPairReject,
		OpKeys, OpKeyRevoke, OpKeysOff, OpSeatPair,
	}
}

// GateOperations returns the names of the operations only the seat gate
// serves, in the order of their table in the package comment: none of them
// is in [Operations]. The list is a new one on each call.
func GateOperations() []string {
	return []string{OpSeatLoginChallenge, OpSeatLogin, OpSeatChallenge, OpSeatDecide, OpSeatWait}
}
