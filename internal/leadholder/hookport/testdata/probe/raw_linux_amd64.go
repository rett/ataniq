// Package probe supplies the few test-child operations that cannot return
// through Go: a SIGSYS handler and the child side of a raw fork.
package probe

func Handler() (entry, restorer uintptr)

// ForkCall runs clone or clone3. The child exits directly. When after is
// nonzero the child first makes that syscall with its one argument and
// exits with its errno. Only the parent returns to Go.
func ForkCall(number, arg, size, after, flag uintptr) (result int64)
