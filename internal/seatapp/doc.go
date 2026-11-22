// Package seatapp is the seat web app: the pages and the small JSON API a
// phone uses to pair with the desk, and the seams the cards, decisions,
// sessions and journal pages will use later.
//
// The app is the standard library only. Pages are made on the server with
// html/template, each act a plain form where an act is a form; the one script
// of the pairing page and the one style sheet are compiled into the binary
// with go:embed and served from the app's own origin. Nothing is fetched from
// another site, there is no inline script and no inline style, and no cookie
// is set.
//
// # Routes
//
//	GET  /healthz             "ok"
//	GET  /                    a plain status page
//	GET  /pair                the pairing page (the code is never in the path or query)
//	GET  /static/pair.js      the pairing script
//	GET  /static/app.css      the style sheet
//	POST /api/pair-challenge  a one-time challenge for a new passkey
//	POST /api/pair            the passkey's data and the one-time code
//
// The origin is the reverse proxy's, https on port 443: [New] refuses any
// other (an http origin, a port, a path, user information, an address
// literal), and the only way around it is a test option that takes a loopback
// host and that no command sets. The app itself listens on plain HTTP behind
// the proxy.
//
// Every response, an error included, carries the security headers (see
// securityHeaders in app.go). Every request body is cut at [MaxBody] bytes.
// Every POST needs an Origin header equal to [Config].Origin (a fixed 403
// otherwise), the JSON content type, and, when the browser sends one,
// Sec-Fetch-Site same-origin.
//
// # Pairing
//
// The phone scans a QR whose address carries the one-time code in the URL
// fragment, so the code is in no log. The page's script reads the fragment and
// clears it with history.replaceState, asks for a challenge (32 random bytes,
// kept 5 minutes, single use, at most 32 outstanding), makes a passkey with
// user verification required and attestation "none", and posts the code and
// the new public key. The server burns the challenge first, then checks the
// browser's own data with no passkey library and no CBOR decoder: the client
// data (type, challenge, origin, no cross-origin), the authenticator data
// (relying party hash, user presence and verification, the attested
// credential id equal to the posted one) and the public key (DER SPKI of a
// P-256 key). Then it hands the code, the credential id and the key to the
// [Daemon] port, and the six words that come back are shown on the phone to be
// compared with the desk console. Nothing is trusted until the owner confirms
// at the desk; this package adds no credential to anything.
//
// A failure is one of a small fixed set of codes, each with its status, and a
// response never holds anything the request sent. Pairing tries are limited
// to 5 per source address per 15 minutes, in memory; the source is the
// address in [Config].ClientAddrHeader when that is set, else the peer.
//
// # Text nobody on the owner's side wrote
//
// [Text] is the one function that renders such text (the six words from the
// daemon today, a lead's note later): plain text, no link made, no markup, no
// image; control and bidirectional characters shown as visible escapes; long
// text cut with a count of what was cut. It is registered in the page
// templates as "text", and a page shows no such text any other way.
//
// # Seams
//
// [Daemon] is the port to the daemon, defined here and implemented later by
// the wiring. A later page adds a template under templates/, a route in the
// route table and its entry in the page inventory (inventory_test.go, which
// fails for a route that is not listed); it needs no change in the middleware.
//
// An approve control exists in exactly one place, the Card page reached from
// the Cards list. No other page, notice, link or message may hold or open a
// decide control. No such page exists yet, and the inventory test checks every
// page for an inline script or style, an event handler, an address outside the
// app, a form or control it does not list, and anything that looks like
// deciding.
package seatapp
