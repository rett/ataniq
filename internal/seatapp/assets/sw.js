// The service worker of the seat's notices. It does two things: it shows a
// notification when a push arrives, and it opens the Cards list when the
// notification is tapped. It has no handler for requests, keeps nothing in
// any store and loads no other script; the page registers it and nothing
// more is asked of it.
//
// A notice never acts on a card and carries nothing of one. The push
// holds at most {"n": N}, how many cards wait, and this worker reads that one
// member and shows it as text it made itself. A push that holds anything else
// (another member, another type, no body, a body that is not JSON) shows the
// plain text. The notification has no buttons and no data, and a tap opens
// the Cards list, a fixed address, whatever the push said.
"use strict";

var PLAIN = "Cards are waiting";
var SUFFIX = " waiting";
var CARDS_PATH = "/cards/";
var TAG = "waiting";
var MAX_N = 999;

// titleOf is the text to show for a push: "N waiting" when the body is an
// object whose one member is n, an integer 0 to 999, and the plain text for
// every other push.
function titleOf(event) {
  try {
    var body = event.data.json();
    if (body !== null && typeof body === "object" && !Array.isArray(body)) {
      var names = Object.keys(body);
      if (names.length === 1 && names[0] === "n") {
        var n = body.n;
        if (typeof n === "number" && Number.isInteger(n) && n >= 0 && n <= MAX_N) {
          return String(n) + SUFFIX;
        }
      }
    }
  } catch (e) {
    return PLAIN;
  }
  return PLAIN;
}

self.addEventListener("push", function (event) {
  event.waitUntil(self.registration.showNotification(titleOf(event), { tag: TAG, renotify: false }));
});

// openCards takes a window of this origin that is open, if there is one, to
// the Cards list and focuses it; otherwise it opens a window on the list.
// The windows asked for include those the worker does not control (the page
// that registered it stays uncontrolled until it is loaded again, and the
// worker does not take control of it), so a tap works at the first notice too. Such a
// window cannot be navigated by a worker, so navigate() comes first, before
// anything that uses up the tap's one permission to show a window, and when
// it is refused the list is opened in a window of its own. Only a window that
// was navigated is focused.
function openCards() {
  return self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(function (windows) {
    if (windows.length > 0) {
      return windows[0].navigate(CARDS_PATH).then(function (w) {
        return w ? w.focus() : w;
      });
    }
    return self.clients.openWindow(CARDS_PATH);
  }).catch(function () {
    return self.clients.openWindow(CARDS_PATH);
  });
}

self.addEventListener("notificationclick", function (event) {
  event.notification.close();
  event.waitUntil(openCards());
});
