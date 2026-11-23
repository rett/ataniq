// Package seatnotice is the protocol between the seat app and the notifier,
// a separate unit that holds the VAPID key and the push subscriptions and is
// the only part that talks to a push service. The seat reaches nothing on the
// network but the gate, so it tells the notifier over a Unix stream socket,
// and the only thing it tells it about the cards is one small integer: how
// many are waiting. No card, id, text or time ever crosses this socket.
//
// The package is the standard library only, and it never imports the push
// code (package webpush): the seat imports this package, and the seat never
// reaches a push service. It holds the types, the codec, a [Client] for the
// seat's side, and [Serve] with a [Handler] for the notifier's side (and
// [FakeHandler] for tests of either).
//
// # The wire
//
// One connection carries one request and one response. A frame is a 4-byte
// big-endian length and then that many bytes of one JSON object; the length
// is 1 to [MaxFrame] (4 KiB). The object is strict: valid UTF-8, no byte
// before or after it, no repeated key at any depth, no key the operation does
// not have, every key matched in its exact case, nested at most 4 levels. A
// request, by its op:
//
//	{"op":"info"}
//	{"op":"notify","n":N}                  N is an integer 0 to 999
//	{"op":"subscribe","sub":{"endpoint":URL,"p256dh":B64,"auth":B64}}
//	{"op":"unsubscribe","endpoint_sha256":HEX}   64 lower-case hex digits
//
// A response is {"ok":true} with the fields the op has, or
// {"ok":false,"code":CODE} with a code of [a-z0-9_]{1,40}:
//
//	info       {"ok":true,"vapid_public":B64,"subscriptions":N}
//	notify     {"ok":true,"sent":K}
//	subscribe, unsubscribe   {"ok":true}
//
// The endpoint is an https URL of at most 2048 bytes; p256dh is the base64url
// (no padding) of a 65-byte uncompressed P-256 point and auth the base64url of
// 16 bytes. The codes the notifier uses are bad_subscription, too_many,
// endpoint_refused and unavailable; the client takes any code of the grammar.
//
// # Deadlines and the peer
//
// The client's call, and the server's turn with a connection, end 2 seconds
// after they begin (see [DefaultDeadline]), except notify, which sends the
// pushes before it answers and has 15 seconds (see [DefaultNotifyDeadline]);
// the server still reads a notify's request within the 2 seconds. The server
// reads the peer's user
// id from the kernel (SO_PEERCRED) and serves only the one id its caller
// allows; on a system without that the server refuses every connection.
package seatnotice
