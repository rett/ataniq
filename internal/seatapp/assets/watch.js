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
// form, a hint it cannot read, an opening heartbeat that does not come
// promptly, a silence, a stream cut inside an event, a browser that cannot
// read one) the page falls back to the long poll, which every other page
// uses, until the page is shown again. A hidden page holds no stream: it
// closes it when hidden or left, and opens one again, after a short random
// wait, when it is shown again or the network is back.
//
// The card list reloads when its version moved (at most once every twenty
// seconds, never over a form field with typed text, and only while it is
// shown: a change heard of is kept pending while the page is hidden, and
// the page reloads once for it when it is shown again). A Card page never
// reloads by itself, since the owner may be ticking sign-offs: it shows a
// banner, and turns off the controls the page marks with data-offer. The
// page's own script honours the stale mark this script puts on the document,
// and this script honours the sent mark the page's script puts there when it
// sends its form, so that a card this very page moved is not said to have
// been moved elsewhere.
//
// The Leads pages (the directory and a conversation, which their template
// marks with data-view="leads") read the same stream, opened with the chat
// cursor the page was rendered with: one kind of event, "chat-changed",
// whose data is the new cursor, an integer, and nothing else. They never
// poll: on any failure of the stream they keep to their Refresh link until
// the page is shown again. On a hint the directory offers "Leads changed.
// Refresh"; a conversation that shows the newest entries (the seat marks it
// with data-follow) and whose newest entry is in view reads again once,
// after its follow cursor, so that the new replies appear (by the card
// list's rule, but at most once every forty seconds: never over typed text
// or an open dialog, never while a control holds the focus, only while
// shown); any other conversation offers "New replies. Show newest", a link
// that loads the conversation's page that holds its newest entry. No hint
// moves the focus or reloads a hidden page. A failure of the stream drops
// a following read waiting its turn, and a browser that cannot read the
// stream says "Not live" from the start.
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
    reload: "Reload",
    leadsChanged: "Leads changed.",
    refresh: "Refresh",
    newReplies: "New replies.",
    showNewest: "Show newest"
  };
  var LOGIN_PATH = "/login/";
  var CARDS_PATH = "/cards/";
  var LEADS_PATH = "/leads/";
  var NEWEST = "#newest";       // the conversation's newest entry, on its page
  var CHAT_PAGE = /^\/leads\/daemon\/[a-z0-9][a-z0-9-]{0,31}$/; // a conversation's own path
  var POLL_PATH = "/api/watch";
  var STREAM_PATH = "/api/watch/stream";
  var STREAM_KIND = "cards-changed";  // the card list's one kind of event; any other is skipped
  var STREAM_KIND_CHAT = "chat-changed"; // the Leads pages' one kind of event; any other is skipped
  var MAX_CURSOR = 9007199254740991;   // 2^53-1, the largest chat cursor

  var REQUEST_TIMEOUT = 30000; // the seat holds a poll for 25 seconds at most
  var MIN_GAP = 2000;          // the least time between the starts of two polls
  var OFFLINE_WAIT = 30000;
  var REPLACED_WAIT = 25000;   // after a poll was replaced by another tab's: a hold's length
  var LIMITED_WAIT = 60000;
  var BACKOFF_FIRST = 5000;
  var BACKOFF_LAST = 60000;
  var RELOAD_GAP = 20000;      // the page is reloaded only once its own read is this old
  var RELOAD_RETRY = 2000;
  var FOLLOW_GAP = 40000;      // a conversation's following read is made only once the page is this old:
                               // its 2 calls, every 40 seconds, fit chat's 5 calls in 80 seconds with one to spare
  var STREAM_LINE_MAX = 512;   // characters of one line of the stream; a longer one fails it
  var STREAM_EVENT_MAX = 256;  // characters of one event's data; more fails it
  var STREAM_FIRST = 8000;     // the opening heartbeat, whole, must come this soon after the request
  var STREAM_SILENCE = 30000;  // the seat writes every 10 seconds: no byte for this long fails it
  var STREAM_SLICE = 256;      // bytes of the stream decoded at a time, so that the bounds hold as it is read
  var STREAM_SHORT = 5000;     // a stream the seat ends sooner than this, twice in a row, fails it
  var REOPEN_FIRST = 1000;     // the wait before a stream is opened again, doubling after each failure,
  var REOPEN_LAST = 30000;     // to this,
  var REOPEN_JITTER = 2000;    // with up to this much more, at random

  var live = root.dataset.live === "yes";
  var card = root.dataset.card || "";
  var binding = root.dataset.binding || "";
  var version = root.dataset.version || "";
  // rendered is the version this page shows, which never moves: the list
  // shows a newer one only by reloading. A stream is opened with it, so
  // that the seat counts as seen only what the page shows, never a hint it
  // has not shown yet; version is the poll's, and moves with each answer.
  var rendered = version;
  var onCard = card !== "";
  // A Card page of a card that is not pending names no card; it is no list,
  // so it never reloads.
  var still = root.dataset.still === "yes";
  // A Leads page names its view. It streams the chat's hints with the cursor
  // it was rendered with, which never moves (the page shows a newer one only
  // by being read again), and never polls.
  var onLeads = root.dataset.view === "leads";
  var renderedChat = onLeads && validCursor(root.dataset.chat || "") ? root.dataset.chat : "0";
  var conversation = onLeads && root.dataset.page === "conversation";
  // followAfter is the cursor a conversation that shows the newest entries
  // reads after to follow new replies; empty on any other page.
  var followAfter = conversation && /^[0-9]{1,16}$/.test(root.dataset.follow || "") ? root.dataset.follow : "";
  var streamKind = onLeads ? STREAM_KIND_CHAT : STREAM_KIND;
  var offerShown = false;  // a Leads page shows its offer, which stays
  // Only the card list and the Leads pages stream; the Card page keeps the
  // poll, which says what became of its card.
  var streamable = live && !onCard && !still && canStream();
  var useStream = streamable; // false after a failure of the stream, until the page is shown again

  var loadedAt = Date.now();
  var stopped = false;     // nothing more is asked: the session ended, or the card is gone for good
  var inflight = false;
  var timer = null;        // the next poll
  var reloadTimer = null;  // a reload waiting its turn
  var pendingChange = false; // the list heard of a version it does not show yet
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

  // quiet clears the status line after a good answer, but a banner, and a
  // Leads page's offer, stay.
  function quiet() {
    if (bannerShown) { showBanner(); } else if (!offerShown) { say(""); }
  }

  function typing() {
    var el = document.activeElement;
    if (!el || !el.tagName) { return false; }
    var tag = el.tagName.toLowerCase();
    return (tag === "input" || tag === "textarea") && typeof el.value === "string" && el.value !== "" &&
      el.type !== "checkbox" && el.type !== "radio";
  }

  // focused reports whether a control holds the focus: any element but the
  // body (or the document's own element, where some browsers leave it). A
  // Leads page never reads again by itself then: it offers the new replies.
  function focused() {
    var el = document.activeElement;
    return !!el && el !== document.body && el !== document.documentElement;
  }

  // holding reports whether the page must not be read again now: a field
  // holds typed text, or an open dialog waits for an answer.
  function holding() {
    return typing() || document.querySelector("dialog[open], [aria-modal='true']") !== null;
  }

  // wantReload reloads the page once twenty seconds have passed since it was
  // loaded (a conversation's following read forty), and not while a field
  // holds typed text or a dialog is open. A reload pending is never set
  // twice. The page reloads only while it is shown: the change is kept
  // pending (pendingChange) while the page is hidden or left, its timer is
  // cancelled then (cancelReload), the page is looked at again when the
  // timer runs, and awake reconciles a pending change once. A conversation
  // that no longer follows when the timer runs, or whose reader holds a
  // control, shows the offer instead.
  function wantReload() {
    pendingChange = true;
    if (reloadTimer !== null || stopped || document.hidden) { return; }
    var wait = Math.max(0, loadedAt + RELOAD_GAP - Date.now());
    if (onLeads) { wait = Math.max(0, loadedAt + FOLLOW_GAP - Date.now()); }
    reloadTimer = setTimeout(function tryReload() {
      reloadTimer = null;
      if (stopped || document.hidden) { return; }
      if (onLeads && (!following() || focused())) { offer(); return; }
      if (holding()) { reloadTimer = setTimeout(tryReload, RELOAD_RETRY); return; }
      reloadNow();
    }, wait);
  }

  // reloadNow reads the page again. The card list reloads. A conversation
  // that still follows (wantReload looked again) is read after its follow
  // cursor, so that every entry after the last it showed appears, at its
  // newest entry.
  function reloadNow() {
    if (onLeads) {
      var page = newestPage();
      // The same address with its fragment would only move the view.
      if (page !== here()) { location.replace(page + NEWEST); return; }
    }
    location.reload();
  }

  // validCursor reports whether s is a chat cursor as the seat writes it: a
  // decimal integer of 0 to 2^53-1, with no sign and no leading zero.
  function validCursor(s) {
    return typeof s === "string" && /^(0|[1-9][0-9]{0,15})$/.test(s) && Number(s) <= MAX_CURSOR;
  }

  // following reports whether a conversation follows new replies: the seat
  // marked the page as showing the newest entries, and the newest entry is
  // in view, so the reader is at it.
  function following() {
    if (followAfter === "") { return false; }
    var el = document.getElementById("newest");
    if (!el || typeof el.getBoundingClientRect !== "function") { return false; }
    var box = el.getBoundingClientRect();
    return box.bottom > 0 && box.top < window.innerHeight;
  }

  // here is this page's address, without its fragment.
  function here() { return location.pathname + location.search; }

  // newestPage is the address of the conversation's page that holds its
  // newest entry: on a page that shows the newest entries, the page after
  // its follow cursor (the following read's), and otherwise the
  // conversation's own address, which the seat answers with its newest
  // entries. The address is read only when it is a conversation's path.
  function newestPage() {
    var path = CHAT_PAGE.test(location.pathname) ? location.pathname : "";
    return followAfter !== "" ? path + "?after=" + followAfter : path;
  }

  // newestTarget is where a conversation's offer goes: newestPage, at its
  // newest entry. When that is this page's own address the link has no
  // fragment, so that following it loads the page again: a fragment alone
  // would only move the view on this page, which does not hold the new
  // replies.
  function newestTarget() {
    var page = newestPage();
    return page === here() ? page : page + NEWEST;
  }

  // offer shows a Leads page's quiet offer, once: a plain link the owner
  // may follow, in the polite status line; nothing moves and nothing is
  // read. A conversation's link loads the page that holds its newest
  // entry, a navigation the owner makes.
  function offer() {
    if (offerShown) { return; }
    offerShown = true;
    if (!conversation) {
      say(TEXT.leadsChanged, [link(TEXT.refresh, LEADS_PATH)]);
      return;
    }
    say(TEXT.newReplies, [link(TEXT.showNewest, newestTarget())]);
  }

  // chatChanged acts on a change of the chat that the page does not show:
  // a conversation that follows, while no control holds the focus, reads
  // again by wantReload's rule; any other Leads page shows its offer. While
  // the page is hidden it is kept pending, for awake.
  function chatChanged() {
    pendingChange = true;
    if (stopped || document.hidden) { return; }
    if (following() && !focused()) { wantReload(); } else { offer(); }
  }

  // cancelReload drops a reload waiting its turn; the change stays pending.
  function cancelReload() {
    if (reloadTimer !== null) { clearTimeout(reloadTimer); reloadTimer = null; }
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
  // is shown. A Leads page never polls: the chat has no poll.
  async function poll() {
    if (stopped || inflight || document.hidden || onLeads) { return; }
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
      cancelReload();
      closeStream();
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
  // same rule, and nothing of the hint is put on the page. It reports false
  // for data that is not the seat's one form, an object whose one member is
  // a version that is not empty, which fails the stream: a stream whose
  // hints cannot be read must not look live.
  function hinted(text) {
    var hint = null;
    try { hint = JSON.parse(text); } catch (e) { return false; }
    if (!hint || typeof hint !== "object" || Array.isArray(hint) || Object.keys(hint).length !== 1 ||
        !validVersion(hint.version) || hint.version === "") {
      return false;
    }
    if (hint.version === version) { return true; }
    version = hint.version;
    quiet();
    if (!still) { wantReload(); }
    return true;
  }

  // chatHinted acts on the data of a chat-changed event: the cursor the seat
  // has now, a decimal integer and nothing else. A cursor that is not the
  // one the page was rendered with is a change of the chat. It reports
  // false for data of any other form, which fails the stream.
  function chatHinted(text) {
    if (!validCursor(text) || text === "0") { return false; }
    if (text !== renderedChat) { chatChanged(); }
    return true;
  }

  // readStream opens a stream and reads it to its end, acting on each
  // event. It returns when the seat ends the stream between two events, and
  // throws on any failure: a refusal, a response that is not a stream, an
  // opening heartbeat that is not whole within STREAM_FIRST of the request
  // (a proxy that holds the response back to its end fails here, well
  // inside a rotation), a line or an event over its bound, a hint that
  // cannot be read, STREAM_SILENCE without a byte once the stream is open,
  // and an end inside an event or before the opening heartbeat. The body is
  // decoded STREAM_SLICE bytes at a time, and the bounds are checked as each
  // slice is taken, so that no more than a bound and a slice is ever held.
  async function readStream(c) {
    var silence = null;
    var opened = false; // the opening heartbeat came, whole
    function hush(ms) {
      if (silence !== null) { clearTimeout(silence); }
      silence = setTimeout(function () { c.ctl.abort(); }, ms);
    }
    hush(STREAM_FIRST);
    try {
      var resp = await send(STREAM_PATH, onLeads ? { leads: renderedChat } : { version: rendered }, c.ctl);
      var type = (resp.headers.get("Content-Type") || "").split(";")[0].trim().toLowerCase();
      if (resp.status !== 200 || resp.redirected || type !== "text/event-stream" ||
          !resp.body || typeof resp.body.getReader !== "function") {
        throw { stream: false };
      }
      var reader = resp.body.getReader();
      var decoder = new TextDecoder();
      var rest = "", kind = "", evData = "";
      for (;;) {
        var chunk = await reader.read();
        if (chunk.done) {
          rest += decoder.decode();
          // The seat ends a stream between two events: anything left is an
          // event cut short.
          if (!opened || rest !== "" || kind !== "" || evData !== "") { throw { stream: false }; }
          return;
        }
        var bytes = chunk.value;
        for (var at = 0; at < bytes.length; at += STREAM_SLICE) {
          rest += decoder.decode(bytes.subarray(at, at + STREAM_SLICE), { stream: true });
          var nl;
          while ((nl = rest.indexOf("\n")) >= 0) {
            if (nl > STREAM_LINE_MAX) { throw { stream: false }; }
            var line = rest.slice(0, nl);
            rest = rest.slice(nl + 1);
            if (line.charAt(line.length - 1) === "\r") { line = line.slice(0, -1); }
            if (line === "") {
              // The end of an event: act on it if it is of the one kind.
              if (kind === streamKind && !(onLeads ? chatHinted(evData) : hinted(evData))) { throw { stream: false }; }
              kind = "";
              evData = "";
              if (!opened) {
                opened = true;
                streamFails = 0;
                quiet();
              }
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
        // Once open, any byte shows the stream alive.
        if (opened) { hush(STREAM_SILENCE); }
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
  // at once, as a page that cannot stream does from its start. A Leads page
  // has no poll: it drops a following read waiting its turn and the change
  // pending (a page that says it is not live moves no more; a stream opened
  // when it is shown again says the current cursor), says it is not live
  // (unless it shows its offer) and keeps to its Refresh link.
  function fallBack() {
    if (onLeads) {
      cancelReload();
      pendingChange = false;
    }
    useStream = false;
    streamFails += 1;
    shortEnds = 0;
    if (onLeads && !offerShown) { say(TEXT.notLive); }
    poll();
  }

  // reopenWait is the wait before a stream is opened again: doubling with
  // the failures in a row, bounded, and spread at random so that the tabs
  // and phones that lost their streams together do not come back together.
  function reopenWait() {
    var base = Math.min(REOPEN_FIRST * Math.pow(2, Math.min(streamFails, 5)), REOPEN_LAST);
    return base + Math.floor(Math.random() * REOPEN_JITTER);
  }

  // asleep: the page is hidden or left. The stream is closed and a reload
  // waiting its turn is dropped (the change stays pending); a poll in
  // flight runs out, as before, and none is asked after it.
  function asleep() {
    cancel();
    cancelReload();
    closeStream();
  }

  // awake: the page is shown again, or the network is back. A list with a
  // change pending reloads for it, once; a Leads page acts on it once. A page that streams (or fell back
  // from the stream) opens one stream after a short random wait, with the
  // version it shows, so a change it missed is said at once; any other page
  // polls at once.
  function awake() {
    if (stopped || document.hidden) { return; }
    cancel();
    // A change heard of while the page was shown, whose reload the page's
    // hiding dropped: one reload now, by the usual rule.
    if (pendingChange && !onCard && !still) { if (onLeads) { chatChanged(); } else { wantReload(); } }
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

  // A page made while its view was not live, and a Leads page in a browser
  // that cannot read the stream (it has no poll), say so and ask nothing.
  if (!live || (onLeads && !streamable)) {
    say(TEXT.notLive);
    return;
  }
  next();
})();
