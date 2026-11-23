// The session's one control: log out. The seat ends the session and
// removes its cookie; this script never sees the cookie's value.
"use strict";
(function () {
  var button = document.getElementById("logout");
  if (!button) { return; }
  button.addEventListener("click", async function () {
    button.disabled = true;
    try {
      await fetch("/api/logout", {
        method: "POST", credentials: "same-origin", cache: "no-store", referrer: "", referrerPolicy: "strict-origin",
        headers: { "Content-Type": "application/json" }, body: "{}"
      });
    } catch (e) { /* the login page, below, is where the phone goes either way */ }
    location.assign("/login/");
  });
  button.hidden = false;
})();
