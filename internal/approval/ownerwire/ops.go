package ownerwire

// The operations of the owner connection. Each name is "owner." and one
// part of lower-case letters, digits and dashes: the client protocol's seam
// for added operations refuses a name with a second dot.
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
)

// Operations returns the names of every operation of the owner connection,
// in the order of the table in the package comment. The list is a new one
// on each call, so a caller may keep or change it.
func Operations() []string {
	return []string{
		OpList, OpShow, OpPaths, OpPatch, OpDecider,
		OpDecide, OpBeat, OpLimit, OpLimitSet, OpLooked,
	}
}
