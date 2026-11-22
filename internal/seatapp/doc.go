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
// Every response, an error included, carries the security headers (see
// securityHeaders in app.go). Every request body is cut at [MaxBody] bytes.
// Every POST needs an Origin header equal to [Config].Origin (a fixed 403
// otherwise), the JSON content type, and, when the browser sends one,
// Sec-Fetch-Site same-origin.
//
// # Seams
//
// [Daemon] is the port to the daemon, defined here and implemented later by
// the wiring. A later page adds a template under templates/, a route in [New]
// and its entry in the page inventory; it needs no change in the middleware.
package seatapp
