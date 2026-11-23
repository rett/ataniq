// Package seatapp is the seat web app: the pages and the small JSON API a
// phone uses to pair with the desk, to log in, to read the cards and to
// approve or deny one, and the seams the journal pages will use later.
//
// The app is the standard library only. Pages are made on the server with
// html/template, each act a plain form where an act is a form; the scripts
// of the pairing and login pages and the one style sheet are compiled into
// the binary with go:embed and served from the app's own origin. Nothing is
// fetched from another site, and there is no inline script and no inline
// style. The only cookies are the session's and the login's (see Sessions),
// set by the login's API and by no page (a decision's API only removes the
// session's, when the daemon says its passkey is no longer paired).
//
// # Routes
//
//	GET  /healthz              "ok"
//	GET  /                     a plain status page
//	GET  /pair/                the pairing page (the code is never in the path or query)
//	GET  /pair                 a 308 to /pair/
//	GET  /static/words.js      the words' list and formula
//	GET  /static/pair.js       the pairing script
//	GET  /static/app.css       the style sheet
//	POST /api/pair-challenge   a one-time challenge for a new passkey
//	POST /api/pair             the passkey's data and the one-time code
//	GET  /login/               the login page
//	GET  /login                a 308 to /login/
//	GET  /static/login.js      the login script
//	POST /api/login-challenge  the daemon's login challenge and its allow-list
//	POST /api/login            the passkey's assertion over it; starts a session
//	POST /api/logout           ends the session
//	GET  /cards/               the card list (a session)
//	GET  /cards                a 308 to /cards/
//	GET  /cards/{id}           one card, with its decide form when it is pending (a session)
//	GET  /cards/{id}/paths/{n} page n of a card's whole path list (a session)
//	GET  /static/session.js    the logout script
//	GET  /static/decide.js     the decide script, which only the Card page loads
//	POST /api/decide-challenge the daemon's challenge for one decision (a session)
//	POST /api/decide           the passkey's assertion over it (a session)
//
// The origin is the reverse proxy's, https on port 443: [New] refuses any
// other (an http origin, a port, a path, user information, an address
// literal), and the only way around it is a test option that takes a loopback
// host and that no command sets. The app itself listens on plain HTTP behind
// the proxy.
//
// Every response, an error included, carries the security headers (see
// securityHeaders in app.go). Every request body is cut at [MaxBody] bytes.
// Every POST needs an Origin header equal to [Config].Origin, a Host header
// equal to the origin's host, Sec-Fetch-Site same-origin (every browser that
// can make a passkey sends it, so a POST without it is refused) (a fixed 403
// otherwise), and the JSON content type. The relying party id is exactly the
// origin's host. The content security policy includes
// require-trusted-types-for 'script', which the scripts meet by setting text
// only with textContent, and every response carries
// Strict-Transport-Security. The Permissions-Policy allows
// publickey-credentials-create to the app's own origin, and
// publickey-credentials-get to the app's own origin on the login page and on
// a Card page that holds the decide form only; every other response forbids
// it.
//
// # Pairing
//
// The phone scans a QR whose address carries the one-time code in the URL
// fragment, so the code is in no log. The page's script reads the fragment and
// clears it with history.replaceState, asks for a challenge (32 random bytes,
// kept 5 minutes, single use, at most 32 outstanding), makes a passkey with
// user verification required and attestation "none", and posts the code and
// the new public key. The server burns the challenge first, then checks the
// browser's own data with no passkey library: the client data (type,
// challenge, origin, no cross-origin), the authenticator data (relying party
// hash, user presence and verification, backup state consistent, the attested
// credential id equal to the posted one) and the public key, which it reads
// from the attested credential data with a small strict CBOR reader of its own
// (cose.go: the five-entry ES256 COSE_Key of a P-256 point on the curve, in
// canonical order, definite lengths only) and turns into its canonical DER
// SPKI, which must equal the posted public key byte for byte. Then it hands
// the code, the credential id and the key to the [Daemon] port, with the
// authenticator data (at most 4096 bytes) and the relying party id, from which
// the daemon reads the kind the authenticator's AAGUID claims and its backup
// flags (claims, not facts: nothing is attested), and the
// [WordCount] words that come back are shown on the phone to be compared with the desk
// console. Nothing is trusted until the owner confirms at the desk; this
// package adds no credential to anything.
//
// The attestation is "none", so the registration holds no signature: the key
// is bound to the credential id and to the authenticator data, and is not
// signed. What the checks give is that the id, the authenticator data and the
// key the daemon hears of are one credential's, not that the phone proved it
// holds the private key.
//
// # The words
//
// The script computes the [WordCount] words itself from the credential the
// phone made (the first eight bytes of SHA-256 of the 4-byte big-endian length
// and the credential id, then the same for the SPKI; each byte indexes the
// 256-word list in assets/words.js, the list of the seatpair package, which a test in
// tests/ holds equal). It shows its own words, and if the words the server
// returns differ it shows a fixed warning and does not offer the comparison.
// That protects the link between the app and the daemon, and an honest
// authority host behind a bad relay. It does not protect against a host that
// is taken over: that host also serves the script, so it can serve one that
// shows whatever it likes. Pair a phone only while the host's no-AI check is
// green, and use a hardware key, with its attestation verified, for a real
// project.
//
// A failure is one of a small fixed set of codes, each with its status, and a
// response never holds anything the request sent. Pairing tries are limited
// to 5 per source address per 15 minutes, in memory; the source is the
// address in [Config].ClientAddrHeader when that is set, else the peer.
//
// # Login
//
// A login proves that the phone holds a credential the owner confirmed at the
// desk; it decides nothing. The login page asks POST /api/login-challenge,
// which asks the [Daemon] for a login challenge. The port recomputes the
// challenge from the gate's key and the daemon's nonce before the app sees it
// (package seatlink), so the phone signs only a challenge the daemon drew for
// this gate. The app keeps the challenge's id server-side, under a new login
// cookie of the browser that asked, and sends the browser only the challenge
// and the allow-list: the raw ids of the paired credentials, which the page
// hands to navigator.credentials.get as allowCredentials, with user
// verification "required". No account chooser is shown, and a credential
// that is not on the list is not offered. POST /api/login takes the
// assertion only with that browser's login cookie: the challenge kept with it
// is burnt first, whatever comes next, and only then is the assertion read
// and handed to the daemon with the kept id, never one the browser sent. The
// daemon verifies it; the app checks only its form and bounds.
//
// When there is no paired credential, the phone is told so (not_paired) and
// nothing is kept. A login challenge lives at most 5 minutes, or less when
// the daemon says so; at most 16 are kept, and a new one drops the oldest, as
// the daemon's store does, so a login challenge is never refused for room.
//
// # Sessions
//
// A login the daemon accepts starts a session. Its cookie, [SessionCookie],
// holds a 256-bit random value the app draws at the login, never one the
// browser brought: a session cookie that came with the login request is ended
// first, so a value planted before the login is never a session (session
// fixation). The value is kept only server-side, as its SHA-256, in memory;
// a restart of the seat ends every session. A cookie whose value is not of
// the form the app makes is not looked up. A session ends 30 minutes after
// its last request ([SessionIdle]) and 12 hours after the login whatever its
// use ([SessionMax]), and at POST /api/logout. The logout script goes to
// the login page only when the seat answered the logout; with no answer, or
// another, the session may still be live, and the page says "Logout not
// confirmed" and leaves the button to tap again. At most 16 are kept; a new
// one drops the one used least recently.
//
// Both cookies are __Host- cookies, Secure, HttpOnly and SameSite=Strict,
// with the path / and no domain: only this host can set them, they travel
// only over TLS, no script reads them, and a request another site starts
// does not carry them. The session's cookie has no Max-Age and ends with the
// browser's session; the login's lives 5 minutes. A request with two cookies
// of one name has none.
//
// # The gate's rate
//
// The gate takes 60 requests a minute on its connection, and the seat has one
// connection, which every browser's requests share. So the seat spends that
// rate on the owner. Every login asks the gate twice (the challenge and the
// login), and the login challenges are limited to 10 per source in 15
// minutes and to 8 a minute from all sources together, so logins, which
// anyone who reaches the login page can start, take at most 16 of the 60 a
// minute. A source is the address in [Config].ClientAddrHeader, as for the
// pairing. Only a session can read cards, and the reads of one credential's
// sessions are limited to 20 a minute (a page asks the gate once, and the
// first page of a card's path list may ask twice), so that logins and one
// phone's reads stay within the 60, with room left for decisions. When the
// gate answers that its rate is spent, the phone says the desk is busy and to
// try again in a minute; nothing is retried by itself.
//
// # The cards
//
// The card pages need a session: a request without a live one is sent to the
// login page (a 303), and nothing of the page, and no call to the daemon, is
// made for it. Every response, these included, carries Cache-Control:
// no-store. The list shows the waiting cards and the recent ones; a card
// shows its sections in the order the card model gives them (the facts, what
// came after, the plan, the refs, the changes and their rows, the four lists,
// the flagged paths, the evidence, the lead's note labelled "Lead-stated, not
// checked", and how it differs from an earlier card), and links to the pages
// of its whole path list when its rows are cut. Each page says that the desk
// runs a STAND-IN door on scratch state and that nothing on it is a real
// approval; the list and the path pages say that approve and deny are on a
// card's own page or at the desk. A card id or page number that is not of
// its form is a 404 and is not asked of the daemon.
//
// The [Daemon] port builds a card from the record's bytes with the card model
// and checks their digest (package seatlink); the app takes a [Card] of plain
// values and never markup. Text a lead wrote is a [Lead], whose only way onto
// a page is [Text].
//
// # Decisions
//
// The Card page of a pending card (no decision on its way) holds the decide
// form, when the port's Offer says the card model allows approve or deny:
// one sign-off box per flagged path and Approve, offered when MayApprove
// allows it and enabled once every box is ticked; Deny, offered when the
// card model's Deny allows it, with an optional reason, enabled whatever is
// ticked, which sends no sign-off. The phone reads a card on one page, so
// the session's reading of it (kept server-side, see [Reading]) counts the
// page's rows, lists, refs and "differs from" block once the page was
// served, each only when the page showed every path and ref of it whole
// ([Whole]), and a cut path list by the pages of it the session was served
// that cut no path, each with the review digest it was read for. The port
// withholds Approve for a part not shown whole. A HEAD of a card page is
// refused 405: only a GET answered with its page is a read.
//
// A tap asks POST /api/decide-challenge with the card, the id of the page's
// form, the action, the indexes of the boxes ticked and the reason; the
// browser never sends a path or a binding. Each Card page with the form has
// a form id of its own, which the app keeps with the binding that page
// showed; the app takes the binding the session read last from its own
// reading, and takes the tap only when its form's page showed that binding,
// so a tab left open on a card that was replaced since is told to look
// again before any challenge is asked. It hands the port the rest; the port freezes
// the decision from the card it showed, asks the daemon for its challenge
// and recomputes it before the app sees it (package seatlink). The app keeps
// the frozen frame and the challenge's id with the session, one decision at
// a time (a new tap replaces the old), and answers the challenge with the
// session's one credential as the allow-list. The page then names the
// action, the card, its project, what it does, its target and its revision
// (the binding's first 12 digits), and only a second tap, on the confirm
// button, asks navigator.credentials.get for one assertion, with user
// verification "required". A cancel or a failed prompt sends nothing and
// leaves the card as it was. POST /api/decide carries the challenge's id
// and the assertion: the kept decision is burnt first, the assertion is
// taken only for its id and the session's credential, and the kept frame is
// what is sent. A card past its expiry gets no challenge.
//
// Once a decision is sent, only a refusal says that nothing was decided.
// Any other answer, and no answer, is "Decision sent. Outcome unknown.":
// the card is marked sent before the port is asked, and only a new read of
// the card (its page, from a read of the port that began after the mark;
// the script offers only "Look again") clears the mark,
// so a lost answer is never followed by a second decision unseen and
// nothing is queued. A held approve shows the host's line and the held
// words. Every failure is a fixed code with the app's fixed words ("say");
// a passkey that is no longer paired ends the session. Decision challenges
// are limited to 10 a minute per credential. Both bodies fit [MaxBody]
// (8192 bytes): the largest the page sends is 1230 bytes for a challenge
// (the form's id, 300 sign-off indexes and a card id of 51 characters; a
// deny's reason of 500 bytes, every one escaped, is 1138) and 5672 bytes for
// an assertion at the wire's bounds.
//
// # Text nobody on the owner's side wrote
//
// [Text] is the one function that renders such text (the words from the
// daemon, a card's host fields, a lead's paths, plan and note): plain text, no
// link made, no markup, no image; control and bidirectional characters shown
// as visible escapes; long text cut with a count of what was cut. It is
// registered in the page templates as "text", and as "lead" for a [Lead], and
// a page shows no such text any other way: a test reads every template and
// fails for an action that writes a value by any other function, but for the
// few the app fills itself (a page's title, a problem's fixed words, the
// relying party, the app's own addresses and the id of a page's form).
//
// # The look
//
// Phone first, operate mode: calm, plain, high contrast, one column. One
// style sheet (assets/app.css), light and dark by prefers-color-scheme, 16 px
// side gutters, no horizontal scroll at 320 px, 44 px touch targets, a visible
// focus, text of at least 16 px, system fonts only, no framework. Tests check
// the rules a test can: the viewport, the language, a title and the landmarks
// of each page, and the style sheet's sizes and contrast.
//
// # Seams
//
// [Daemon] is the port to the daemon, defined here and implemented over the
// seat gate by package seatlink, which this package does not import. A later
// page adds a template under templates/, a route in the route table and its
// entry in the page inventory (inventory_test.go, which
// fails for a route that is not listed); it needs no change in the middleware.
//
// An approve control exists in exactly one place, the Card page reached from
// the Cards list. No other page, notice, link or message may hold or open a
// decide control, and only the Card page loads the decide script. The
// inventory test fetches every page, the card pages with a session, and
// checks each for an inline script or style, an event handler, an address
// outside the app, a form or control it does not list, and, on every page
// but the Card page, anything that looks like deciding.
package seatapp
