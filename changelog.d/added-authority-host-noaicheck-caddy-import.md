- The authority host's no-AI check follows the `import` lines of a release's
  Caddyfile that name an absolute path (a glob in the last component too) and
  reads each file on the host, as it reads an owner-local `EnvironmentFile=`:
  the shipped Caddyfile's `/etc/authority-host/seat/site.caddy` is no longer
  outside the check. An imported file gets the placeholder scan of the
  release's own files (a name like a provider key is a `HIT env`) and a new
  one, `HIT proxy <file> outbound-target`, for a `reverse_proxy` upstream that
  is not a Unix socket, `localhost`, a loopback address or a bare port. A file
  that is not there is `HIT incomplete caddy-import <path>`; the import is a
  pass only when the file was read.
