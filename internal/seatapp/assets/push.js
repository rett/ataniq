// The controls of the notices on the Cards list: Enable notices and Stop
// notices, and a status line. A notice is a small push to the Home Screen app
// that says how many cards wait; it carries nothing of a card, and it never
// acts on one. This script only turns the push on and off for this
// phone: it asks the seat for the push key, subscribes the browser, and hands
// the seat the subscription to keep at the notifier; to stop, it names the
// subscription by the SHA-256 of its address and drops it in the browser.
//
// The phone's permission is asked in one place only, inside the tap on
// Enable, since the browser accepts it from nothing else. Every text put on
// the page is one of the constants below, set with textContent, never as
// markup and never taken from an answer of the seat or of the browser. The
// seat's answers are read only for their status and for the one code that
// says there is no room for another phone.
"use strict";
(function () {
  var enableBtn = document.getElementById("enable-notices");
  var stopBtn = document.getElementById("stop-notices");
  var statusEl = document.getElementById("notices-status");
  if (!enableBtn || !stopBtn || !statusEl) { return; }

  var TEXT = {
    unsupported: "This browser cannot show notices.",
    notHome: "Open this page from the Home Screen app to turn on notices.",
    working: "Working.",
    on: "Notices are on.",
    off: "Notices are off.",
    noPermission: "Notices need your permission. Change it in the phone's settings.",
    failed: "Could not turn on notices. Try again later.",
    stopFailed: "Could not stop notices. Try again later.",
    full: "No room for another phone. Stop notices on another phone first.",
    limited: "Too many tries. Wait a minute.",
    ended: "Session ended. Log in again."
  };
  var SW_PATH = "/sw.js";
  var INFO_PATH = "/api/push/info";
  var SUBSCRIBE_PATH = "/api/push/subscribe";
  var UNSUBSCRIBE_PATH = "/api/push/unsubscribe";
  var REQUEST_TIMEOUT = 15000;
  var KEY_LENGTH = 65;

  function say(text) { statusEl.textContent = text; }

  // Failures are thrown as {kind: ...}; show maps them to a constant text.
  function fail(kind) { return { kind: kind }; }

  function show(e, fallback) {
    var kind = e && typeof e.kind === "string" ? e.kind : "";
    if (kind === "permission") { say(TEXT.noPermission); }
    else if (kind === "full") { say(TEXT.full); }
    else if (kind === "limited") { say(TEXT.limited); }
    else if (kind === "ended") { say(TEXT.ended); }
    else { say(fallback); }
  }

  var supported = "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
  var standalone = navigator.standalone === true ||
    (typeof matchMedia === "function" && matchMedia("(display-mode: standalone)").matches);

  if (!supported) { say(TEXT.unsupported); return; }
  if (!standalone) { say(TEXT.notHome); return; }

  function toBase64Url(buffer) {
    var bytes = new Uint8Array(buffer);
    var text = "";
    for (var i = 0; i < bytes.length; i++) { text += String.fromCharCode(bytes[i]); }
    return btoa(text).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  // fromBase64Url decodes the push key; a key that is not 65 bytes is no key.
  function fromBase64Url(text) {
    if (typeof text !== "string" || !/^[A-Za-z0-9_-]+$/.test(text)) { throw fail("key"); }
    var padded = text.replace(/-/g, "+").replace(/_/g, "/");
    while (padded.length % 4 !== 0) { padded += "="; }
    var raw = atob(padded);
    if (raw.length !== KEY_LENGTH) { throw fail("key"); }
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) { out[i] = raw.charCodeAt(i); }
    return out;
  }

  async function sha256Hex(text) {
    var digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
    var bytes = new Uint8Array(digest);
    var hex = "";
    for (var i = 0; i < bytes.length; i++) { hex += (bytes[i] < 16 ? "0" : "") + bytes[i].toString(16); }
    return hex;
  }

  // call posts one JSON body to the seat and returns the parsed answer of a
  // 200. Any other status is thrown as a failure of its kind.
  async function call(path, body) {
    var ctl = new AbortController();
    var cut = setTimeout(function () { ctl.abort(); }, REQUEST_TIMEOUT);
    try {
      var resp = await fetch(path, {
        method: "POST", credentials: "same-origin", cache: "no-store", redirect: "error", referrer: "", referrerPolicy: "strict-origin",
        headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal: ctl.signal
      });
      if (resp.status === 401) { throw fail("ended"); }
      if (resp.status === 429) { throw fail("limited"); }
      if (resp.status !== 200) {
        var code = "";
        try {
          var err = await resp.json();
          if (err && typeof err.code === "string") { code = err.code; }
        } catch (e) { code = ""; }
        throw fail(code === "too_many" ? "full" : "seat");
      }
      if (resp.redirected) { throw fail("seat"); }
      var data = await resp.json();
      if (!data || typeof data !== "object" || data.ok !== true) { throw fail("seat"); }
      return data;
    } catch (e) {
      if (e && typeof e.kind === "string") { throw e; }
      throw fail("seat");
    } finally {
      clearTimeout(cut);
    }
  }

  // swScriptURL is the address of the service worker as the register call
  // takes it. That call is a script sink under the page's Trusted Types
  // rule, so the address goes through a policy that gives this one address
  // and no other.
  var swPolicy = null;
  function swScriptURL() {
    var tt = window.trustedTypes;
    if (!tt || typeof tt.createPolicy !== "function") { return SW_PATH; }
    if (swPolicy === null) {
      swPolicy = tt.createPolicy("seat-sw", {
        createScriptURL: function (url) {
          if (url !== SW_PATH) { throw new TypeError("not the service worker"); }
          return url;
        }
      });
    }
    return swPolicy.createScriptURL(SW_PATH);
  }

  async function currentSubscription() {
    var reg = await navigator.serviceWorker.getRegistration("/");
    if (!reg) { return null; }
    return reg.pushManager.getSubscription();
  }

  function showState(subscribed) {
    enableBtn.hidden = subscribed;
    stopBtn.hidden = !subscribed;
    enableBtn.disabled = false;
    stopBtn.disabled = false;
    say(subscribed ? TEXT.on : TEXT.off);
  }

  async function refresh() {
    var sub = null;
    try { sub = await currentSubscription(); } catch (e) { sub = null; }
    showState(sub !== null);
  }

  enableBtn.addEventListener("click", async function () {
    enableBtn.disabled = true;
    say(TEXT.working);
    var sub = null;
    try {
      var permission = await Notification.requestPermission();
      if (permission !== "granted") { throw fail("permission"); }
      var reg = await navigator.serviceWorker.register(swScriptURL(), { scope: "/" });
      await navigator.serviceWorker.ready;
      var info = await call(INFO_PATH, {});
      var key = fromBase64Url(info.vapid_public);
      sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
      var p256dh = sub.getKey("p256dh");
      var auth = sub.getKey("auth");
      if (!p256dh || !auth) { throw fail("seat"); }
      await call(SUBSCRIBE_PATH, { endpoint: sub.endpoint, p256dh: toBase64Url(p256dh), auth: toBase64Url(auth) });
      showState(true);
    } catch (e) {
      // A subscription the seat did not take is not kept in the browser.
      if (sub) { try { await sub.unsubscribe(); } catch (e2) { /* nothing more to do */ } }
      show(e, TEXT.failed);
      enableBtn.disabled = false;
    }
  });

  stopBtn.addEventListener("click", async function () {
    stopBtn.disabled = true;
    say(TEXT.working);
    try {
      var sub = await currentSubscription();
      if (sub) {
        // The notifier forgets it first: if that fails the browser keeps its
        // subscription and the tap can be made again.
        await call(UNSUBSCRIBE_PATH, { endpoint_sha256: await sha256Hex(sub.endpoint) });
        await sub.unsubscribe();
      }
      showState(false);
    } catch (e) {
      show(e, TEXT.stopFailed);
      stopBtn.disabled = false;
    }
  });

  refresh();
})();
