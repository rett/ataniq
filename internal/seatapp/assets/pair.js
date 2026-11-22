// Pairing: make a passkey on this phone and send its public key and the
// one-time code to the seat. No dependency; every text put on the page is set
// with textContent, never as markup.
"use strict";
(function () {
  var root = document.getElementById("pair");
  var statusEl = document.getElementById("status");
  var button = document.getElementById("start");
  var result = document.getElementById("result");
  var wordsEl = document.getElementById("words");

  // Every failure ends in one of these fixed messages; retry says whether the
  // same code can be tried again on this page.
  var FAILS = {
    nocode: ["This page needs the code from the desk. Scan the QR code on the desk console again.", false],
    unsupported: ["This browser cannot make the passkey this needs. Open the link in the phone's own browser and scan the code again.", false],
    cancelled: ["The passkey was cancelled or timed out. Nothing was paired. Tap the button to try again.", true],
    network: ["The seat could not be reached. Check the connection, then tap the button to try again.", true],
    challenge_invalid: ["This try expired. Tap the button to try again.", true],
    daemon_unavailable: ["The desk could not be reached. Try again in a moment.", true],
    code_refused: ["The desk did not accept this code. It may have expired or been used. Start a new pairing at the desk and scan its code.", false],
    rate_limited: ["Too many tries. Wait a while, then scan the code at the desk again.", false],
    failed: ["Pairing failed. Nothing was paired. Start a new pairing at the desk.", false]
  };

  var code = readCode();
  var busy = false;

  function readCode() {
    var c = location.hash.replace(/^#/, "");
    try { c = decodeURIComponent(c); } catch (e) { c = ""; }
    // The code is never kept in the address bar or the history.
    history.replaceState(null, "", location.pathname + location.search);
    return /^[\x21-\x7e]{1,64}$/.test(c) ? c : "";
  }

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

  // The one fetch helper. The page's own Referrer-Policy header would make the
  // default policy of a request one that sends "Origin: null" on a same-origin
  // POST, which the seat refuses, so the policy is set here: strict-origin
  // keeps the Origin header, and the empty referrer sends no Referer.
  async function post(path, body) {
    var resp;
    try {
      resp = await fetch(path, {
        method: "POST", credentials: "omit", cache: "no-store", referrer: "", referrerPolicy: "strict-origin",
        headers: { "Content-Type": "application/json" }, body: JSON.stringify(body)
      });
    } catch (e) { throw "network"; }
    var data = null;
    try { data = await resp.json(); } catch (e) { /* a body that is not JSON is a failure below */ }
    if (!resp.ok) { throw data && typeof data.error === "string" ? data.error : "failed"; }
    return data;
  }

  function supported() {
    return !!(window.PublicKeyCredential && navigator.credentials && window.crypto &&
      window.AuthenticatorAttestationResponse &&
      AuthenticatorAttestationResponse.prototype.getPublicKey &&
      AuthenticatorAttestationResponse.prototype.getAuthenticatorData);
  }

  async function pair() {
    var ch = await post("/api/pair-challenge", {});
    if (!ch || typeof ch.challenge !== "string") { throw "failed"; }
    var cred;
    try {
      cred = await navigator.credentials.create({ publicKey: {
        rp: { id: root.dataset.rpId, name: root.dataset.rpName },
        user: { id: crypto.getRandomValues(new Uint8Array(16)), name: "owner", displayName: "owner" },
        challenge: b64d(ch.challenge),
        pubKeyCredParams: [{ type: "public-key", alg: -7 }],
        authenticatorSelection: { userVerification: "required", residentKey: "preferred" },
        attestation: "none",
        timeout: 120000
      } });
    } catch (e) {
      throw e && e.name === "NotSupportedError" ? "unsupported" : "cancelled";
    }
    if (!cred || !cred.response) { throw "cancelled"; }
    var r = cred.response, key = r.getPublicKey();
    if (!key) { throw "unsupported"; }
    var out = await post("/api/pair", {
      code: code,
      challenge: ch.challenge,
      credential_id: b64e(cred.rawId),
      client_data_json: b64e(r.clientDataJSON),
      authenticator_data: b64e(r.getAuthenticatorData()),
      public_key: b64e(key)
    });
    if (!out || !Array.isArray(out.words) || out.words.length !== 6) { throw "failed"; }
    return out.words;
  }

  function show(words) {
    wordsEl.textContent = "";
    words.forEach(function (w) {
      var li = document.createElement("li");
      li.textContent = String(w);
      wordsEl.appendChild(li);
    });
    say("Paired on this phone, waiting for you at the desk.");
    button.hidden = true;
    result.hidden = false;
  }

  button.addEventListener("click", function () {
    if (busy) { return; }
    busy = true;
    button.disabled = true;
    say("Waiting for your phone to ask for your face, finger or passcode.");
    pair().then(show, function (kind) { fail(typeof kind === "string" ? kind : "failed"); });
  });

  if (!code) {
    fail("nocode");
  } else if (!supported()) {
    fail("unsupported");
  } else {
    say("Ready. Tap the button to make a passkey on this phone.");
    button.hidden = false;
  }
})();
