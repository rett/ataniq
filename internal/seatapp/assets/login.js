// Login: prove with a passkey paired at the desk that this is the owner's
// phone. The seat keeps the session; this script never sees its value. No
// dependency; every text put on the page is set with textContent, never as
// markup.
"use strict";
(function () {
  var root = document.getElementById("login");
  var statusEl = document.getElementById("status");
  var button = document.getElementById("start");

  // Every failure ends in one of these fixed messages; retry says whether
  // the button is offered again.
  var FAILS = {
    unsupported: ["This browser cannot use a passkey. Open the seat in the phone's own browser.", false],
    site_refused: ["The browser refused this site's name for a passkey. Nothing was sent.", false],
    cancelled: ["Cancelled. Nothing was sent. Tap the button to try again.", true],
    network: ["The seat could not be reached. Check the connection, then tap the button to try again.", true],
    challenge_invalid: ["This took too long. Tap the button to try again.", true],
    login_refused: ["The desk did not take this passkey. If this phone is not paired, pair it at the desk.", true],
    not_paired: ["No passkey is paired with the desk yet. Pair this phone at the desk first.", false],
    login_unavailable: ["The desk does not take phone logins now.", false],
    daemon_unavailable: ["The desk cannot be reached. Try again in a moment.", true],
    desk_busy: ["The desk is busy. Try again in a minute.", true],
    rate_limited: ["Too many tries. Wait a few minutes, then try again.", true],
    failed: ["Logging in failed. Tap the button to try again.", true]
  };

  var busy = false;

  function say(text) { statusEl.textContent = text; }

  function fail(kind) {
    var f = FAILS[kind] || FAILS.failed;
    say(f[0]);
    busy = false;
    button.disabled = false;
    button.hidden = !f[1];
  }

  function b64e(buf) {
    var a = new Uint8Array(buf), s = "";
    for (var i = 0; i < a.length; i++) { s += String.fromCharCode(a[i]); }
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function b64d(str) {
    var s = str.replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) { s += "="; }
    var raw = atob(s), a = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) { a[i] = raw.charCodeAt(i); }
    return a;
  }

  // The one fetch helper. As on the pairing page, the request's referrer
  // policy is set here so that a same-origin POST keeps its Origin header.
  // The seat's cookies go with it: the login cookie that holds this
  // browser's challenge, and the session the answer sets.
  async function post(path, body) {
    var resp;
    try {
      resp = await fetch(path, {
        method: "POST", credentials: "same-origin", cache: "no-store", referrer: "", referrerPolicy: "strict-origin",
        headers: { "Content-Type": "application/json" }, body: JSON.stringify(body)
      });
    } catch (e) { throw "network"; }
    var data = null;
    try { data = await resp.json(); } catch (e) { /* a body that is not JSON is a failure below */ }
    if (!resp.ok) { throw data && typeof data.error === "string" ? data.error : "failed"; }
    return data;
  }

  function supported() {
    return !!(window.PublicKeyCredential && navigator.credentials && navigator.credentials.get);
  }

  async function login() {
    var ch = await post("/api/login-challenge", {});
    if (!ch || typeof ch.challenge !== "string" || !Array.isArray(ch.allow) || ch.allow.length === 0) { throw "failed"; }
    var cred;
    try {
      cred = await navigator.credentials.get({ publicKey: {
        challenge: b64d(ch.challenge),
        rpId: root.dataset.rpId,
        allowCredentials: ch.allow.map(function (id) { return { type: "public-key", id: b64d(String(id)) }; }),
        userVerification: "required",
        timeout: 120000
      } });
    } catch (e) {
      if (e && e.name === "NotSupportedError") { throw "unsupported"; }
      if (e && e.name === "SecurityError") { throw "site_refused"; }
      throw "cancelled"; // NotAllowedError: cancelled, timed out, or no consent
    }
    if (!cred || !cred.response) { throw "cancelled"; }
    var r = cred.response;
    var out = await post("/api/login", {
      credential_id: b64e(cred.rawId),
      client_data_json: b64e(r.clientDataJSON),
      authenticator_data: b64e(r.authenticatorData),
      signature: b64e(r.signature)
    });
    // Only a path of the seat itself is followed.
    if (!out || typeof out.next !== "string" || !/^\/[a-z\/]*$/.test(out.next)) { throw "failed"; }
    return out.next;
  }

  button.addEventListener("click", function () {
    if (busy) { return; }
    busy = true;
    button.disabled = true;
    say("Waiting for your phone to ask for your face, finger or passcode.");
    login().then(function (next) {
      say("Logged in.");
      location.assign(next);
    }, function (kind) { fail(typeof kind === "string" ? kind : "failed"); });
  });

  if (!supported()) {
    fail("unsupported");
  } else {
    say("Ready. Tap the button to log in.");
    button.hidden = false;
  }
})();
