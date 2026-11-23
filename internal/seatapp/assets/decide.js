// Decide: approve or deny the card this page shows, with one fresh passkey
// assertion for each decision the owner taps. Only the Card page loads this
// script. The seat keeps the decision server-side; the browser sends only
// the id of this page's form, the action, the indexes of the flagged paths
// signed off, the reason, and then the assertion. The form's id ties the tap
// to the card this page shows: a page left open after the card was replaced
// is told to look again, before any challenge is asked. Nothing is sent by itself, nothing is sent twice, and
// nothing is kept to send later. No dependency; every text put on the page
// is set with textContent, never as markup.
"use strict";
(function () {
  var root = document.getElementById("decide");
  if (!root) { return; }
  var statusEl = document.getElementById("decide-status");
  var approveBtn = document.getElementById("approve");
  var denyBtn = document.getElementById("deny");
  var reasonEl = document.getElementById("reason");
  var reasonLabel = document.getElementById("reason-label");
  var signoffs = document.getElementById("signoffs");
  var boxes = Array.prototype.slice.call(root.querySelectorAll("input.signoff"));
  var confirmEl = document.getElementById("confirm");
  var confirmAction = document.getElementById("confirm-action");
  var goBtn = document.getElementById("confirm-go");
  var cancelBtn = document.getElementById("confirm-cancel");
  var look = document.getElementById("look");
  var card = root.dataset.card;
  var form = root.dataset.form;

  // The page's own words, for what happens in the browser. The seat's
  // answers carry their own fixed words, in "say".
  var SAY = {
    ready: "Approve or deny below. Nothing is sent until you confirm with your passkey.",
    asking: "Asking the desk for this decision's challenge.",
    check: "Check the card below, then confirm with your passkey.",
    waiting: "Waiting for your phone to ask for your face, finger or passcode.",
    sending: "Sending the decision.",
    cancelled: "Cancelled. Nothing was sent.",
    expired: "This took too long. Tap again.",
    unreachable: "The seat cannot be reached. Nothing was sent.",
    unknown: "Decision sent. Outcome unknown.",
    unsupported: "This browser cannot use a passkey. Decide at the desk.",
    site_refused: "The browser refused this site's name for a passkey. Nothing was sent.",
    reason_long: "The reason is longer than 500 bytes. Shorten it, then tap Deny again.",
    failed: "Something went wrong. Look at the card again."
  };
  var ACTIONS = {
    approve: ["Approve this card?", "Approve with your passkey"],
    deny: ["Deny this card?", "Deny with your passkey"]
  };
  // The answers to a decision after which the owner may tap again: nothing
  // was decided and the card is as it was.
  var AGAIN = { challenge_expired: true, challenge_stale: true, assertion_refused: true, desk_busy: true, daemon_unavailable: true, rate_limited: true, decisions_inhibited: true };

  var busy = false;
  // pending is the one challenge the next confirm tap may use; a tap takes
  // it, so one challenge is signed at most once.
  var pending = null;

  function say(text) { statusEl.textContent = text; }

  function allSigned() {
    return boxes.every(function (b) { return b.checked; });
  }

  // stale says the live refresh script found this page's card changed or
  // gone: the controls stay disabled, whatever else happens here.
  function stale() { return document.documentElement.dataset.stale === "yes"; }

  function refresh() {
    if (approveBtn) { approveBtn.disabled = stale() || busy || !allSigned(); }
    if (denyBtn) { denyBtn.disabled = stale() || busy; }
  }

  function showForm(on) {
    [approveBtn, denyBtn, reasonEl, reasonLabel, signoffs].forEach(function (el) { if (el) { el.hidden = !on; } });
  }

  // back leaves the card as it was: the form again, nothing pending.
  function back(text) {
    pending = null;
    busy = false;
    confirmEl.hidden = true;
    goBtn.disabled = false;
    cancelBtn.disabled = false;
    showForm(true);
    refresh();
    say(text);
  }

  // onlyLook ends the page's part: no form and no confirm, only "Look
  // again", which reads the card afresh.
  function onlyLook(text) {
    pending = null;
    busy = true;
    showForm(false);
    confirmEl.hidden = true;
    say(text);
    look.hidden = false;
  }

  function b64e(buf) {
    var a = new Uint8Array(buf), s = "";
    for (var i = 0; i < a.length; i++) { s += String.fromCharCode(a[i]); }
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function b64d(str) {
    var s = String(str).replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) { s += "="; }
    var raw = atob(s), a = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) { a[i] = raw.charCodeAt(i); }
    return a;
  }

  // The one fetch helper, as on the login page. A failure is thrown as
  // {code, say}: the seat's code and words, or "network" when no answer
  // came.
  async function post(path, body) {
    var resp;
    try {
      resp = await fetch(path, {
        method: "POST", credentials: "same-origin", cache: "no-store", referrer: "", referrerPolicy: "strict-origin",
        headers: { "Content-Type": "application/json" }, body: JSON.stringify(body)
      });
    } catch (e) { throw { code: "network" }; }
    var data = null;
    try { data = await resp.json(); } catch (e) { /* a body that is not JSON is a failure below */ }
    if (!resp.ok || !data) {
      throw {
        code: data && typeof data.error === "string" ? data.error : "failed",
        say: data && typeof data.say === "string" ? data.say : ""
      };
    }
    return data;
  }

  function supported() {
    return !!(window.PublicKeyCredential && navigator.credentials && navigator.credentials.get);
  }

  // ask asks the seat for the challenge of one decision, and shows what it
  // is for before the passkey is asked.
  async function ask(action) {
    var body = { card: card, form: form, action: action, signed_off: [], reason: "" };
    if (action === "approve") {
      body.signed_off = boxes.filter(function (b) { return b.checked; }).map(function (b) { return Number(b.value); });
    } else if (reasonEl) {
      body.reason = reasonEl.value;
      if (new TextEncoder().encode(body.reason).length > 500) { throw { code: "reason_long", say: SAY.reason_long }; }
    }
    var ch = await post("/api/decide-challenge", body);
    if (typeof ch.challenge_id !== "string" || typeof ch.challenge !== "string" || ch.action !== action ||
        !Array.isArray(ch.allow) || ch.allow.length !== 1 || typeof ch.expires_in !== "number") {
      throw { code: "failed" };
    }
    pending = {
      id: ch.challenge_id, challenge: b64d(ch.challenge), allow: b64d(ch.allow[0]), action: action,
      deadline: Date.now() + ch.expires_in * 1000
    };
    showForm(false);
    confirmAction.textContent = ACTIONS[action][0];
    goBtn.textContent = ACTIONS[action][1];
    confirmEl.hidden = false;
    say(SAY.check);
    goBtn.focus();
  }

  function start(action) {
    if (busy || stale()) { return; }
    busy = true;
    refresh();
    say(SAY.asking);
    ask(action).then(function () { busy = false; }, function (e) {
      e = e || {};
      if (e.code === "network") { back(SAY.unreachable); return; }
      if (e.code === "key_revoked" || e.code === "session_ended" || e.code === "sent_unknown" || e.code === "look_again" || e.code === "card_changed") {
        onlyLook(e.say || SAY.failed);
        return;
      }
      back(e.say || SAY.failed);
    });
  }

  // confirm asks the phone for one assertion over the pending challenge,
  // with user verification required and the session's one credential
  // allowed, and sends it once.
  async function confirm(p) {
    if (Date.now() >= p.deadline) { back(SAY.expired); return; }
    say(SAY.waiting);
    var cred;
    try {
      cred = await navigator.credentials.get({ publicKey: {
        challenge: p.challenge,
        rpId: root.dataset.rpId,
        allowCredentials: [{ type: "public-key", id: p.allow }],
        userVerification: "required",
        timeout: Math.max(1000, Math.min(120000, p.deadline - Date.now()))
      } });
    } catch (e) {
      if (e && e.name === "NotSupportedError") { back(SAY.unsupported); return; }
      if (e && e.name === "SecurityError") { back(SAY.site_refused); return; }
      back(SAY.cancelled); // NotAllowedError: cancelled, timed out, or no consent
      return;
    }
    if (!cred || !cred.response) { back(SAY.cancelled); return; }
    var r = cred.response;
    say(SAY.sending);
    var out;
    try {
      out = await post("/api/decide", {
        challenge_id: p.id,
        credential_id: b64e(cred.rawId),
        client_data_json: b64e(r.clientDataJSON),
        authenticator_data: b64e(r.authenticatorData),
        signature: b64e(r.signature)
      });
    } catch (e) {
      e = e || {};
      // No answer, or one that leaves the outcome open: the decision may
      // have been taken. It is never sent again; the card is read again.
      if (e.code === "network" || !AGAIN[e.code]) { onlyLook(e.say || SAY.unknown); return; }
      back(e.say || SAY.failed);
      return;
    }
    var line = typeof out.line === "string" ? out.line : "";
    var words = typeof out.say === "string" ? out.say : "";
    onlyLook(out.held ? line + "\n" + words : words + "\n" + line);
  }

  boxes.forEach(function (b) { b.addEventListener("change", refresh); });
  if (approveBtn) { approveBtn.addEventListener("click", function () { start("approve"); }); }
  if (denyBtn) { denyBtn.addEventListener("click", function () { start("deny"); }); }
  cancelBtn.addEventListener("click", function () {
    if (!pending || busy) { return; }
    back(SAY.cancelled);
  });
  goBtn.addEventListener("click", function () {
    if (busy || stale() || !pending) { return; }
    var p = pending;
    pending = null;
    busy = true;
    goBtn.disabled = true;
    cancelBtn.disabled = true;
    confirm(p).catch(function () { onlyLook(SAY.unknown); });
  });

  if (!supported()) {
    say(SAY.unsupported);
    return;
  }
  showForm(true);
  refresh();
  say(SAY.ready);
})();
