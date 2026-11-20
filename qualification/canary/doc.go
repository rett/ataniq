// Package canary holds the checks that go with the canary slice's intake
// sandbox template, in templates/. The template is bubblewrap-based and
// serves one throwaway repository. The test of this package checks its form
// only: the template is accepted by the intake's own template parser, and it
// and its note name no host, user or home path. The test starts no
// namespace, runs no bubblewrap and waives nothing; whether the template
// confines is the owner's to vouch for, from the note beside it.
package canary
