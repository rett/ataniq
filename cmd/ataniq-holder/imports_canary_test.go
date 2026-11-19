//go:build linux && ataniq_canary

package main

// guardListFlags are the flags of go list for this build: the tag.
var guardListFlags = []string{"-tags", "ataniq_canary"}

// guardRepoDeps is the list of this build. The command has no file of canary
// mode yet, so it is the six; canary mode adds its packages here by name.
func guardRepoDeps() []string { return scratchRepoDeps }
