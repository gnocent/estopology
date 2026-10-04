#!/usr/bin/env bash
# Starts essim (two clusters) and estopo with a fresh data directory for the
# Playwright tests, then waits. Everything stops with this script.
set -euo pipefail
cd "$(dirname "$0")/../.."
export PATH="$PATH:/usr/local/go/bin" GOFLAGS=-buildvcs=false
work=/tmp/estopo-e2e
rm -rf "$work" && mkdir -p "$work"
go build -o "$work/estopo" ./cmd/estopo
go build -o "$work/essim" ./cmd/essim
trap 'kill 0' EXIT INT TERM

"$work/essim" -clusters small,c -insecure -port 29600 -version 9.5.4 -speed 60 > "$work/essim.log" 2>&1 &
for _ in $(seq 1 60); do grep -q "clu-c" "$work/essim.log" 2>/dev/null && break; sleep 0.5; done

ESTOPO_LISTEN=127.0.0.1:18093 ESTOPO_DATA_DIR="$work/data" "$work/estopo" serve > "$work/estopo.log" 2>&1 &
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:18093/healthz > /dev/null && break; sleep 0.5; done

put() {
  curl -sf -o /dev/null -X PUT -H "Content-Type: application/json" "http://127.0.0.1:18093/api/v1/admin/clusters/$1" -d "$2"
}
put small '{"name":"Small","enabled":true,"machine_attr":"machine","es":{"URLs":["http://127.0.0.1:29600"],"Auth":"none"}}'
put cold '{"name":"Cold","enabled":true,"machine_attr":"machine","es":{"URLs":["http://127.0.0.1:29601"],"Auth":"none"}}'

# Wait for both first full captures.
for _ in $(seq 1 120); do
  n=$(curl -sf http://127.0.0.1:18093/api/v1/admin/collectors | grep -o '"last_full":{[^}]*"status":"ok"' | wc -l || true)
  [ "$n" -ge 2 ] && break
  sleep 0.5
done
echo "ready"
wait
