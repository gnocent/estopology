#!/usr/bin/env bash
# Starts a small secured Elasticsearch cluster of the given version in Docker,
# shapes it (tiers, awareness, CCS remotes, constrained and red indices, an
# ongoing relocation, then a lost node) and records the raw responses of every
# API estopo uses (SPEC §5.1, §5.2) into testdata/es/<version>/.
#
# Usage (Linux with Docker): scripts/capture-fixtures.sh 8.19.22
# Test credentials below are throwaway values for this local cluster only.
set -euo pipefail
cd "$(dirname "$0")/.."
ver=${1:?usage: capture-fixtures.sh <es version>}
work=${ESTOPO_TESTENV:-$HOME/.cache/estopo-testenv}
pki=$work/pki
out=$PWD/testdata/es/$ver
port=19200
url=https://localhost:$port
export ES_VERSION=$ver PKI_DIR=$pki ES_PORT=$port

compose() { docker compose -p estopo-fx -f testenv/es/docker-compose.yml "$@"; }
log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }

bash testenv/es/pki.sh "$pki" >/dev/null
compose down -v --remove-orphans >/dev/null 2>&1 || true
trap 'compose down -v --remove-orphans >/dev/null 2>&1 || true' EXIT
log "starting ES $ver"
compose up -d --quiet-pull 2>/dev/null

# Admin calls (setup only).
es() {
  local m=$1 p=$2; shift 2
  curl -sS --max-time 120 --cacert "$pki/ca-chain.crt" -u elastic:estopo-test \
    -H 'Content-Type: application/json' -X "$m" "$url$p" "$@"
}
esq() { es "$@" >/dev/null; }
bulk() { # ndjson file
  local r
  r=$(curl -sS --max-time 300 --cacert "$pki/ca-chain.crt" -u elastic:estopo-test \
    -H 'Content-Type: application/x-ndjson' -X POST "$url/_bulk?refresh=true" --data-binary "@$1")
  [[ $(jq -r '.errors' <<<"$r") == false ]] || { log "bulk failed: ${r:0:300}"; exit 1; }
}

for i in $(seq 1 90); do
  n=$(es GET '/_cluster/health' 2>/dev/null | jq -r '.number_of_nodes // 0' 2>/dev/null || echo 0)
  [[ $n == 4 ]] && break
  sleep 2
done
[[ $n == 4 ]] || { log "cluster did not form"; compose logs --tail 40; exit 1; }
log "cluster formed"

# --- cluster settings, CCS remotes ------------------------------------------
esq PUT /_cluster/settings -d '{
  "persistent": {
    "cluster.routing.allocation.awareness.attributes": "dc",
    "cluster.remote.self.seeds": ["es01:9300"],
    "cluster.remote.ghost.seeds": ["ghost.invalid:9300"],
    "cluster.remote.ghost.skip_unavailable": true,
    "cluster.remote.prx.mode": "proxy",
    "cluster.remote.prx.proxy_address": "es02:9300"
  },
  "transient": { "cluster.routing.allocation.node_concurrent_recoveries": 2 }
}'

# --- estopo identities: role, basic user, PKI mapping, API key --------------
role='{"cluster":["monitor"],"indices":[{"names":["*"],"privileges":["monitor","view_index_metadata"],"allow_restricted_indices":true}]}'
esq PUT /_security/role/estopo_reader -d "$role"
esq PUT /_security/user/estopo_basic -d '{"password":"estopo-basic-test","roles":["estopo_reader"]}'
esq PUT /_security/role_mapping/estopo_pki -d '{"roles":["estopo_reader"],"enabled":true,
  "rules":{"all":[{"field":{"realm.name":"pki1"}},{"field":{"username":"estopo"}}]}}'
key=$(es POST /_security/api_key -d "{\"name\":\"estopo\",\"role_descriptors\":{\"estopo_reader\":$role}}")
apikey=$(jq -r '.encoded // empty' <<<"$key")
[[ -n $apikey ]] || apikey=$(jq -r '"\(.id):\(.api_key)"' <<<"$key" | base64 -w0)

# --- indices -----------------------------------------------------------------
esq PUT /_ilm/policy/estopo-test -d '{"policy":{"phases":{"hot":{"actions":{}}}}}'
mk() { esq PUT "/$1?wait_for_active_shards=0" -d "{\"settings\":$2}"; }
hot='"index.routing.allocation.include._tier_preference":"data_hot"'
for d in 28 29 30; do
  mk "applog-2026.09.$d" "{\"number_of_shards\":2,\"number_of_replicas\":1,$hot,\"index.lifecycle.name\":\"estopo-test\",\"index.refresh_interval\":\"30s\"}"
done
mk weblog-000001 "{\"number_of_shards\":1,\"number_of_replicas\":1,$hot}"
mk weblog-000002 "{\"number_of_shards\":1,\"number_of_replicas\":1,$hot,\"index.translog.durability\":\"async\"}"
esq POST /_aliases -d '{"actions":[{"add":{"index":"weblog-000002","alias":"weblog","is_write_index":true}}]}'
mk archive-2026.08 '{"number_of_shards":1,"number_of_replicas":0,"index.routing.allocation.include._tier_preference":"data_warm","index.priority":10}'
mk cold-2026.07 '{"number_of_shards":1,"number_of_replicas":0,"index.routing.allocation.include._tier_preference":"data_cold,data_warm"}'
mk tight-tspn "{\"number_of_shards\":2,\"number_of_replicas\":1,$hot,\"index.routing.allocation.total_shards_per_node\":1}"
mk overrep "{\"number_of_shards\":1,\"number_of_replicas\":2,$hot}"
mk red-require "{\"number_of_shards\":1,\"number_of_replicas\":0,$hot,\"index.routing.allocation.require._name\":\"nope\"}"
mk exclude-es01 "{\"number_of_shards\":1,\"number_of_replicas\":1,$hot,\"index.routing.allocation.exclude._name\":\"es01\",\"index.unassigned.node_left.delayed_timeout\":\"5m\"}"
mk closed-idx "{\"number_of_shards\":1,\"number_of_replicas\":0,$hot}"
mk .hidden-idx "{\"number_of_shards\":1,\"number_of_replicas\":0,$hot,\"index.hidden\":true}"
mk big-reloc "{\"number_of_shards\":1,\"number_of_replicas\":0,$hot,\"index.routing.allocation.require._name\":\"es01\"}"

bulk=$work/bulk-$$.ndjson
for d in 28 29 30; do
  awk -v idx="applog-2026.09.$d" 'BEGIN{for(i=0;i<2000;i++){printf "{\"index\":{\"_index\":\"%s\"}}\n{\"n\":%d,\"msg\":\"event %d\"}\n", idx, i, i}}' > "$bulk"
  bulk "$bulk"
done
# ~24 MB of incompressible payload so that the relocation below lasts minutes.
head -c 18000000 /dev/urandom | base64 -w 1000 |
  awk '{printf "{\"index\":{\"_index\":\"big-reloc\"}}\n{\"blob\":\"%s\"}\n", $0}' > "$bulk"
bulk "$bulk"
rm -f "$bulk"
esq POST /_flush
esq POST /closed-idx/_close
esq PUT /applog-2026.09.28/_settings -d '{"index.blocks.write":true}'
esq GET '/_cluster/health?wait_for_no_initializing_shards=true&timeout=60s'

# --- start a slow relocation --------------------------------------------------
esq PUT /_cluster/settings -d '{"transient":{"indices.recovery.max_bytes_per_sec":"100kb"}}'
esq PUT /big-reloc/_settings -d '{"index.routing.allocation.require._name":"es02"}'
reloc=no
for i in $(seq 1 30); do
  es GET '/_cat/shards/big-reloc?h=state' | grep -q RELOCATING && { reloc=yes; break; }
  sleep 1
done
log "relocation in progress: $reloc"

# --- capture ------------------------------------------------------------------
rm -rf "$out"; mkdir -p "$out/headers"
printf 'name\tpath\thttp_code\tsize_download\ttime_total\n' > "$out/calls.tsv"

cap() { # name path [json body, sent with GET]
  local name=$1 path=$2 body=${3:-}
  local args=(-sS --max-time 120 --cacert "$pki/ca-chain.crt" --cert "$pki/client.crt" --key "$pki/client.key"
    --compressed -D "$out/headers/$name.txt" -o "$out/$name.json" -w '%{http_code}\t%{size_download}\t%{time_total}')
  [[ -n $body ]] && args+=(-X GET -H 'Content-Type: application/json' --data-binary "$body")
  printf '%s\t%s\t%s\n' "$name" "$path" "$(curl "${args[@]}" "$url$path")" >> "$out/calls.tsv"
}

wl='index.number_of_shards,index.number_of_replicas,index.auto_expand_replicas,index.routing.allocation.*,index.routing.rebalance.enable,index.blocks.*,index.unassigned.node_left.delayed_timeout,index.priority,index.refresh_interval,index.translog.durability,index.lifecycle.name,index.version.created,index.creation_date,index.uuid,index.hidden'
ns_fp='cluster_name,_nodes,nodes.*.name,nodes.*.host,nodes.*.ip,nodes.*.timestamp,nodes.*.roles,nodes.*.os.cpu,nodes.*.os.cgroup,nodes.*.process.cpu,nodes.*.jvm.uptime_in_millis,nodes.*.jvm.mem.heap_used_percent,nodes.*.fs.total,nodes.*.fs.io_stats.total'
n_fp='cluster_name,_nodes,nodes.*.name,nodes.*.host,nodes.*.ip,nodes.*.version,nodes.*.roles,nodes.*.attributes,nodes.*.transport_address,nodes.*.http.publish_address,nodes.*.os.pretty_name,nodes.*.os.name,nodes.*.os.version,nodes.*.os.arch,nodes.*.os.available_processors,nodes.*.jvm.version,nodes.*.jvm.start_time_in_millis'
cs_h='index,shard,prirep,state,docs,store,id,node,unassigned.reason,unassigned.at,unassigned.details'
ci_h='uuid,index,status,health,pri,rep,docs.count,store.size,pri.store.size,creation.date'
privs='{"cluster":["monitor"],"index":[{"names":["*"],"privileges":["monitor","view_index_metadata"],"allow_restricted_indices":true}]}'

calls=(
  "root|/"
  "health|/_cluster/health"
  "nodes_stats|/_nodes/stats/os,process,jvm,fs"
  "nodes_stats_filtered|/_nodes/stats/os,process,jvm,fs?filter_path=$ns_fp"
  "cat_allocation|/_cat/allocation?format=json&bytes=b"
  "cluster_settings|/_cluster/settings?flat_settings=true"
  "cluster_settings_defaults|/_cluster/settings?flat_settings=true&include_defaults=true"
  "remote_info|/_remote/info"
  "nodes|/_nodes/os,jvm,http,transport"
  "nodes_filtered|/_nodes/os,jvm,http,transport?filter_path=$n_fp"
  "cat_indices|/_cat/indices?format=json&bytes=b&expand_wildcards=all&h=$ci_h"
  "index_settings|/_all/_settings/$wl?flat_settings=true&expand_wildcards=all"
  "cat_shards|/_cat/shards?format=json&bytes=b&h=$cs_h"
)
for c in "${calls[@]}"; do cap "${c%%|*}" "${c#*|}"; done
cap has_privileges /_security/user/_has_privileges "$privs"
for h in shards indices allocation; do # column reference, text format
  curl -sS --cacert "$pki/ca-chain.crt" --cert "$pki/client.crt" --key "$pki/client.key" \
    "$url/_cat/$h?help" > "$out/cat_${h}_help.txt"
done
log "main capture done"

# --- every call with each auth method (status only) ---------------------------
printf 'auth\tname\thttp_code\n' > "$out/auth.tsv"
for c in "${calls[@]}"; do
  name=${c%%|*} path=${c#*|}
  for auth in pki basic apikey none; do
    case $auth in
      pki) a=(--cert "$pki/client.crt" --key "$pki/client.key") ;;
      basic) a=(-u estopo_basic:estopo-basic-test) ;;
      apikey) a=(-H "Authorization: ApiKey $apikey") ;;
      none) a=() ;;
    esac
    code=$(curl -sS --max-time 120 --cacert "$pki/ca-chain.crt" "${a[@]}" -o /dev/null -w '%{http_code}' "$url$path")
    printf '%s\t%s\t%s\n' "$auth" "$name" "$code" >> "$out/auth.tsv"
  done
done

# --- the Go client itself against this cluster (all auth methods) -----------
if command -v go >/dev/null || [[ -x /usr/local/go/bin/go ]]; then
  log "running the Go client live test"
  ESTOPO_LIVE_URL=$url ESTOPO_LIVE_PKI=$pki ESTOPO_LIVE_APIKEY=$apikey ESTOPO_LIVE_PASSWORD=estopo-basic-test \
    GOFLAGS=-buildvcs=false PATH="$PATH:/usr/local/go/bin" \
    go test -count=1 -run TestLive -v ./internal/esclient/ > "$work/live-$ver.log" 2>&1 \
    && log "live test ok" || { log "LIVE TEST FAILED (see $work/live-$ver.log)"; tail -30 "$work/live-$ver.log" >&2; }
fi

# --- a data node leaves: unassigned replicas, delayed allocation, promotions -
log "stopping es02"
compose stop es02 >/dev/null 2>&1
for i in $(seq 1 60); do
  n=$(es GET '/_cluster/health' | jq -r '.number_of_nodes // 0' 2>/dev/null || echo 0)
  [[ $n == 3 ]] && break
  sleep 2
done
sleep 3
for c in "health|/_cluster/health" "cat_shards|/_cat/shards?format=json&bytes=b&h=$cs_h" \
         "cat_allocation|/_cat/allocation?format=json&bytes=b" \
         "nodes_stats_filtered|/_nodes/stats/os,process,jvm,fs?filter_path=$ns_fp"; do
  cap "${c%%|*}_nodeleft" "${c#*|}"
done

{
  echo "version: $ver"
  echo "image: $(docker image inspect --format '{{index .RepoDigests 0}}' "docker.elastic.co/elasticsearch/elasticsearch:$ver")"
  echo "captured: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "$out/meta.txt"
log "fixtures written to $out"
