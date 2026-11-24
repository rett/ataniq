- The seat refuses a POST that carries `Sec-Fetch-Site` or `Content-Type`
  twice, and any request with two `__Host-seat` cookies, now counted before
  they are parsed, so a second cookie with a malformed value no longer hides.
  The card-list stream takes a body of exactly one string member `version`
  (absent, null or repeated is 400) and opens only once the seat's watcher
  has a version.
