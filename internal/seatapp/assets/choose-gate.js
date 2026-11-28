// The desk QR carries only a fragment. The owner picks the issuing gate
// before any pairing request, and the code stays out of HTTP and history.
"use strict";
(function () {
  var fragment = location.hash;
  history.replaceState(null, "", location.pathname);
  document.querySelectorAll("button[data-pair]").forEach(function (button) {
    button.addEventListener("click", function () {
      var path = button.dataset.pair;
      if (/^\/g\/[0-9a-f]{64}\/pair\/$/.test(path)) { location.replace(path + fragment); }
    });
  });
})();
