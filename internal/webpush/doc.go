// Package webpush sends Web Push messages: the VAPID authentication of an
// application server (RFC 8292), the aes128gcm message encryption (RFC 8291,
// with the content coding of RFC 8188) and the one POST to a push service
// (RFC 8030).
//
// It is the only part of the notices that talks to a push service. The seat
// never imports it (imports_test.go holds the seat's command and packages to
// that): the seat hands a count to a separate notifier, and only the notifier
// holds the VAPID key and the subscriptions and reaches the network.
//
// The package is the standard library only, in its tests as well. It uses
// package os in keyfile.go alone, for the key file, and package net/http in
// send.go alone.
//
// # VAPID
//
// A [VAPIDKey] is an ECDSA P-256 key. [GenerateVAPIDKey] makes one,
// [WriteVAPIDKeyFile] writes it as a PKCS#8 PRIVATE KEY block to a new file
// of mode 0600 (it never overwrites), and [LoadVAPIDKeyFile] reads it back,
// holding the folder and the file to the checks the gate's identity files
// have: a real folder that group and other cannot write; a regular file, not
// a link of either kind, of at most 16 KiB, that group and other cannot read
// or write. [VAPIDKey.PublicKeyString] is the key a browser takes as
// applicationServerKey.
//
// Each message carries a fresh ES256 JWT with three claims: aud, the origin
// of the endpoint; exp, at most 12 hours after the sender's clock; and sub,
// the configured contact (a mailto: or https: URI). It goes in the header
//
//	Authorization: vapid t=<JWT>, k=<public key, base64url>
//
// # Encryption
//
// [Encrypt] encrypts a payload to a subscription's p256dh key and auth
// secret as one aes128gcm record with record size 4096: a header of salt,
// record size, key id length and the ephemeral public key (86 bytes), then
// the payload, the last-record delimiter 0x02 and the 16-byte tag. A payload
// over [MaxPayload] (3993) bytes is refused: the body would not fit the
// 4096 bytes a push service must accept. The salt and the ephemeral key come
// from a reader the caller gives, so a test can reproduce the vector of RFC
// 8291 Appendix A byte for byte.
//
// # Sending
//
// [Sender.Send] posts the encrypted body to the subscription's endpoint with
// the headers Content-Encoding: aes128gcm, TTL, and Urgency and Topic when
// they are set. Before it encrypts anything it refuses an endpoint that is
// not https, that has credentials or a fragment, whose host is an IP literal
// or whose port is not 443, or whose host the [HostPolicy] does not accept;
// the package hard-codes no push service, the caller's policy names them
// ([AllowHosts] accepts exactly the hosts of a list). [Config.TestEndpoints]
// lifts the IP-literal and port refusals for tests against a local server and
// nothing else.
//
// The sender follows no redirect, sends no cookie, bounds the whole request
// by [Config.Timeout] and reads at most 4 KiB of a response body. The answer
// maps to:
//
//	201              nil
//	404, 410         a *StatusError that is ErrGone: drop the subscription
//	413              a *StatusError that is ErrEntityTooLarge
//	429              a *StatusError that is ErrRateLimited, with RetryAfter
//	500 to 599       a *StatusError that is ErrServer, with RetryAfter
//	anything else    a *StatusError that is ErrUnexpectedStatus
//
// No error text holds the endpoint, which is a bearer capability for the
// subscription, nor anything the push service sent.
package webpush
