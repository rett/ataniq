// Package pairview is the console's screen for pairing a phone from the desk.
//
// The owner opens it with P. It explains in a few lines what pairing is and
// offers Enter to start. A start asks the daemon for a one-time code
// (Backend.PairStart); the view builds the address Origin + "/pair/#" + code,
// encodes it as a QR code (package qr, level M) and draws it, black on white
// with a quiet zone, whatever the console's theme is, with the seconds the
// code has left. The phone scans it, makes its passkey and sends its public
// key through the app; the daemon then shows the key as six short words. The
// view polls Backend.PairStatus every two seconds, and on the Pending phase
// shows the six words large and spaced with the line "Compare with your
// phone. Confirm only if they match." Only the owner's c and then y confirm
// (Backend.PairConfirm); x rejects (Backend.PairReject). Nothing is trusted
// until the owner confirms at the desk.
//
// The code is a secret for ten minutes. It appears only inside the drawn QR
// modules: the view keeps the drawn rows and drops the code and the address,
// and never writes either as text, into a status line, an error or a title.
// Every string from the backend that is drawn (the words, the id, the kind)
// is untrusted and is cleaned first; the text of a backend's error is never
// drawn, a fixed line stands for it. Six words that are not each one short
// printable word cannot be confirmed, only rejected.
//
// The origin is configuration. The view refuses to draw anything but the
// fixed line "pairing origin not configured" unless it is https with a host
// name (not an address), no userinfo, no path, and no port but 443 (see
// ValidOrigin); it then starts no pairing.
//
// Esc closes the view at any time and confirms nothing. Closing while the
// code is waiting does not cancel the pairing on the daemon: the code ends by
// itself when it expires. Opening the view again reads the daemon's state: a
// pairing already waiting for its phone cannot show its code a second time
// and offers a new code instead, and one whose words are waiting for the
// owner is shown at once.
//
// The view calls the backend only from a command (tea.Cmd), never from
// Update, SetSize or Draw, so a slow backend cannot hold the keys. Of this
// repository the package imports only the QR encoder, and none of os, net or
// os/exec.
package pairview
