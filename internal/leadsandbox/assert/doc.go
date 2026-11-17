// Package assert holds the start-up assertions of the lead sandbox: the ten of
// the lead-hosting design (section 3.8) as a table, the spec the holder writes
// for the probe and the report the probe writes. It is a proposal that awaits
// qualification and an owner decision. Nothing here says the sandbox is
// qualified, safe or supported: a report states facts about ten assertions,
// and a reader decides what to do with them.
//
// This package so far holds the table ([Assertions]), the [Spec] with its
// strict decoder ([DecodeSpec]) and its builder ([NewSpec]), and the [Report]
// with its one-line format ([EncodeLine], [ParseLine]) and the judgement of a
// report against its spec ([Failed], [Problems]). It also holds the inside
// checks, assertions 1 to 8, in [RunInside], over a small [System] interface:
// [HostSystem] is the real one (Linux) and tests use a fake. The checks never
// create a file (writability is asked of the kernel with access(W_OK) and read
// from the mount table's read-only flag, and both must say read-only), never
// issue TIOCSTI, never send a byte on a connection and start no process: the
// package imports no os/exec. A check that cannot be made is not-checked, with
// the reason, and [Failed] counts it as a failure unless the spec marks the
// assertion optional.
package assert
