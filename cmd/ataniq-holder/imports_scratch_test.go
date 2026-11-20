//go:build linux && !ataniq_canary

package main

// guardListFlags are the flags of go list for this build: none.
var guardListFlags []string

// guardRepoDeps is the list of this build: exactly the six.
func guardRepoDeps() []string { return scratchRepoDeps }
