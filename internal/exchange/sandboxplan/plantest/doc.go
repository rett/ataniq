// Package plantest is a fake sandbox planner for the tests of other
// packages. It is for tests only: no command may import it.
//
// [Fake] implements [sandboxplan.Planner]. A test sets the fields that say
// which plans it returns, and reads back the requests it received. The plans
// it returns run nothing sandboxed: an upload-pack argv can be plain
// git-upload-pack on a temporary repository, a measure command prints a
// number the test chose, and a verify prefix can be /usr/bin/env. A test
// that uses it checks the code under test, not a sandbox.
//
// The package uses no network. It starts a process only in [GitUploadPack],
// which may run git --exec-path.
package plantest
