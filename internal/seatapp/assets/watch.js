// Live refresh of the cards pages. The page names the version of the seat's
// view it was made from; this script asks the seat, with one long poll at a
// time, whether that moved. It reads and shows; it sends nothing but that
// poll, and the poll carries no card text.
//
// The card list reloads when its version moved (at most once every twenty
// seconds, never over a form field with typed text). A Card page never
// reloads by itself, since the owner may be ticking sign-offs: it shows a
// banner, and turns off the controls the page marks with data-offer. The
// page's own script honours the stale mark this script puts on the document,
// and this script honours the sent mark the page's script puts there when it
// sends its form, so that a card this very page moved is not said to have
// been moved elsewhere.
//
// Every text put on the page is one of the constants below, set with
// textContent, never as markup, and never taken from the seat's answer.
"use strict";
(function () {
  var root = document.getElementById("watch");
  var statusEl = document.getElementById("watch-status");
  if (!root || !statusEl) { return; }

  var TEXT = {
    notLive: "Not live",
    offline: "Not live: the desk is out of reach",
    ended: "Session ended. Log in again.",
    changed: "This card changed or was decided elsewhere. Look again.",
    login: "Log in",
    cards: "Back to the cards",
    reload: "Reload"
  };
  var LOGIN_PATH = "/login/";
  var CARDS_PATH = "/cards/";
  var POLL_PATH = "/api/watch";

  var REQUEST_TIMEOUT = 30000; // the seat holds a poll for 25 seconds at most
  var MIN_GAP = 2000;          // the least time between the starts of two polls
  var OFFLINE_WAIT = 30000;
  var REPLACED_WAIT = 25000;   // after a poll was replaced by another tab's: a hold's length
  var LIMITED_WAIT = 60000;
  var BACKOFF_FIRST = 5000;
  var BACKOFF_LAST = 60000;
  var RELOAD_GAP = 20000;      // the page is reloaded only once its own read is this old
  var RELOAD_RETRY = 2000;

  var live = root.dataset.live === "yes";
  var card = root.dataset.card || "";
  var binding = root.dataset.binding || "";
  var version = root.dataset.version || "";
  var onCard = card !== "";
  // A Card page of a card that is not pending names no card; it is no list,
  // so it never reloads.
  var still = root.dataset.still === "yes";

  var loadedAt = Date.now();
  var stopped = false;     // nothing more is asked: the session ended, or the card is gone for good
  var inflight = false;
  var timer = null;        // the next poll
  var reloadTimer = null;  // a reload waiting its turn
  var backoff = 0;
  var bannerShown = false;

  // say sets the one status line. Extra nodes (a link, a button) go after
  // the text, inside the same element.
  function say(text, extras) {
    statusEl.textContent = text;
    (extras || []).forEach(function (n) {
      statusEl.appendChild(document.createTextNode(" "));
      statusEl.appendChild(n);
    });
  }

  function link(text, path) {
    var a = document.createElement("a");
    a.setAttribute("href", path);
    a.textContent = text;
    return a;
  }

  function reloadButton() {
    var b = document.createElement("button");
    b.type = "button";
    b.className = "quiet";
    b.textContent = TEXT.reload;
    b.addEventListener("click", function () { location.reload(); });
    return b;
  }

  // showBanner tells the owner the card is not as the page shows it.
  function showBanner() {
    bannerShown = true;
    say(TEXT.changed, [link(TEXT.cards, CARDS_PATH), reloadButton()]);
  }

  // withdraw turns off every control the page marks as an offer, and marks
  // the document, so that the page's own script keeps them off.
  function withdraw() {
    document.documentElement.dataset.stale = "yes";
    Array.prototype.forEach.call(document.querySelectorAll("[data-offer]"), function (el) { el.disabled = true; });
  }

  // quiet clears the status line after a good answer, but a banner stays.
  function quiet() {
    if (bannerShown) { showBanner(); } else { say(""); }
  }

  function typing() {
    var el = document.activeElement;
    if (!el || !el.tagName) { return false; }
    var tag = el.tagName.toLowerCase();
    return (tag === "input" || tag === "textarea") && typeof el.value === "string" && el.value !== "" &&
      el.type !== "checkbox" && el.type !== "radio";
  }

  // wantReload reloads the list once twenty seconds have passed since this page
  // was loaded, and not while a field holds typed text. A reload pending is
  // never set twice.
  function wantReload() {
    if (reloadTimer !== null) { return; }
    var wait = Math.max(0, loadedAt + RELOAD_GAP - Date.now());
    reloadTimer = setTimeout(function tryReload() {
      if (typing()) { reloadTimer = setTimeout(tryReload, RELOAD_RETRY); return; }
      location.reload();
    }, wait);
  }

  function schedule(ms) {
    if (timer !== null) { clearTimeout(timer); }
    timer = setTimeout(function () { timer = null; poll(); }, ms);
  }

  function cancel() {
    if (timer !== null) { clearTimeout(timer); timer = null; }
  }

  function validVersion(v) { return typeof v === "string" && v.length <= 64 && /^[0-9a-f]*$/.test(v); }

  // ask sends one poll and returns the seat's answer. A failure is thrown as
  // {limited: true} for a 429, and as {limited: false} for any other.
  async function ask() {
    var ctl = new AbortController();
    var cut = setTimeout(function () { ctl.abort(); }, REQUEST_TIMEOUT);
    try {
      var body = { version: version };
      if (onCard) { body.card = card; body.binding = binding; }
      var resp = await fetch(POLL_PATH, {
        method: "POST", credentials: "same-origin", cache: "no-store", redirect: "error", referrer: "", referrerPolicy: "strict-origin",
        headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal: ctl.signal
      });
      if (resp.status === 429) { throw { limited: true }; }
      if (resp.status !== 200 || resp.redirected) { throw { limited: false }; }
      var data = await resp.json();
      if (!data || typeof data !== "object" || typeof data.state !== "string") { throw { limited: false }; }
      return data;
    } finally {
      clearTimeout(cut);
    }
  }

  // poll asks once, then the next poll is scheduled from the answer. Nothing
  // is asked while the page is hidden: the page asks again, at once, when it
  // is shown.
  async function poll() {
    if (stopped || inflight || document.hidden) { return; }
    inflight = true;
    var began = Date.now();
    var data = null, failure = null;
    try { data = await ask(); } catch (e) { failure = e || {}; }
    inflight = false;
    if (stopped) { return; }
    if (failure) {
      if (failure.limited === true) {
        backoff = 0;
        schedule(LIMITED_WAIT);
      } else {
        backoff = backoff === 0 ? BACKOFF_FIRST : Math.min(backoff * 2, BACKOFF_LAST);
        schedule(backoff);
      }
      return;
    }
    backoff = 0;
    handle(data, began);
  }

  // handle acts on an answer: its state is one of same, changed, replaced,
  // offline and ended, and, for a Card page, its card is one of live, changed, gone and
  // unknown.
  function handle(data, began) {
    var next = Math.max(0, MIN_GAP - (Date.now() - began));
    switch (data.state) {
    case "ended":
      stopped = true;
      cancel();
      say(TEXT.ended, [link(TEXT.login, LOGIN_PATH)]);
      return;
    case "offline":
      say(TEXT.offline);
      schedule(OFFLINE_WAIT);
      return;
    case "replaced":
      // Another tab of this session took this poll's place. Asking again at
      // once would take that tab's place in turn, and the tabs would end
      // each other's polls without end: wait out a hold instead.
      quiet();
      schedule(REPLACED_WAIT);
      return;
    case "changed":
      if (validVersion(data.version)) { version = data.version; }
      break;
    case "same":
      break;
    default:
      // Not a state this script knows: look again later, say nothing.
      schedule(OFFLINE_WAIT);
      return;
    }
    if (onCard) {
      var cs = typeof data.card === "string" ? data.card : "unknown";
      if (cs === "changed" || cs === "gone") {
        // The card is not as this page shows it, for good: no more asking.
        stopped = true;
        cancel();
        withdraw();
        // The page sent its form, which moved the card: the page says what
        // became of it, and "elsewhere" would be wrong.
        if (document.documentElement.dataset.sent !== "yes") { showBanner(); }
        return;
      }
      switch (cs) {
      case "live":
        // The version taken above keeps the next poll quiet.
        quiet();
        break;
      case "unknown":
        // The list cannot say: the banner shows only when the state moved
        // with the version, and the controls stay.
        if (data.state === "changed") { showBanner(); } else { quiet(); }
        break;
      default:
        quiet();
      }
    } else {
      quiet();
      if (data.state === "changed" && !still) { wantReload(); }
    }
    schedule(next);
  }

  document.addEventListener("visibilitychange", function () {
    if (stopped) { return; }
    cancel();
    if (!document.hidden) { poll(); }
  });

  if (!live) {
    say(TEXT.notLive);
    return;
  }
  poll();
})();
