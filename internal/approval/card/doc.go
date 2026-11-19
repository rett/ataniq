// Package card says what an approval card holds: the model every surface
// draws from, with no drawing in it.
//
// A card is one pending record ([record.Pending]) made ready for the owner
// to read and decide. [New] builds a [Card] from the record. The sections of
// a card are fields of the model, in the order a surface draws them: the
// title, the verb line, the plan block, the ref lines, the diff summary and
// its rows, the four lists, the flagged paths, the evidence, the lead's note
// and the keys. The package knows no terminal, no width and no colour, and
// it reads no clock: every rule that needs the time takes it as a value.
//
// # What is on a card
//
// A card is the record's binding made readable. Everything under the
// binding's digest is on the model, and nothing else is, with three
// exceptions and the card's name (below). Each exception is a field of
// [Card] with a label that a surface draws beside it:
//
//   - Card.Note, the lead's note ([NoteLabel]): it is shown and not
//     approved;
//   - Card.Differs, the "differs from" block ([DiffersLabel]): it comes from
//     an earlier record. When that record is refused, Card.DiffersRefused
//     says so in one line, in its place;
//   - Card.After and Card.Verdicts, what came after the decision
//     ([AfterLabel]): the state and the outcome line, the held mark
//     ([HeldLabel]) and the verdicts. Card.Refused, one mark of the
//     verdict block, stands with them: it says that a run refused, and an
//     undecided card carries it so that a surface can say so in one line.
//
// One more value stands in a section that the binding covers without being
// under its digest: Title.ID, the record's id. It is the name of the card
// and not content: the host makes it, and a decision carries it beside the
// digest. The coverage test lists it by name, so it is not taken for an
// accident.
//
// # Lead text
//
// A lead can put any byte in a path, a ref name or its note. Every such
// string on the model is a [LeadText]: a value with no exported field, whose
// only way out is the escaping of the leadtext package. The plan text is the
// host's, and its lines are LeadText all the same, because nothing proves
// that no byte of a lead is in it.
//
// One string a lead chose stands on the model as a plain string: the feature
// name, and only after [record.ValidFeatureName] passed it. New refuses a
// record whose name fails. Every other plain string of the model is one the
// host wrote, and New refuses a record in which such a string holds anything
// the escaping would change, so no plain string of a card needs escaping.
//
// # The rules
//
// [Card.MayApprove] is the one rule for when an approve may be offered. It
// takes a [Reading], which is what the reader has done with the card, and
// answers yes or no with a one-line reason. [Card.ShownDigest] is the digest
// of what the card showed.
//
// # What surfaces and backends share
//
// The package also holds the values a surface and a backend hand each other
// ([Decision], [Outcome], [Listing], [Page]), the "differs from" block
// ([DiffersFrom]) and the input of the count from the first card
// ([CountInput]).
package card
