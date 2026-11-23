- The seat can now turn notices on from the phone. When `ataniq-seat` runs
  with `--notify-socket` (a notifier that also takes subscriptions), the card
  list shows a Notices section with Enable notices and Stop notices buttons and
  a status line: notices work only from the Home Screen app on iPhone, so the
  page says to add it to the Home Screen, open it from there and tap Enable.
  The tap asks the phone's permission, registers a service worker (`/sw.js`)
  and hands the notifier the phone's push subscription through
  `POST /api/push/subscribe`; Stop notices drops it through
  `POST /api/push/unsubscribe` (the subscription is named by the SHA-256 of its
  address), and `POST /api/push/info` gives the push key. These calls need a
  session and are limited to 6 a minute for a credential. The seat serves a web
  app manifest (`/manifest.webmanifest`, the Home Screen app opens on
  `/cards/`) and its icons. A notice shows only "N waiting", or "Cards are
  waiting" for anything else, has no buttons and carries nothing of a card, and
  a tap on it opens the card list. The content security policy gains
  `manifest-src 'self'` and `worker-src 'self'`. Without `--notify-socket`
  these routes answer 404 and the pages show nothing about notices.
