// Package release builds and checks the authority host's release folder,
// the one ah-switch imports (see deploy/authority-host/README.md, "The
// release builder").
//
// Build copies the binaries and the source tree's etc/ and share/ into a new
// folder <counter>-<short commit>, writes its MANIFEST and its SHA256SUMS and
// sets every mode, so that two builds of the same input are byte-identical.
// A static release given no package step gets StaticPackageStep, which
// refuses a change of package snapshot; a refused step leaves the host's
// package state unknown until ah-switch recover --package-state.
// Check reads a built folder back: its sums, manifest and shape, the coverage
// of its command lists (the builder's job, which the switch does not check)
// and the shipped no-AI check on it.
//
// The package needs only the standard library and opens no network
// connection. Everything it reports is a fixed phrase and, where one helps,
// a name of the folder that passed the name rules; it never prints the
// content of a file.
package release
