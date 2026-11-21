- The inject fence's result line now carries the shape of the screen it read,
  numbers and flags only, no text, after the failed conditions: `screen WxH
  alt B cursor X,Y last-row N` (the size, the alternate-screen flag, the
  cursor, and the last row that holds anything). When the screen is not one the recogniser knows, the line says
  `composer not read` instead of `composer false empty false ...`, which read
  as a composer that holds text. A `not-now` on a live claude now says whether
  the screen was on the alternate screen and where the cursor was. The
  recogniser's rules and fixtures are unchanged.
