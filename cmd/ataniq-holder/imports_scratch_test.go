//go:build linux && !ataniq_canary

package main

// guardListFlags are the flags of go list for this build: none.
var guardListFlags []string

// guardRepoDeps is the dependency list for the scratch build.
func guardRepoDeps() []string { return scratchRepoDeps }
