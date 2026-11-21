//go:build linux && ataniq_canary

// Package screenrec is the holder's screen recorder, for the owner who makes
// the fixtures of a new agent version by hand (docs/lead-injection.md, "Adding
// a version"). It is compiled in canary mode only, and the holder uses it only
// when it was given --record-dir (a scratch lead whose config says
// "record_screens": true).
//
// # What it writes
//
// Into one new folder, made 0700 by [Recorder.Open], every file 0600 and made
// exclusively by the folder's handle without following a link:
//
//   - README.txt, which says the files are raw (scrub before use; do not
//     commit);
//   - NNNN.txt and NNNN.json, from 0001 on: one pair for each distinct
//     snapshot of the holder's pane (taken once the screen has had no output
//     for [Settle], at most once per [PairInterval], and once more when the
//     session is over), in the fixture format of
//     internal/inject/screen/testdata (the text with a final newline; the size,
//     the cursor, the two flags, the title and the faint spans, "source":
//     "recorded", an empty description and no "want"). The snapshot is the
//     pane's text snapshot converted by injectbridge.SnapshotOf, the
//     screen.Snapshot the fence reads, taken as the fence does only while the
//     parser is at ground and no mode 2026 frame is open; one that waited
//     [TornAfter] for that, or that the last look took before it, has
//     "torn": true in its .json;
//   - titles.jsonl, one JSON line for each window title the pane shows that
//     differs from the last one recorded;
//   - osc7501.jsonl, one JSON line for each OSC 7501 payload the holder's hook
//     sees (the data after "7501;"), the support query included;
//   - LIMIT.txt, only when needed, at most three lines: one when the pairs
//     stopped at [MaxPairs], one when the recording stopped at [MaxBytes], and
//     "dropped N", written once when the recording ends, when the queue
//     dropped N items in all.
//
// Every line carries "ms", the milliseconds since the recorder was made. A
// title or a payload is agent-controlled text: it is cut at [MaxPayload] bytes
// and stored as a JSON string, never interpreted. A payload that is not valid
// UTF-8 is stored with the replacement character for each bad byte, which
// encoding/json makes, and marked "utf8": false.
//
// # What it never does
//
// It never blocks the pane or the agent. The OSC 7501 hook calls
// [Recorder.OSC7501] where the session applies output, and that call copies at
// most [MaxPayload] bytes into a small queue and returns. [Recorder.Watch]
// takes the session's output lock only to read the pane's size and title and,
// at most once per [PairInterval] and only after output or a resize, its text
// snapshot; when the session is over it takes one last look, whatever the
// time since the last snapshot, so the screen shown at the end is recorded,
// and then closes the recorder, which waits for the writer. One goroutine of
// its own writes the queue to the files; the queue holds [QueueLen] items and
// drops the oldest when it is full, counting the drops. The files are never
// read back.
//
// No name or path comes from the agent: the folder is the one the holder was
// given, and the files have fixed names and a counter.
//
// # Bounds and failures
//
// At most [MaxPairs] pairs and [MaxBytes] bytes in all, README.txt and
// LIMIT.txt included (the last 512 bytes are kept for LIMIT.txt, whose three
// lines are each written at most once). The pair that would pass MaxPairs is
// not written: one line goes to LIMIT.txt and one to the log, no pair is taken
// after it, and the titles and payloads go on. The item that would pass
// MaxBytes is not written: one line goes to LIMIT.txt and the recording stops
// for good. A write that fails stops the recording too, and is logged once, by
// its errno and with no path. Either way the agent and the pane go on as
// before.
package screenrec
