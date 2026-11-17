// Package sandboxnet derives the network inputs of the lead sandbox: the
// resolv.conf the holder writes for the sandbox, and the argument list of the
// pasta network wrapper (the lead-hosting design, section 3.2).
//
// The package is pure. Functions take file content as bytes, or read through
// the small [FS] interface that tests replace with a fake. Nothing here starts
// a process, creates a namespace or writes a file, and input that a host or a
// lead could write is bounded before any of it is kept.
//
// This package is a proposal and is unqualified. It says what is generated and
// checked, not that the resulting sandbox is safe or supported; qualification
// is a separate, opt-in step.
package sandboxnet
