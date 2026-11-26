# The witness overlay extends the composed seat and notifier commands.
# The health commands of the release, run by ah-switch after the reload (see
# ../README.md, "Layout"); every line must exit 0. The front end, then the
# seat app on its own socket (its /healthz answers once it serves).
curl -fsS --max-time 5 http://127.0.0.1/healthz
curl -fsS --max-time 5 --unix-socket /run/seat/http.sock http://localhost/healthz
# Verify a fresh signed local witness status.
{release}/bin/ataniq-witness status --config /etc/authority-host/witness/config.json
