- The seat's content security policy pins the Trusted Types policy names with
  `trusted-types seat-sw`, the one policy `push.js` makes to register the
  service worker, with no `default` and no `allow-duplicates`, so a script that
  runs on the origin cannot make a policy of its own. A test holds that every
  policy any script creates is named, and no other.
