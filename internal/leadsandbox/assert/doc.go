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
// report against its spec ([Failed], [Problems]). The checks themselves are
// not in it yet.
//
// It does not run a check, open a connection, read a file or start a process.
package assert
