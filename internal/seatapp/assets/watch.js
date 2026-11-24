// Live refresh of the cards pages. The page names the version of the seat's
// view it was made from; this script asks the seat whether that moved. It
// reads and shows; it sends nothing but its one request at a time, which
// carries no card text.
//
// The card list reads a stream of hints from the seat (server-sent events
// read with fetch, a POST like every other request of the app): one kind of
// event, "cards-changed", whose data is the new version and nothing else.
// The seat ends a stream within 25 seconds and the list opens another. On
// any failure of the stream (a refusal, an error, a stream of the wrong
// form, a silence, a browser that cannot read one) the page falls back to
// the long poll, which every other page uses, until the page is shown again.
// A hidden page holds no stream: it closes it when hidden or left, and opens
// one again, after a short random wait, when it is shown again or the
// network is back.
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
  var STREAM_PATH = "/api/watch/stream";
  var STREAM_KIND = "cards-changed";  // the one kind of event acted on; any other is skipped

  var REQUEST_TIMEOUT = 30000; // the seat holds a poll for 25 seconds at most
  var MIN_GAP = 2000;          // the least time between the starts of two polls
  var OFFLINE_WAIT = 30000;
  var REPLACED_WAIT = 25000;   // after a poll was replaced by another tab's: a hold's length
  var LIMITED_WAIT = 60000;
  var BACKOFF_FIRST = 5000;
  var BACKOFF_LAST = 60000;
  var RELOAD_GAP = 20000;      // the page is reloaded only once its own read is this old
  var RELOAD_RETRY = 2000;
  var STREAM_LINE_MAX = 512;   // characters of one line of the stream; a longer one fails it
  var STREAM_EVENT_MAX = 256;  // characters of one event's data; more fails it
  var STREAM_SILENCE = 30000;  // the seat writes every 10 seconds: no byte for this long fails it
  var STREAM_SHORT = 5000;     // a stream the seat ends sooner than this, twice in a row, fails it
  var REOPEN_FIRST = 1000;     // the wait before a stream is opened again, doubling after each failure,
  var REOPEN_LAST = 30000;     // to this,
  var REOPEN_JITTER = 2000;    // with up to this much more, at random

  var live = root.dataset.live === "yes";
  var card = root.dataset.card || "";
  var binding = root.dataset.binding || "";
  var version = root.dataset.version || "";
  var onCard = card !== "";
  // A Card page of a card that is not pending names no card; it is no list,
  // so it never reloads.
  var still = root.dataset.still === "yes";
  // Only the card list streams; the Card page keeps the poll, which says
  // what became of its card.
  var streamable = live && !onCard && !still && canStream();
  var useStream = streamable; // false after a failure of the stream, until the page is shown again

  var loadedAt = Date.now();
  var stopped = false;     // nothing more is asked: the session ended, or the card is gone for good
  var inflight = false;
  var timer = null;        // the next poll
  var reloadTimer = null;  // a reload waiting its turn
  var backoff = 0;
  var bannerShown = false;
  var pollCtl = null;      // the poll in flight
  var conn = null;         // the open stream: its controller, and whether this page closed it
  var streamFails = 0;     // failures of the stream in a row
  var shortEnds = 0;       // streams in a row that the seat ended soon after they opened

  // canStream reports whether the browser can read a stream with fetch.
  function canStream() {
    return typeof window.fetch === "function" && typeof window.AbortController === "function" &&
      typeof window.TextDecoder === "function" && typeof window.ReadableStream === "function";
  }

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

  // next asks by the page's transport: the stream, or a poll.
  function next() {
    if (useStream) { stream(); } else { poll(); }
  }

  function schedule(ms) {
    if (timer !== null) { clearTimeout(timer); }
    timer = setTimeout(function () { timer = null; next(); }, ms);
  }

  function cancel() {
    if (timer !== null) { clearTimeout(timer); timer = null; }
  }

  function validVersion(v) { return typeof v === "string" && v.length <= 64 && /^[0-9a-f]*$/.test(v); }

  // send makes the one kind of request of this script, the poll's and the
  // stream's: a same-origin JSON POST, with no redirect and no cache.
  function send(path, body, ctl) {
    return fetch(path, {
      method: "POST", credentials: "same-origin", cache: "no-store", redirect: "error", referrer: "", referrerPolicy: "strict-origin",
      headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal: ctl.signal
    });
  }

  // ask sends one poll and returns the seat's answer. A failure is thrown as
  // {limited: true} for a 429, and as {limited: false} for any other.
  async function ask() {
    var ctl = new AbortController();
    pollCtl = ctl;
    var cut = setTimeout(function () { ctl.abort(); }, REQUEST_TIMEOUT);
    try {
      var body = { version: version };
      if (onCard) { body.card = card; body.binding = binding; }
      var resp = await send(POLL_PATH, body, ctl);
      if (resp.status === 429) { throw { limited: true }; }
      if (resp.status !== 200 || resp.redirected) { throw { limited: false }; }
      var data = await resp.json();
      if (!data || typeof data !== "object" || typeof data.state !== "string") { throw { limited: false }; }
      return data;
    } finally {
      clearTimeout(cut);
      if (pollCtl === ctl) { pollCtl = null; }
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
    // The page went back to the stream meanwhile: this poll's answer is
    // not needed.
    if (stopped || useStream) { return; }
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

  // hinted acts on the data of a cards-changed event: the version the seat
  // has now. It is the poll's "changed" on the list: the list reloads by the
  // same rule, and nothing of the hint is put on the page.
  function hinted(text) {
    var hint = null;
    try { hint = JSON.parse(text); } catch (e) { return; }
    if (!hint || typeof hint !== "object" || !validVersion(hint.version) || hint.version === version) { return; }
    version = hint.version;
    quiet();
    if (!still) { wantReload(); }
  }

  // readStream opens a stream and reads it to its end, acting on each
  // event. It returns when the seat ends the stream, and throws on any
  // failure: a refusal, a response that is not a stream, a line or an
  // event over its bound, or STREAM_SILENCE without a byte.
  async function readStream(c) {
    var silence = null;
    function hush() {
      if (silence !== null) { clearTimeout(silence); }
      silence = setTimeout(function () { c.ctl.abort(); }, STREAM_SILENCE);
    }
    hush();
    try {
      var resp = await send(STREAM_PATH, { version: version }, c.ctl);
      var type = (resp.headers.get("Content-Type") || "").split(";")[0].trim().toLowerCase();
      if (resp.status !== 200 || resp.redirected || type !== "text/event-stream" ||
          !resp.body || typeof resp.body.getReader !== "function") {
        throw { stream: false };
      }
      streamFails = 0;
      quiet();
      var reader = resp.body.getReader();
      var decoder = new TextDecoder();
      var rest = "", kind = "", evData = "";
      for (;;) {
        var chunk = await reader.read();
        if (chunk.done) { return; }
        hush();
        rest += decoder.decode(chunk.value, { stream: true });
        var nl;
        while ((nl = rest.indexOf("\n")) >= 0) {
          var line = rest.slice(0, nl);
          rest = rest.slice(nl + 1);
          if (line.length > STREAM_LINE_MAX) { throw { stream: false }; }
          if (line.charAt(line.length - 1) === "\r") { line = line.slice(0, -1); }
          if (line === "") {
            // The end of an event: act on it if it is of the one kind.
            if (kind === STREAM_KIND) { hinted(evData); }
            kind = "";
            evData = "";
            continue;
          }
          if (line.charAt(0) === ":") { continue; } // a heartbeat
          var colon = line.indexOf(":");
          var field = colon < 0 ? line : line.slice(0, colon);
          var value = colon < 0 ? "" : line.slice(colon + 1);
          if (value.charAt(0) === " ") { value = value.slice(1); }
          if (field === "event") {
            kind = value;
          } else if (field === "data") {
            evData = evData === "" ? value : evData + "\n" + value;
            if (evData.length > STREAM_EVENT_MAX) { throw { stream: false }; }
          }
          // Any other field is skipped.
        }
        if (rest.length > STREAM_LINE_MAX) { throw { stream: false }; }
      }
    } finally {
      clearTimeout(silence);
    }
  }

  // stream holds the page's one stream. When the seat ends it, another is
  // opened after a short random wait; when it fails, the page falls back to
  // the poll.
  async function stream() {
    if (stopped || !useStream || conn !== null || document.hidden) { return; }
    var c = { ctl: new AbortController(), closed: false };
    conn = c;
    var began = Date.now();
    var failed = false;
    try { await readStream(c); } catch (e) { failed = true; }
    c.ctl.abort();
    if (conn === c) { conn = null; }
    // This page closed it (hidden, left, or back to the poll): nothing more.
    if (c.closed || stopped) { return; }
    if (!failed && Date.now() - began < STREAM_SHORT) {
      shortEnds += 1;
      failed = shortEnds >= 2;
    } else if (!failed) {
      shortEnds = 0;
    }
    if (failed) {
      fallBack();
      return;
    }
    schedule(reopenWait());
  }

  // closeStream closes the open stream, if there is one.
  function closeStream() {
    if (conn !== null) {
      conn.closed = true;
      conn.ctl.abort();
      conn = null;
    }
  }

  // fallBack turns the page to the poll until it is shown again, and polls
  // at once, as a page that cannot stream does from its start.
  function fallBack() {
    useStream = false;
    streamFails += 1;
    shortEnds = 0;
    poll();
  }

  // reopenWait is the wait before a stream is opened again: doubling with
  // the failures in a row, bounded, and spread at random so that the tabs
  // and phones that lost their streams together do not come back together.
  function reopenWait() {
    var base = Math.min(REOPEN_FIRST * Math.pow(2, Math.min(streamFails, 5)), REOPEN_LAST);
    return base + Math.floor(Math.random() * REOPEN_JITTER);
  }

  // asleep: the page is hidden or left. The stream is closed; a poll in
  // flight runs out, as before, and none is asked after it.
  function asleep() {
    cancel();
    closeStream();
  }

  // awake: the page is shown again, or the network is back. A page that
  // streams (or fell back from the stream) opens one stream after a short
  // random wait, with the version it shows, so a change it missed is said
  // at once; any other page polls at once.
  function awake() {
    if (stopped || document.hidden) { return; }
    cancel();
    if (streamable) {
      useStream = true;
      if (pollCtl !== null) { pollCtl.abort(); }
      if (conn === null) { schedule(reopenWait()); }
      return;
    }
    poll();
  }

  document.addEventListener("visibilitychange", function () {
    if (stopped) { return; }
    if (document.hidden) { asleep(); } else { awake(); }
  });
  window.addEventListener("pagehide", function () { asleep(); });
  window.addEventListener("pageshow", function (ev) { if (ev.persisted) { awake(); } });
  window.addEventListener("online", function () { awake(); });

  if (!live) {
    say(TEXT.notLive);
    return;
  }
  next();
})();
