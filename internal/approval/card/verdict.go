package card

// VerdictBlock is what the verdict runs said about a card. No verdict run
// exists yet, so the block is empty. It is shown only after the owner
// decided: Card.Verdicts, Item.Verdicts and Inputs.Verdicts point at it, and
// [New] copies it to a card only when the card is decided.
type VerdictBlock struct{}
