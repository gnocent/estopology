#!/usr/bin/env bash
# Runs the LDAP tests of estopo against a real OpenLDAP (LDAPS and StartTLS),
# then removes the container. Usage (Linux with Docker): testenv/ldap/run.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
work=$(mktemp -d /tmp/estopo-ldap.XXXXXX)
name=estopo-test-ldap
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

# Test PKI: root CA -> server certificate for localhost / 127.0.0.1.
openssl ecparam -name prime256v1 -genkey -noout -out "$work/ca.key"
openssl req -x509 -new -key "$work/ca.key" -days 2 -subj "/CN=estopo LDAP test CA" -out "$work/ca.crt"
openssl ecparam -name prime256v1 -genkey -noout -out "$work/server.key"
openssl req -new -key "$work/server.key" -subj "/CN=localhost" -out "$work/server.csr"
printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' > "$work/ext"
openssl x509 -req -in "$work/server.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial \
  -days 2 -extfile "$work/ext" -out "$work/server.crt" 2>/dev/null
chmod 644 "$work"/*
printf 'service-secret\n' > "$work/bind.pw"

docker build -q -t estopo-test-openldap "$here" >/dev/null
docker rm -f "$name" >/dev/null 2>&1 || true
docker run -d --name "$name" -p 127.0.0.1:1636:636 -p 127.0.0.1:1389:389 -v "$work:/certs:ro" estopo-test-openldap >/dev/null
for _ in $(seq 1 60); do
  docker logs "$name" 2>&1 | grep -q "slapd starting" && break
  sleep 0.5
done
docker exec "$name" slapd -VV 2>&1 | head -1

cd "$root"
export PATH="$PATH:/usr/local/go/bin" GOFLAGS=-buildvcs=false
ESTOPO_LDAP_LIVE=1 ESTOPO_LDAP_CA="$work/ca.crt" ESTOPO_LDAP_BIND_PW="$work/bind.pw" \
  go test -count=1 -run TestLiveLDAP -v ./internal/auth
