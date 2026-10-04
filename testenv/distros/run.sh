#!/usr/bin/env bash
# Starts the static estopo binary in RHEL-like containers (SPEC §13.2):
# AlmaLinux and Rocky Linux 8, 9 and 10, as an unprivileged user, over HTTPS,
# and checks /healthz with the CA. Usage (Linux with Docker): testenv/distros/run.sh
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d /tmp/estopo-distros.XXXXXX)
trap 'rm -rf "$work"' EXIT
export PATH="$PATH:/usr/local/go/bin"

(cd "$root" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$work/bin/estopo" ./cmd/estopo)

openssl ecparam -name prime256v1 -genkey -noout -out "$work/ca.key"
openssl req -x509 -new -key "$work/ca.key" -days 2 -subj "/CN=estopo distro test CA" -out "$work/ca.crt"
openssl ecparam -name prime256v1 -genkey -noout -out "$work/server.key"
openssl req -new -key "$work/server.key" -subj "/CN=localhost" -out "$work/server.csr"
printf 'subjectAltName=DNS:localhost\nextendedKeyUsage=serverAuth\n' > "$work/ext"
openssl x509 -req -in "$work/server.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial \
  -days 2 -extfile "$work/ext" -out "$work/server.crt" 2>/dev/null
cat "$work/server.crt" "$work/ca.crt" > "$work/fullchain.pem"
mkdir -p "$work/certs" && cp "$work/ca.crt" "$work/fullchain.pem" "$work/server.key" "$work/certs/"
chmod -R a+rX "$work"

cat > "$work/check.sh" <<'IN'
set -eu
. /etc/os-release
export ESTOPO_LISTEN=127.0.0.1:8443 ESTOPO_DATA_DIR=/tmp/estopo-data \
  ESTOPO_TLS_CERT=/certs/fullchain.pem ESTOPO_TLS_KEY=/certs/server.key \
  ESTOPO_ADMIN_PASSWORD=initial-admin-password ESTOPO_LOG_FORMAT=json
/opt/estopo/estopo version
/opt/estopo/estopo serve > /tmp/estopo.log 2>&1 &
for _ in $(seq 1 50); do
  if out=$(curl -sf --cacert /certs/ca.crt https://localhost:8443/healthz); then
    echo "$PRETTY_NAME (uid $(id -u)): $out"
    kill %1
    exit 0
  fi
  sleep 0.2
done
echo "$PRETTY_NAME: no /healthz" >&2
cat /tmp/estopo.log >&2
exit 1
IN

fail=0
for image in almalinux:8 almalinux:9 almalinux:10 rockylinux/rockylinux:8 rockylinux/rockylinux:9 rockylinux/rockylinux:10; do
  if ! docker run --rm --user 65534:65534 --network none \
      -v "$work/bin:/opt/estopo:ro" -v "$work/certs:/certs:ro" -v "$work/check.sh:/check.sh:ro" \
      "$image" /bin/sh /check.sh; then
    echo "FAILED: $image" >&2
    fail=1
  fi
done
exit $fail
