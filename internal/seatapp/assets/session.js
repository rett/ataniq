// The session's one control: log out. The seat ends the session and
// removes its cookie; this script never sees the cookie's value. The phone
// goes to the login page only when the seat answered that the session ended:
// a 200 that was not redirected (a redirect fails the fetch) whose body is
// the seat's JSON acknowledgement, {"next": "/login/"}. When no answer came,
// or another (a proxy's page, say), the session may still be live, and the
// page says so and leaves the button to tap again.
"use strict";
(function () {
  var button = document.getElementById("logout");
  var statusEl = document.getElementById("logout-status");
  if (!button || !statusEl) { return; }
  button.addEventListener("click", async function () {
    button.disabled = true;
    statusEl.textContent = "";
    var ended = false;
    try {
      var resp = await fetch("/api/logout", {
        method: "POST", credentials: "same-origin", cache: "no-store", redirect: "error", referrer: "", referrerPolicy: "strict-origin",
        headers: { "Content-Type": "application/json" }, body: "{}"
      });
      var data = null;
      if (resp.status === 200 && !resp.redirected) { data = await resp.json(); }
      ended = !!data && typeof data === "object" && data.next === "/login/";
    } catch (e) { ended = false; }
    if (ended) {
      location.assign("/login/");
      return;
    }
    statusEl.textContent = "Logout not confirmed: the seat did not answer that the session ended. Tap again, or close this browser.";
    button.disabled = false;
  });
  button.hidden = false;
})();
