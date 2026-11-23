// Package genesis is the family of packages behind the guided new-project
// interview (brief and catalog), and this directory's own tests, which hold
// what no one package can: the rules that cross the family.
//
// # What the tests here hold
//
// No test file and no test data of the family holds a literal that a secret
// scanner would take for a key, a token or a private key block: the samples
// the tests need are built at run time from pieces (see TestNoKeyShapedLiteral).
//
// The package holds no code, only this comment and its tests.
package genesis
