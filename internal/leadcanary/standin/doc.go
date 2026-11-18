//go:build linux && ataniq_canary

// Package standin is the stand-in agent of the canary slice: a program that
// draws a prompt on a terminal, turns bracketed paste on and, when a pasted
// message is submitted, takes one token from it, writes the token to
// canary-token.txt at the top of its working tree and commits the file on the
// branch canary/one. It stands where a real agent would, so that the slice can
// be run, and tested, without one.
//
// # What it takes from a message
//
// Only a token: the first line of the pasted text of the exact form
// "canary-token: <token>", where the token is 16 to 64 characters of a-z, 0-9
// and "-". The token is matched against that character set before anything
// uses it. Nothing else of the message is kept, shown to a shell, or run: the
// stand-in starts one program, git, with a fixed argument list and an explicit
// minimal environment, and the token reaches it only as file content.
//
// # Two modes
//
// The mode is chosen at start, by the stand-in itself: replay mode when the
// file replay.on exists in <home>/canary-replay/, plain mode otherwise. So
// the configuration that starts the stand-in is the same in both.
//
// Plain mode draws a prompt, echoes what it receives and, when a bracketed
// paste is followed by a carriage return, commits the token of the paste.
//
// Replay mode holds no screen and names no runtime. It draws two screens that
// it reads from the folder (1.json and 2.json, each with its text), asks the
// terminal the OSC 7501 support query and a DA1 query, records which reply
// came first in order.txt ("no-7501-reply" when none came within the wait,
// and replaced by the true order if one comes later), writes one idle report
// when the support query is answered, draws the second screen after a bracketed paste and, when a
// carriage return follows the paste, commits as plain mode does. The report's
// payload, and so the name of the application in it, comes from the optional
// file report.txt.
//
// # Terminal
//
// Run puts the terminal in raw mode when it is given a descriptor, and
// restores it on return. It returns nil at the end of its input and when its
// context ends.
package standin
