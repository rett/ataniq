// Package command is the message admission rules and the durable command
// state machine of the lead injection protocol (design sections 4.1, 4.2 and
// 4.5).
//
// This part holds the rules about who may send a message to whom and what a
// message may look like: the typed sender and target, the edge policy, the
// name grammar, the header line and the body rules that keep a body from
// forging a header. It is pure: no I/O, no goroutine and no clock.
package command
