// The session's one control: log out. The seat ends the session and
// removes its cookie; this script never sees the cookie's value. The phone
// goes to the login page only when the seat answered that the session ended;
// when no answer came, or another, the session may still be live, and the
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
        method: "POST", credentials: "same-origin", cache: "no-store", referrer: "", referrerPolicy: "strict-origin",
        headers: { "Content-Type": "application/json" }, body: "{}"
      });
      ended = resp.ok;
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
