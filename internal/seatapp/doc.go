// Package seatapp is the seat web app: the pages and the small JSON API a
// phone uses to pair with the desk, to log in, to read the cards and to
// approve or deny one, the pages of the guided new-project interview, and
// the seams the journal pages will use later.
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
//	GET  /manifest.webmanifest the web app manifest of the Home Screen app (a PushPort)
//	GET  /sw.js                the service worker that shows a push (a PushPort)
//	GET  /static/push.js       the notices' controls script, the card list's alone (a PushPort)
//	GET  /static/icon-192.png, /static/icon-512.png, /static/apple-touch-icon.png   the icons (a PushPort)
//	POST /api/push/info        the push key and the number of subscriptions (a session, a PushPort)
//	POST /api/push/subscribe   a subscription to hand the notifier (a session, a PushPort)
//	POST /api/push/unsubscribe the hash of an endpoint to forget (a session, a PushPort)
//	GET  /projects/            the open new projects and the start (a session, a Genesis)
//	GET  /projects             a 308 to /projects/
//	POST /projects/new         a form: start a new project (a session, a Genesis)
//	GET  /projects/{thread}    the project's round, its summary, or its finished brief (a session, a Genesis)
//	GET  /projects/{thread}/change/{node}   one answer's question, to answer again (a session, a Genesis)
//	GET  /projects/{thread}/discard         the step before a discard (a session, a Genesis)
//	GET  /projects/{thread}/plan            the project's plan, links only (a session, a Genesis)
//	POST /projects/{thread}/answer, /change, /finish, /discard   forms (a session, a Genesis)
//
// The routes marked PushPort exist only when [Config].Notifier is also a
// [PushPort]; without one they answer 404 and no page mentions notices. The
// routes marked Genesis answer the "not offered" page (a post: a redirect to
// the project list, which answers it) unless [Config].Daemon is also a
// [Genesis] whose calls are offered.
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
// can make a passkey sends it, so a POST without it is refused, and no
// option of [Config] lets one through) (a fixed 403 otherwise), and the JSON
// content type, or, for the forms of the project pages, the form's
// (application/x-www-form-urlencoded). A form's post may also come with no
// Origin header at all, with Sec-Fetch-Site same-origin; "Origin: null" is
// refused. The referrer policy of every response is same-origin, under which
// a browser keeps the Origin header of a form's post to the app (under
// no-referrer it would send "Origin: null"). The relying party id is exactly the
// origin's host. The content security policy includes
// require-trusted-types-for 'script', which the scripts meet by setting text
// only with textContent, with trusted-types naming the one policy push.js
// makes (seat-sw) and no other, and every response carries
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
// address in [Config].ClientAddrHeader when that is set, else the peer. The
// pairings handed to the daemon are limited to 6 in 11 minutes from all
// sources together. The gate does not count a refused pairing against the
// seat's refusal bound, so strangers cannot shut the seat out with them; the
// bound is for the daemon's log, the tries at an open pairing code and the
// room at the gate for anyone's calls. Its cost: strangers who spend it keep
// the owner's own pairing waiting (rate_limited) while they keep it up, never
// the desk.
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
// the login page only when the seat answered the logout with its JSON
// acknowledgement, not redirected (its fetch refuses a redirect); with no
// answer, or another, the session may still be live, and the page says
// "Logout not confirmed" and leaves the button to tap again. At most 16 are kept; a new
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
// The gate takes 60 requests a minute on its connection, pings included,
// and the seat has one connection, which every browser's requests share. The
// login challenges are limited to 10 per source in 15 minutes and to 8 a
// minute from all sources together; a source is the address in
// [Config].ClientAddrHeader, as for the pairing. Only a session can read
// cards, and the reads of one credential's sessions are limited to 20 a
// minute; decision challenges to 10 a minute per credential. These limits do
// not bound the gate's calls by themselves (a login challenge may be answered
// minutes later, and the first page of a card's path list may ask the gate
// twice), so the port counts every call it sends and keeps each kind to its
// own room at the gate (package seatlink): anyone's pairings and logins,
// the Cards pages' reads, the Leads pages' chat calls (a room of their own,
// 5 calls in 80 seconds), the watcher's waits, decisions and the new-project interview (12 calls in
// 80 seconds: one for each tap of the owner's, and a few reads), which the
// others cannot take, and a decision's
// challenge only with room for the decision too, kept for that challenge
// until its decision is sent, and a login challenge only with room for its
// login, so a login is not refused for room after the passkey. The app
// tells a port that keeps such room ([ChallengeDropper]) of each decision's
// challenge whose decision it will not send. A call without room is
// [ErrBusy], and nothing is sent. When the port or the gate says the rate is
// spent, the phone says the desk is busy and to try again in a minute;
// nothing is retried by itself.
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
// ([Whole], which also says whether the header's names and the lines of
// the plan and of the note were shown whole), and a cut path list by the
// pages of it the session was served that cut no path, each with the review
// digest it was read for. The port withholds Approve for a part not shown
// whole. A HEAD of a card page is
// refused 405: only a GET answered with its page is a read, and only once
// the page was written and flushed without an error. A Card page's read
// (which settles a lost answer and keeps the page's form) is checked again
// then, and is not noted when a decision of the card was sent, or an answer
// lost, since the read began.
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
// the card is marked sent, and in flight, before the port is asked; a lost
// answer marks it again and ends the flight, and only a new read of the
// card (its page, from a read of the port that began after the answer was
// lost; the script offers only "Look again") clears the mark,
// so a lost answer is never followed by a second decision unseen and
// nothing is queued. A held approve shows the host's line and the held
// words. Every failure is a fixed code with the app's fixed words ("say");
// a passkey that is no longer paired ends the session. A port that answers
// [ErrDecisionsInhibited] (on the authority host, while its decision inhibit
// is there) sent nothing: the phone shows "Decisions are paused on this
// host. Nothing was sent." under the code decisions_inhibited, and the card
// stays as it was. While the port's Offer says Inhibited, the Card page of a
// pending card shows "Decisions are paused on this host." and no Approve or
// Deny form; the card stays readable. Decision challenges are limited to 10
// a minute per credential. Both bodies fit [MaxBody]
// (8192 bytes): the largest the page sends is 1230 bytes for a challenge
// (the form's id, 300 sign-off indexes and a card id of 51 characters; a
// deny's reason of 500 bytes, every one escaped, is 1138) and 5672 bytes for
// an assertion at the wire's bounds.
//
// # New projects
//
// When the [Daemon] is also a [Genesis] and its list of open projects is
// offered, the card list shows the projects in progress (from the list, which
// the port may answer from its memory of the last one) and a "New project"
// button; without it, or when the port answers [ErrNotOffered], the card list
// shows nothing of projects and every project page answers "New projects are
// not offered". A project is a thread of the guided interview the daemon
// keeps (seat.genesis): the owner answers rounds of one to three questions of
// the host's catalog, reads the summary, changes an answer, and finishes or
// discards it. Finishing keeps the answers as the project's brief, at the
// desk, and accepts the answers decided for the owner, which the summary
// shows right above the finish button and says so; nothing starts, and no
// agent is given the brief.
//
// The pages are made on the server, with no script but the logout's, and
// each act is a plain HTML form posted to the app: a start, the answers of
// a round, the change of one answer, a finish, a discard (after a step of its
// own). A post passes the checks of every changing request (the Origin and
// Host headers, Sec-Fetch-Site same-origin), with the form's content type,
// and needs a session; it calls the port once and is answered with a 303 to
// a page (post/redirect/get), so a reload never sends a form again and a
// refusal is never retried. A question is a fieldset of real inputs: radio
// buttons for a single choice, a yes-or-no question and a scale of 1 to 5,
// check boxes for a multiple choice, each option with its label and its
// example as text; "Other" with a text field for a short note (200 bytes,
// "about 200 letters" on the page, maxlength 200) only where the question
// takes the owner's own words, and "You decide for me" and
// "Skip" only where it allows them. A form whose fields do not agree (two
// kinds at once, own words with an option, words that are not plain text or
// are past 200 bytes) is refused before the port is asked. Every refusal is
// one of a fixed set of notes, named by a word in the redirect's query, on
// the page it redirects to; a page never shows a refused answer's text, and
// what the owner typed is in no address, page or log line. A thread id in an
// address is 16 lower-case hexadecimal digits and a node id of the catalog's
// grammar, or the page is a 404 (a post goes to the project list) and nothing
// is asked of the port. A summary shows the three groups (what you chose,
// what was decided for you, the defaults), the owner's own words quoted and
// marked as such, and a "Change" link for each row whose question the port
// holds. The pages and posts of one credential are limited to 30 a minute;
// the port keeps the interview to its own room at the gate. No project page
// holds an approve or decide control, and the page inventory says so.
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
// Every page behind a session has the main navigation: a "Skip to content"
// link first, then, in the header, <nav aria-label="Main"> of plain links
// ("Cards", and "Projects" while new projects are offered) with
// aria-current on the link of the page's own section, then the content in
// <main id="content">. It is a page's navView; the public pages and the
// problem pages have none. The Card page and the path pages ask the daemon
// nothing of the interview: they show "Projects" by the card list's last
// answer. The navigation holds no button, form or script, so it adds no
// logout control: the plan page keeps no script and no button. nav_test.go
// holds every page to it. On a phone it is a bar at the top, in the page's
// flow. The layout gives the body of those pages the class nav-page, and on
// a screen at least 60rem wide the style sheet alone makes their header a
// sticky rail on the left beside a column of about 44rem (60rem for a list
// of cards); the markup and its order are the same at every width, and the
// public pages keep their one column.
//
// # Seams
//
// [Daemon] is the port to the daemon, defined here and implemented over the
// seat gate by package seatlink, which this package does not import. A later
// page adds a template under templates/, a route in the route table and its
// entry in the page inventory (inventory_test.go, which
// fails for a route that is not listed); it needs no change in the middleware.
// [Genesis] is an optional port beside it, found by a type assertion.
//
// An approve control exists in exactly one place, the Card page reached from
// the Cards list. No other page, notice, link or message may hold or open a
// decide control, and only the Card page loads the decide script. The
// inventory test fetches every page, the card pages with a session, and
// checks each for an inline script or style, an event handler, an address
// outside the app (in href, src, action, formaction, srcset, poster, data,
// ping, background, cite or manifest, or a meta element's content), a meta
// element with http-equiv, a form or control it does not list, a form that
// is not a plain post to a project route, and, on every page but the Card
// page, anything that looks like deciding.
package seatapp
