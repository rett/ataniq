curl -fsS --max-time 5 http://127.0.0.1/healthz
curl -fsS --max-time 5 --unix-socket /run/seat/http.sock http://localhost/healthz
{release}/bin/ataniq-witness status --config /etc/authority-host/witness/config.json
