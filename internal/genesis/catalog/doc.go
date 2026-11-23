// Package catalog holds the question catalog of a new-project interview,
// as data, and the walker that picks the next round of questions and
// applies the owner's answers to a project brief.
//
// # The catalog
//
// A catalog is canonical JSON, ataniq.genesis-catalog/1, reviewed like
// code. Version v1 is embedded ([V1]); its digest ([Catalog.Digest]) is
// recorded in every brief started under it, so an interview stays on one
// version. A [Node] is one question: the brief field it sets, its type
// (single, multi, yesno, scale), its topic, a plain prompt, 2 to 8 options
// with a label and a one-line example each, whether it takes other text or
// you-decide, its default, a condition ([Predicate]), the fields a changed
// answer invalidates, a weight and a round hint. The catalog also lists the
// required fields of each kind's brief (software, publishing, media, and
// other for the generic branch).
//
// A condition is a closed set of operations (eq, in, and, not, set,
// unset), at most 4 deep and 16 in size: there is no expression language.
//
// [Load] checks a catalog strictly: its form, its texts (prompt at most
// 160 bytes, label 60, example 100, no markup characters, no web
// addresses), unique ids, reserved option ids, conditions that name only
// fields asked in an earlier round and values those can take, an acyclic
// invalidation graph, and that a changed answer leaves no stale answer
// behind: a node whose condition names a field is cleared when that field
// changes, and a change of kind clears every domain extension field.
//
// # The walker
//
// The walker is pure: no input or output, and the brief given is never
// changed. [Next] returns the next round: among the nodes whose condition
// holds and whose field is unset and required for the brief's kind, the
// one of the highest weight, and up to two more on its topic. It is done
// when every required field is filled, after 6 rounds, when the caller
// says enough, or when no question is left that fills a required field.
// [Apply] checks an answer against its node and sets the field: an option
// as owner-chosen, other text as owner-chosen free text, you-decide as the
// node's default with the node's provenance, skip as the default. An
// answer that changes an earlier one first clears the closure of the
// node's invalidations, keeps the cleared values in the history and
// reports how many it reset, so a card can say "this resets 3 later
// answers". [Summary] groups the brief by provenance.
//
// Every walker function refuses a brief started under another catalog
// version with the code catalog_mismatch. Answers fill brief fields and
// nothing else: no walker function reaches a decision, a contract or a
// plan.
//
// Refusals are [*brief.Error] values with the codes declared in the brief
// package.
package catalog
