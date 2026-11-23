// Package genesis is the family of packages behind the guided new-project
// interview, and this directory's own tests, which hold what no one package
// can: the rules that cross the family.
//
// The family:
//
//   - brief holds the project brief, ataniq.project-brief/1, the typed
//     record an interview fills, and every refusal code of the family.
//   - catalog holds the question catalog, as data, and the walker that picks
//     the next round and applies the owner's answers to a brief.
//   - package approval/ownerwire holds the words of the wire the phone
//     speaks to ask the questions and send the answers: the one gate
//     operation seat.genesis, with its strict forms and the gate codes
//     genesis-busy, genesis-unknown-thread, genesis-bad-answer,
//     genesis-over-cap, genesis-secret-shaped and genesis-done. The wire
//     knows nothing of the packages above; the host maps one to the other.
//
// An answer writes a brief field and nothing else: nothing in the family
// reaches a decision, a contract or a plan.
//
// # What the tests here hold
//
// No test file and no test data of the family holds a literal that a secret
// scanner would take for a key, a token or a private key block: the samples
// the tests need are built at run time from pieces (see TestNoKeyShapedLiteral).
//
// Every refusal code of the brief package maps to the word the seat gate
// answers it with, or to none, and the map is held complete (see
// TestEveryRefusalCodeHasItsGateWord): a new refusal code fails the test until
// it is mapped.
//
// The package holds no code, only this comment and its tests.
package genesis
