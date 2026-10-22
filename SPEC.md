*(Version française : [SPEC_fr.md](SPEC_fr.md))*

# ES.Topo — Project specification

> Lightweight history of Elasticsearch cluster topology (7.17 → 9.x) and exploration over time: the state of a cluster at a given date, the differences and the path taken between two dates.

Status: **v1.0 — matches the implementation** (2026-10-01). This version adds to the v0.1 scoping draft (2026-09-30) the clarifications and decisions made during implementation. Departures from v0.1 are marked **(v1.0)**.

**v1.1** (2026-10-01): the interface was reworked after a trial on simulated data (help in the screens, look, one page per element of the topology, richer logical view, family charts). These changes are marked **(v1.1)**.

**v1.2** (2026-10-04): security and robustness pass before the first public release (0.8.0-beta), and alignment of this document with the code: local listener by default, creation of the first administrator, secrets directory, sessions, requests coming from another site, instance identifier and blocked replication, three build targets. These changes are marked **(v1.2)**.

**v1.3** (2026-10-22): security fixes of version 0.8.1-beta: LDAP identity read from the entry found, sessions closed on a mode change and on the standby, login throttling. These changes are marked **(v1.3)**.

---

## 1. Problem and users

- **Problem**: during a cluster incident (lost nodes, unassigned shards, massive rebalancing, an upgrade going wrong…), there is no **historical** view of the topology: where the shards were, which nodes existed, with which attributes, and when that changed. Monitoring tools provide metrics, not topology.
- **Users**: the people who operate Elasticsearch clusters, during an incident and in post-mortems.
- **Target sizing**: around fifteen clusters, versions **7.17 to 9.x**, up to **~250 nodes** and **~250,000 shards** per cluster, tens of thousands of indices, several ES nodes per physical machine, several datacenters, hot/warm/cold tiers, **CCS**. Data streams, the frozen tier, searchable snapshots and CCR get no specific treatment.
- **Works without external network access**: deployment comes down to copying a binary. Everything (UI, fonts, assets) is embedded.

## 2. Scope

### 2.1 In scope

- Periodic, adaptive collection of the topology of N clusters (list and parameters managed in the admin).
- Compact historical storage in an embedded database, with automatic retention (duration and max size).
- Web UI: state of a cluster at a date T, difference and **path taken** between two dates, event journal, losses and disappearances, search, allocation constraints, relocation flows.
- Built-in administration (clusters, scheduling, retention, storage, collector status, users, HA).
- Authentication: none / local accounts / LDAP (OpenLDAP over LDAPS), reader and admin roles.
- Simple high availability: passive standby instance, manual promotion.
- ES cluster simulator for tests, performance measurements and demonstration data (v1.2).

### 2.2 Out of scope

- Alerting / notifications (emails, webhooks…).
- Performance monitoring (indexing/search rates, latencies, GC…): that is the job of the existing tooling.
- Data troubleshooting: mappings, aliases, document contents.
- Any **write** to the ES clusters: the tool is strictly read-only (§5.6).
- ILM, template, snapshot/SLM management.
- Automatic HA failover.
- RPM packaging (bare binary + example systemd unit).
- ECK/Kubernetes and Elastic Cloud specifics.

## 3. Volume and disk budget

Sizing assumptions: ~1.1 M shards in total, ~300 k indices, ~2,000 nodes, ~100 bytes/row including indexes, 2–5% of shards actively written, weekly partitions.

| Item | Per year (estimate) |
|---|---|
| Weekly keyframes (copy of the full state at the start of each partition) | ~6 GB |
| Structure intervals (creations, deletions, relocations, state changes) | 4–7 GB |
| Shard and index sizes/doc counts (deadband) | ~18 GB |
| Node metrics every 5 min (deadband) + health | 5–12 GB |
| Settings (deduplicated), cluster settings, CCS, events | < 2 GB |
| **Total** | **~35–45 GB/year — plan for 100–200 GB for 1 year, ~10 GB for 30 days** |

**(v1.0) Measured on the simulated dataset** (§13, 30 days backfilled, 373,000 shard copies): **1.015 GB** of partitions, i.e. about **1×** the estimate scaled down to this dataset (0.98 to 1.25 GB). A full week of cluster A (245,000 copies) weighs about 150 MB, i.e. about 35 GB/year extrapolated to the assumptions above. Keyframes cost more than the 100-bytes-per-row assumption (about 250 bytes per open copy, indexes included), but the other items cost less. Measured on simulated data only. The admin shows, per cluster, the bytes written per day and a 30-day / 1-year projection (§9).

Note: naive storage (a full snapshot every hour) would take ~1 TB/year. The interval model was chosen mainly for **performance** (diff, path and search remain indexed queries over few rows); the disk saving is secondary.

## 4. Architecture and decisions

| Decision | Rationale |
|---|---|
| Go, single static binary `CGO_ENABLED=0`, targets `linux/amd64`, `windows/amd64` and `darwin/arm64` | No glibc dependency → identical on RHEL-like 8/9/10 distributions; deployment = copying the binary. |
| **Pure-Go SQLite** (`modernc.org/sqlite`), WAL mode | No CGO. The volume (tens of GB/year) does not call for a columnar engine. DuckDB and chDB ruled out: CGO, size, glibc compatibility. |
| **One database per cluster and per ISO week** (UTC) + a `meta.db` database | Writes from 15 clusters without contention; retention = deleting files, no `VACUUM`; queries confined to a single file. |
| **Validity-interval model** + keyframe at the start of each partition | Only changes are written; state at T = one query; diff/path = intervals that open/close between A and B. |
| Metrics (sizes, docs, CPU…) kept separate from structure, with a **deadband** | Otherwise every hot shard would create a new interval every hour. |
| Settings and blobs deduplicated by hash (zstd-compressed `blob`, `klauspost/compress`, pure Go) | Thousands of indices share the same settings. |
| **Capture log** (normalized, compressed "records") as the single input to ingestion | Deterministic ingestion: used for HA (log shipping), tests (replay) and debugging. |
| Raw HTTP client (`net/http`), not the official client | Only `_cat`/`_cluster`/`_nodes` APIs that are stable from 7.17 to 9.x are used; the typed client varies across versions. |
| **Streaming** JSON decoding of large responses, `Accept-Encoding: gzip` | `_cat/shards` on 250 k shards = tens of MB. |
| Front end: **Svelte 5 + TypeScript + Vite**, D3 (selected modules) + **canvas** for high cardinalities; compiled build **committed** to the repository and embedded via `go:embed` | A plain `go build` is enough (Node is only needed to modify the UI); nothing to download in air-gapped production. |
| Server-side XLSX export (`excelize`, pure Go) | No heavy JS library, same code for all exports. |
| LDAP via `go-ldap/ldap/v3` (pure Go); local passwords hashed with argon2id (`x/crypto`) | Pure Go, standard. |
| Injectable clock throughout the code (collection, ingestion, retention) | Enables accelerated backfill (30 simulated days in minutes) for tests and performance. |

### 4.1 Project layout

```
cmd/estopo/         main binary (serve, version, admin reset-password)
cmd/essim/          ES cluster simulator (tests, perf, demo)
internal/esclient/  ES HTTP client (TLS, mTLS, API key, basic, read-only)
internal/collector/ scheduling, light probe, full capture, triggers
internal/record/    capture record format (normalized, zstd), spool
internal/store/     meta.db, partitions, interval ingestion, retention, replication
internal/derive/    families, hierarchy, severity, constraints
internal/query/     state at T (cache), diff, path, losses, journal, search
internal/api/       JSON REST API + exports (CSV/JSON/XLSX), /metrics
internal/auth/      local, LDAP, sessions, CSRF, login throttling
internal/ha/        standby replication, promotion, fencing
internal/app/       wiring (ingestion pipeline, retention loops)
internal/sim/       simulation engine (used by essim and the tests)
internal/i18n/      server-side FR/EN catalogues (event messages, exports)
internal/…          (v1.2) other packages: backfill, bench, buildinfo, clock, config, logging, model, testpki
web/                Svelte sources; web/dist/ compiled, committed, embedded
deploy/             example systemd unit and environment file
testenv/            real ES (Docker), OpenLDAP, Rocky/Alma startup
docs/               (v1.2) guides (installation, clusters, authentication, high availability, operations), release notes
scripts/            verification, capture of real responses, licence notices
testdata/           responses of real ES 7.17, 8.19 and 9.5
.github/workflows/  verification on every push, publication of the releases
```

## 5. Collection

### 5.1 Light probe (default: every 5 min, configurable)

| Call | Purpose |
|---|---|
| `GET /` | `cluster_uuid`, name, version (detects an identity change, §5.5) |
| `GET _cluster/health` | status, number of nodes/data nodes, active/primary/relocating/initializing/unassigned/delayed shards, pending tasks |
| `GET _nodes/stats/os,process,jvm,fs` (filter_path) | node presence (id, name) + metrics: `process.cpu.percent` (the node's own CPU, relevant with cpusets), `os.cpu` and load (machine-wide, shown as such), `os.cgroup` if present, heap %, uptime (→ exact start time), `fs.total`, `fs.io_stats` (IO throughput computed from deltas) |
| `GET _cat/allocation?format=json&bytes=b` | shards per node, `disk.indices`, `disk.used/avail/total/percent` |
| `GET _cluster/settings?flat_settings=true` | hash; stored in full when it has changed |
| `GET _remote/info` | CCS: alias, mode, connected, number of connected nodes |

### 5.2 Full capture

| Call | Purpose |
|---|---|
| `GET _nodes` (filter_path) | name, id, host, ip, transport/http addresses, roles, attributes, ES version, `os.pretty_name`, kernel, arch, JVM version |
| `GET _cat/indices?format=json&bytes=b&expand_wildcards=all&h=uuid,index,status,health,pri,rep,docs.count,store.size,pri.store.size,creation.date` | indices |
| `GET _all/_settings/<whitelist>?flat_settings=true&expand_wildcards=all` | significant index settings (§5.7) — **no mappings** |
| `GET _cat/shards?format=json&bytes=b&h=index,shard,prirep,state,docs,store,id,node,unassigned.reason,unassigned.at,unassigned.details` | shards (the `node` field of a `RELOCATING` shard contains the target, which is parsed) |
| + the light probe calls | for a consistent snapshot; **(v1.2)** the cluster settings are then read with `include_defaults=true` (watermarks, awareness) |

Each call is timed; duration and response size are recorded and visible in the admin (this makes it possible to measure the actual cost in production, §18).

### 5.3 Adaptive triggering

- **Full capture triggered** when the light probe sees a notable change: health status, node set (appearance/disappearance/new generation), relocating/initializing/unassigned counters above a (configurable) threshold, modified cluster settings.
- **Forced capture** if there has been no full capture for `max_interval` (default 60 min), counted from the start of the collection as long as none was attempted (probes that keep failing never trigger one).
- The schedule runs on a monotonic clock: the system clock stepping back (NTP, restored VM) does not delay the probes; the records keep the wall-clock time.
- **Minimum delay** between two full captures, `min_interval` (default 10 min): in the middle of a crisis, the tool must not add load to a cluster that is already struggling. A trigger occurring during this delay is deferred, not lost.
- "Capture now" button in the admin (also honours the minimum delay, unless an admin explicitly forces it).
- **Jitter** on schedules; the number of simultaneous full captures is limited (default 3, adjustable in the admin, applied at restart) to bound memory usage and load.
- **(v1.0)** Triggering compares the probe with the **last successful full capture**. A failed full capture counts towards `min_interval` (a struggling cluster is not hammered), but the trigger remains due.

### 5.4 Entity identity

- **Cluster**: `cluster_uuid`; bound to the configuration entry at the first capture.
- **Node**: identity = **name**. A change of ES `id` under the same name = **new generation** of the same node (successive "lives", displayed). Two simultaneous nodes with the same name (abnormal): key `(name, id)`, reported by the `node_name_duplicate` event (v1.0) when the situation begins.
- **Machine**: node attribute configured per cluster, otherwise `host`, otherwise IP.
- **Index**: **UUID** (an index deleted then recreated under the same name is a different entity; the UI shows both lives). Name → UUID mapping at capture time via `_cat/indices`.
- **Shard copy**: `(index, shard number, p/r, node)`. A copy that changes node = one interval closed + one interval opened, paired during analysis (relocation, promotion, loss). Unassigned copies: `(index, shard, p/r, NULL, rank)`.

### 5.5 Robustness

- **Capture failure** (timeout, 5xx, no master): recorded as such (`capture.status = failed`), `capture_failed` event, **no interval is closed** (unknown ≠ unchanged). The UI shows an "unknown" band over the period.
- **Partial capture**: if `_cat/shards` or `_cat/indices` fails, nothing is closed on the structure side; the metrics that were obtained are kept.
- **Inconsistency between calls** (index created between `_cat/indices` and `_cat/shards`): provisional entity, resolved at the next capture.
- **`cluster_uuid` change** behind the same URL (VIP repointed, cluster rebuilt): collection for this cluster is **suspended** (the probe is not recorded), an alert is raised in the admin, and admin action is required:
  - **accept**: the configuration is bound to the new identity at the next capture;
  - **handle as a new cluster**: a new configuration reuses the same connection parameters, and the old one is disabled while keeping its history.
- **(v1.0)** A failure of `_all/_settings` leaves the capture `ok` with "unknown" settings (the previous ones are kept). A failure of `_nodes`, `_cat/indices` or `_cat/shards` makes it `partial` (nothing structural is closed). **(v1.2)** A `_nodes` or `_nodes/stats` response that reports failed nodes, or holds no node at all, is treated as a failure of the call: no node is declared gone (a failed `_nodes` makes the capture `partial`). A failure of a side call (`_nodes/stats`, `_cat/allocation`, `_cluster/settings`, `_remote/info` other than a 400) leaves out its section only, which is unknown, not empty (no node declared gone, no remote closed, the previous cluster settings kept): a probe is then `partial`, a full capture stays `ok` and keeps its structure, with the error noted.
- System and hidden indices are included; UI filter "Hide system indices" (on by default, carried in the URL, applies to all views).
- Timestamps: collector clock in UTC (ms); dates provided by ES (`creation_date`, `unassigned.at`, node start) are stored separately as **exact dates**.

### 5.6 Connection, security and privileges

- TLS 1.2+; authentication: **client certificate (default)**, API key, basic. Secrets and keys **on disk**, referenced by path; optional passphrase via a file. **(v1.2)** API key or basic authentication on an `http://` URL is refused, except towards loopback. Responses are bounded (2 GiB decoded). If `ESTOPO_SECRETS_DIR` is set, every file path typed in the admin must be inside that directory (§12.1).
- CA: a **default CA** (PEM file, or directory from which every `*.pem`/`*.crt`/`*.cer` is loaded: the company's root + intermediates; set in the admin's Clusters tab and checked on save), overridable per cluster; "Add the system CA store" option. With no CA configured at all: system store.
- Encrypted PKCS#8 private keys (PBES2, OpenSSL's default) are accepted, with the passphrase read from a file.
- Several URLs per cluster are possible (fallback), usually a single VIP.
- The client allows **only `GET` and `HEAD`** (safeguard coded and tested).
- Privileges: cluster `monitor`; indices `monitor` + `view_index_metadata` on `*` (with `allow_restricted_indices`). **(v1.0) Confirmed** on 7.17.29, 8.19.22 and 9.5.4 with all three authentication methods. The admin's connection test checks the privileges (`_security/user/_has_privileges`).
- `_remote/info` returns 400 if the queried node does not have the `remote_cluster_client` role: treated as "CCS unavailable", not as a capture failure.

### 5.7 Index settings whitelist (default, editable in the admin)

**(v1.2)** `index.number_of_shards`, `index.number_of_replicas`, `index.auto_expand_replicas`, `index.routing.allocation.*` (covers `include`/`exclude`/`require`, `total_shards_per_node`, `enable` and `_tier_preference`), `index.routing.rebalance.enable`, `index.blocks.*`, `index.unassigned.node_left.delayed_timeout`, `index.priority`, `index.refresh_interval`, `index.translog.durability`, `index.lifecycle.name`, `index.version.created`, `index.creation_date`, `index.uuid`, `index.hidden`.

Cluster settings: **all** of them (persistent + transient), kept in history with diffs.

## 6. Data model

### 6.1 Files

```
<data_dir>/
  meta.db                                        configuration, users, acknowledgements, partition catalogue
  clusters/<cluster_id>/<YYYY>-W<ww>.db             one partition per cluster and ISO week (UTC)
  spool/<cluster_id>/<ms>-<seq>-<kind>.rec.zst      capture records (72 h)
  tmp/                                           partition snapshots in transit (HA)
```

### 6.2 `meta.db`

`cluster` (config, active/suspended state, bound uuid, schedule, hierarchy, machine attribute, node reference), `setting` (global settings: retention, whitelist, authentication and LDAP, default CA, parallelism, cache size, `/metrics`), `family_rule` (regex per cluster), `user`, `session`, `node_ack` (acknowledgements, §7.2), `partition` (catalogue: cluster, week, size, sealed), `ha_state`, `audit` (admin actions), `alert`.

**(v1.0)** `session`, `partition`, `alert` and `ha_state` describe the instance itself: they are not replicated to the standby (§11). The audit log is kept for **1 year**; expired sessions are purged every hour.

### 6.3 Partition (cluster × week) — main tables

Times stored as integers (ms UTC). `valid_to IS NULL` = open interval.

- `capture(id, ts, kind[light|full|keyframe], trigger, status[ok|partial|failed], duration_ms, api_stats json, error)`
- `health(ts, status, nodes, data_nodes, active_primary, active, relocating, initializing, unassigned, delayed_unassigned, pending_tasks)` — at every probe
- `node(id, name, es_id, generation, first_seen)` — dimension
- `node_presence(node_id, valid_from, valid_to)` — 5-min resolution (light probe)
- `node_info(node_id, valid_from, valid_to, roles_mask, tiers, ip, transport_port, http_addr, host, machine, es_version, os_name, os_kernel, jvm_version, attrs_blob)` — full-capture resolution
- `node_metric(node_id, ts, proc_cpu, os_cpu, load1, cgroup_cpu, heap_pct, disk_total, disk_used, disk_avail, disk_indices, shards, io_r_ops, io_w_ops, io_r_bytes, io_w_bytes)` — `WITHOUT ROWID`, deadband
- `idx(id, uuid, name, family, creation_date, version_created)` — dimension
- `index_iv(index_id, valid_from, valid_to, status, pri, rep, settings_blob)`
- `index_metric(index_id, ts, docs, store_bytes, pri_store_bytes)` — deadband
- `shard_iv(id, index_id, shard, prirep, node_id, slot, state, reloc_target_node_id, unassigned_reason, unassigned_at, unassigned_details_blob, valid_from, valid_to)`
- `shard_metric(index_id, shard, prirep, node_id, ts, docs, store_bytes)` — deadband (default: ±5% or ±1 GB)
- `cluster_settings_iv(valid_from, valid_to, blob)`
- `remote_iv(alias, valid_from, valid_to, mode, connected, nodes_connected, blob)`
- `event(id, ts_lo, ts_hi, exact_ts, type, severity, subject_kind, subject_ref, group_ref, payload json)`
- `blob(hash, data)` — normalized, compressed content

Indexes (at minimum): `valid_from` and `valid_to` on each interval table; partial index `WHERE valid_to IS NULL`; `shard_iv(index_id, shard)`; `shard_iv(node_id, valid_from)`; `event(ts_hi)`, `event(type, ts_hi)`, `event(subject_kind, subject_ref)`. Writes in batched transactions (one per capture).

### 6.4 Temporal semantics

- **State at T** = state observed by the **last successful full capture ≤ T** (reproducible). Query: `valid_from ≤ Tc AND (valid_to IS NULL OR valid_to > Tc)`. Within a partition, an open interval means "still open at the end of the period covered".
- **Uncertainty window** of a change = `]last capture where the old state was seen, first capture where the new one is seen]` (`ts_lo`, `ts_hi`). The UI shows "between 14:05 and 14:10", or an **exact** date (badge) when ES provides one: index creation (`creation_date`), unassignment (`unassigned.at`), node arrival (timestamp − uptime).
- Node presence and health: probe resolution (5 min). Shard placement: full-capture resolution (≤ 60 min, ≤ 10 min during periods of churn).
- **Keyframe**: at the **first record** of a new week (light probe included, v1.0), all open intervals are copied into the new partition **with their original start date**, together with the **last value of each metric** (otherwise, because of the deadband, the start of the week would have no metrics). A partition can be read on its own.
- **(v1.0)** Static index settings (uuid, `creation_date`, `version.created`, hidden) are carried by the `idx` dimension, not by the settings blob. The first import of a cluster produces no creation events. Unassigned copies are distinguished by a rank (`slot`) among those of the same `(index, shard, p/r)`, sorted by unassignment date.
- Diff/path **spanning several partitions**: reconstruction by natural keys (node name, index UUID), the event journal being continuous from one partition to the next.

### 6.5 Derived events (at ingestion)

`health_changed`, `node_left`, `node_joined`, `node_new_generation`, `node_name_duplicate` (v1.0), `node_upgraded` (ES/OS/JVM version), `node_attrs_changed`, `machine_lost` (all nodes of a machine gone in the same window), `index_created`, `index_deleted`, `index_closed`, `index_opened`, `index_settings_changed`, `cluster_settings_changed`, `shard_relocated`, `shard_promoted`, `shard_lost`, `shard_unassigned`, `shard_assigned`, `remote_disconnected`, `remote_connected`, `capture_failed`, `cluster_uuid_changed`.

### 6.6 Retention and maximum size

- Admin parameters: duration (default 30 days, up to ≥ 1 year) and maximum size of the data directory (default: none).
- Granularity: the **week** (a partition is deleted once its entire period is outside retention).
- Max size exceeded: the oldest partitions are deleted, across all clusters, until the total is back under the threshold (the latest partition of each cluster is never deleted: until the first record of a week, the partition of the week before still holds the state that the keyframe carries on); alert and banner in the admin. **(v1.0)** The size counted is that of the whole data directory, `meta.db` and spool included; only partitions are deleted.
- Cluster removed from the configuration: history kept until it expires. **(v1.2)** The "Purge the history" button (admin) is in the form of the cluster, which can no longer be reached once the cluster is removed: purge before removing. A cluster still collected starts its new history with a full capture (trigger `first`, subject to `min_interval`).

### 6.7 Capture records and determinism

Each probe and capture produces a normalized **record** (sorted JSON, zstd) written atomically to the spool, then ingested. Ingestion depends only on the records and the previous state. Consequences: the standby replays the same records (§11); ingestion tests are replays; at startup, records that were spooled but not yet ingested are replayed. A manual replay tool (to correct a recent ingestion bug) is **postponed** (v1.0).

Records of a cluster are ingested in date order: one older than the last ingested is refused. If the clock of the instance jumped forward then came back (NTP, restored VM), the records dated during the jump put the history ahead of the clock, and the next ones are refused until the clock catches up: a gap in the history. It is not corrected, but reported when the last ingested record is more than an hour ahead of the clock: in the refusal error, in the `estopo_history_ahead_seconds` metric and as a banner of the Collectors page (§9).

## 7. Display-time computations (nothing stored)

### 7.1 Severity (descending order, red → green)

1. Unassigned primaries (red cluster)
2. Nodes missing compared with the reference (§7.2)
3. Unassigned replicas
4. Nodes above the `high` watermark
5. Infeasible allocation constraints (§7.5)
6. Initializing / relocating shards

"Unknown" (failed capture): grey. Default colouring of the topology: **unassigned shards**, then severity; the most worrying issue is shown in red at the top.

### 7.2 Node count reference and acknowledgement

- Reference = set of nodes seen over the **last 7 days (rolling)**.
- A node removed for good "fades out" on its own after 7 days; to clear it immediately: **acknowledge** it (node removed as of date X, comment, author). Restricted to the admin role, recorded in the audit log, revocable.

### 7.3 Index families

Automatic detection of date and rollover suffixes (`yyyy.MM.dd`, `yyyy.MM`, `yyyy-MM-dd`, `yyyyMMdd`, `-000001`, …) → family name. Per-cluster regexes to override it, with a preview of the grouping in the admin.

**(v1.1)** Details:
- The family is derived **from the index name alone** (no alias, data stream or ILM). Suffixes are removed repeatedly (`.ds-logs-app-2026.09.28-000001` → `.ds-logs-app`); a name without a recognised suffix is its own family.
- A four-digit suffix is taken for a year only between **1990 and 2099**: `metrics-8080` stays its own family.
- The family is recomputed at read time with the rules in force: changing a rule regroups the history too.
- **History of a family**: `GET /clusters/{id}/families/{family}/history` samples, between two dates, the sum of documents, of the size on disk and the number of indices (last known reading of each index; a deleted index stops counting).

### 7.4 Navigation hierarchy

Levels configurable per cluster, among: node attribute, tier, machine, node. **(v1.2)** In the admin, the hierarchy is typed as a comma-separated list of words: `tier`, `machine` and `node` are the built-in levels, any other word is a node attribute name (example: `dc, tier, machine, node`); in the API, each level is an object (`{"kind":"attr","attr":"dc"}`, `{"kind":"tier"}`…). Default: the attributes from `cluster.routing.allocation.awareness.attributes` in order, then tier, machine, node.

### 7.5 Allocation constraints (per index, at date T)

For each index: required copies per shard (`1 + replicas`) compared with the eligible nodes (preferred tier available, index and cluster `include`/`exclude`/`require`, nodes below the `high` watermark), with `total_shards_per_node`, with awareness (forced or not) and with `same_shard.host` (copies of the same shard on the same machine). As in Elasticsearch: a `require` with several values needs all of them to match (one is enough for `_ip`, `_host_ip`, `_publish_ip`), and a node without the attribute satisfies no `require`, not even `*`; the `high` watermark is ignored when `cluster.routing.allocation.disk.threshold_enabled` is `false`; from 8.5, a percentage watermark is capped by `cluster.routing.allocation.disk.watermark.high.max_headroom` (150 GB when the watermark is left at its default): a node with at least that much free space is below it.
Classification: **infeasible** / **tight** (becomes infeasible if a node or a machine is lost) / **OK**, with a textual explanation of the limiting constraint. This is a heuristic: it does not replace `_cluster/allocation/explain`.

### 7.6 Losses and disappearances

Over a window [A, B], every copy whose interval closes is classified as:
- **deleted**: the index is deleted in the same window (separate filter);
- **moved**: a copy `(index, shard, p/r)` reappears on another node and the source node is still present;
- **promoted**: a replica that became primary;
- **lost**: its node left the cluster in the same window (or the copy disappeared without a successor).

Losses are **grouped by departed node** (then by machine/rack if the whole machine left). For each lost copy: before (node, zone, p/r, size) and after (promoted replica / unassigned with reason / reallocated on a given node at a given date, never before the loss / **deleted**: its index was deleted afterwards, before the copy was rebuilt / still unassigned at B). The zone is the value of the first awareness attribute, taken from the cluster settings or else from the node configuration (defaults).

### 7.7 Path taken

For a shard copy, an index or a family: the sequence of intervals between A and B (node, state, window), including the round trips that a simple A/B diff cannot show. B is included: a position taken at B is part of the path, as of the state at B. Beyond 20,000 intervals the path is cut and says so (`truncated`); the first intervals in (index, shard, p/r, start) order are kept.

## 8. Web interface

### 8.1 Principles

- **Air-gapped**: everything embedded (JS, CSS, OFL-licensed fonts), no external requests.
- **FR/EN language** and **light/dark theme**, switchable instantly (remembered in the browser and in the profile). Browser **local time**; UTC in a tooltip.
- **High cardinalities**: server-side aggregation (families, top N + "others"), **canvas** rendering for matrices and large sets, drill-down to shard level only on a filtered scope. Pagination/limits on every API list. **(v1.0)** No virtualized tables: server-side pagination bounds the DOM size just as well, and each page remains a permalink.
- **Purposeful animations**: transitions between two dates (nodes appearing/disappearing and tiles sliding in the physical view, interpolated cells in the logical matrix, changing colours), never blocking (about 300 ms), disabled under `prefers-reduced-motion`.
- **Permalinks**: cluster + date(s) + view + filters in the URL.
- Browsers: recent Chrome, Edge, Firefox.
- **(v1.1) Help in the interface**: every screen has a collapsible "Understanding this screen" panel (the choice is remembered per screen in the browser); ambiguous zones and columns carry an "i" icon; coloured views have a legend; values carry their unit. Native tooltips are replaced by a single bubble, in normal text size, on several lines, shown on hover and on keyboard focus.
- **(v1.1) Reference date**: every dated view (cluster, group, node, family, index, constraints) carries the same time band; the date is in the URL (`at`) and follows the navigation.
- **(v1.1) Look**: one set of design tokens for both themes (colours, three shadow levels, radii, durations); translucent surfaces over a fixed background halo, soft gradients on the blocks, an indigo → cyan accent; backdrop blur kept for the header, the search list and the tooltip. **IBM Plex Sans** (OFL, embedded) for text, JetBrains Mono for code. Icons drawn as inline SVG, without a library.
- **(v1.1) Screen transitions**: the browser's View Transitions API, wired in the router. Going down the hierarchy (cluster → group → node, family → index) zooms in, going up zooms out, staying at the same level cross-fades; the clicked link becomes the title of the page it opens. A change of date or filter is never animated at page level. Only `transform` and `opacity` are animated (120 ms on hover, 220 ms for a screen); clicks go through during a transition; everything is off under `prefers-reduced-motion`.

### 8.2 Screens

1. **Multi-cluster home**: one row per cluster, sorted by severity; 7-day health band, counters (nodes / indices / shards / unassigned), versions present, latest notable events.
2. **Cluster at date T**:
   - time band: health band + event density + cursor (dragging it updates the view, with the final rendering on release);
   - **physical** view: DC → rack/machine → nodes as tiles, tiers as bands, choice of colouring (unassigned by default, disk %, process CPU, heap, shard count, version);
   - **logical** view: families × nodes (aggregated matrix, canvas);
   - **(v1.1)** the side detail panel is removed. The cluster state, the missing nodes, the CCS remotes and the versions are in a strip above the topology; the tooltip of a node gives machine, IP, roles, version, attributes, shards, disk, CPU and heap;
   - **(v1.1)** a click on a node opens its page; a click on the name of a frame (zone, tier, machine) opens the page of that group (screen 12). There is no node selection (`?sel=`) any more;
   - **(v1.1)** tier frames are tinted by temperature (hot, warm, cold, frozen, content, master).
   - **(v1.1) Logical view**, every setting kept in the URL:
     - rows per **family** or per **index** (`rows=index`; the largest indices first, then the newest);
     - text **filter**: on family names (index names are searched only when no family matches), or on index names in index mode;
     - number of rows: 30, 60, 120 or all (500 at most), the rest in an "others" row that the colour scale ignores;
     - columns: one node per column, or grouped by a level of the hierarchy (zone, tier in each zone, machine) with the sum written in the cell;
     - in every mode the columns follow the hierarchy, separated by gaps (wider for a higher level) and topped by one band per level (zone, tier in the colour of its temperature, machine).
   - API: `GET /clusters/{id}/matrix?top=&q=&rows=` also returns the total number of matching rows and, in index mode, the UUIDs.
3. **Node**: generations, presence over time, attributes and versions (history), metrics (sparklines), content at T (shards per family). **(v1.1)** Metrics take three quarters of the width (150 px charts, 1,200 points, guide lines at 25/50/75 %). Presence, then attributes and versions, are in the left quarter: version, JVM, OS, roles (tinted by tier) and attributes as tags; earlier periods are folded and what changed is ringed. A new generation (same name, new ES id, hence a data directory emptied or replaced) is explained in place.
4. **Family / index**: list of the family's indices, then index → shards × nodes matrix, settings and their history, **shard path** (timeline per copy, colour = node, interval details), other lives of the same name.
   - **(v1.1)** Family: 7-day charts of the documents, the size on disk and the number of indices (§7.3); the scale of the first two starts from the lowest value.
   - **(v1.1)** Index: the period of the shard path can be chosen (last hour, 6 h, 24 h, 7 days by default, 31 days, or two dates); it ends at the reference date and is cut to the life of the index (nothing before its creation nor after its deletion). Over that same period, three charts: documents, size on disk, size of the primaries (`GET /clusters/{id}/indices/{uuid}/history`).
5. **A/B diff**: **(v1.1)** default period: the last hour, for this screen as for flows, losses and the journal; presets 1 h, 6 h, 24 h, 7 days. Numeric summary (nodes ±, indices ±, shards moved/lost/new/promoted, settings changed); tabs for nodes / indices / shards / settings / cluster settings / CCS. Copies are compared node by node first: a replica that becomes primary on its node is **promoted**, and the copy that disappeared counts as a lost replica (not as a primary moved). A node seen by a probe but not yet described by a full capture is not "changed". When only light probes precede A, the diff starts from the first full capture; **path taken** by default (movement events in the window); CSV, JSON, XLSX exports.
6. **Relocation flows** over a period: Sankey DC/tier/machine/node → DC/tier/machine/node, histogram of relocations over time, click on a flow → list of shards; trajectory of a shard (Gantt-style timeline).
7. **Losses and disappearances** (§7.6): groups by lost node/machine, before/after, re-replication, "deliberate deletions" filter. The API caps each group (1,000 copies by default); the summary counts everything and the export contains everything.
8. **Event journal**: filterable by type, severity, node, machine, family, period; uncertainty window or exact date displayed.
9. **Global search** (omnibox, autocompletion): node, IP, machine, index name or UUID, family, "shard N de X à T" (also `shard N of X at T`, `X[N]`, `X[N] @ T`) → history page of the entity.
10. **Allocation constraints** (§7.5): list sorted infeasible → tight → OK, explanation, filters.
11. **Administration** (§9).
12. **(v1.1) Group** (zone, tier or machine), address `/c/<cluster>/g/<label>/<label>…`: summary cards (nodes present and missing, machines, copies and primaries, disk, average and maximum CPU and heap, moves, versions), topology of the group alone as large named tiles (plain tiles above 48 nodes), sortable table of the nodes, content per family, events of the nodes and machines of the group over 7 days (shard moves excluded), 7-day charts of the metrics of its nodes (mean process CPU and heap, disk used, shards added up, nodes measured; a node present without a new reading in a slice counts with its last known value, because of the deadband; for a machine, also machine CPU and load; `GET /clusters/{id}/group/history`). API: `GET /clusters/{id}/group?g=…&at=` and the `g=` filter on `/events`.
   - Group and node pages carry a **"Zoom out"** button to the level above and a clickable breadcrumb (cluster / zone / tier / machine / node).

## 9. Administration

- **Clusters**: default CA; add/edit (name, URL(s), auth type, cert/key/passphrase/API key/password paths, optional dedicated CA, call timeout; **(v1.2)** the connection timeout, 10 s, cannot be set), enabling, **Test the connection** (version, uuid, privileges), **schedule**, hierarchy, family regexes with preview, declared node reference (optional), acknowledgements (with effective date), accept a new identity or handle as a new cluster, purge.
- **Schedule** (per cluster, in the form): probe period (5 min), `min_interval` (10 min), `max_interval` (60 min), trigger threshold, jitter; validated server-side. Parallelism is global (Collectors tab).
- **Retention and storage**: duration, max size, partitions (size, period), bytes/day per cluster, 30-day / 1-year projection.
- **Collectors**: parallelism; per cluster, last probe and capture (status, total duration, **duration and size per API call**), recent errors, next run. A banner reports a cluster whose history is ahead of the clock (§6.7). **(v1.2)** "Capture now" is in the form of the cluster (Clusters tab).
- **Settings**: index settings whitelist.
- **Authentication**: mode (none / local / LDAP), users, LDAP configuration + step-by-step test.
- **HA**: role, followed primary or watched standby, configured token, replication lag per cluster, promote, rebuild, demote, resume.
- **System**: version, uptime, memory, number of states kept in cache, enabling `/metrics`, audit of admin actions. **(v1.2)** The data size is in the Retention and storage tab.
- **(v1.0)** Configuration changes are recorded in the audit log. On a standby, the admin is read-only (§11).

## 10. Authentication and web security

- Modes: **none** (everyone is admin, warning banner), **local**, **LDAP** (OpenLDAP over LDAPS with the company CA: service account, user search, bind with the user's password, role based on membership of an admin / reader group).
- Roles: **reader** (view everything, export); **admin** (additionally: configuration, acknowledgements, forced capture, purge, HA).
- Local accounts: argon2id passwords (m = 64 MiB, t = 3, p = 2), 12 characters minimum. **(v1.2)** At sign-in, the name of a local account is compared ignoring case. Users change their own password from the header (click on their name). As long as authentication is enabled, the last active local admin account can be neither deleted, nor disabled, nor demoted.
- **(v1.0) LDAP in detail**:
  - `ldaps://` URL, or `ldap://` **with mandatory StartTLS** (plaintext simple bind refused); several URLs tried in order; CA as a PEM file (otherwise system CAs);
  - service account password in a file, re-read at each login;
  - default user filter `(&(objectClass=inetOrgPerson)(uid={user}))`, name escaped, exactly one entry expected; **(v1.3)** the account is named by the `user_attr` attribute of the entry found (`uid` by default, exactly one value required), not by the typed name: with a filter on several attributes, one person has a single account. That name replaces `{user}` in the group filter;
  - `groupOfNames`, `groupOfUniqueNames` and `posixGroup` groups by default (adjustable filter); DNs compared the way LDAP does;
  - an empty password is refused before any call: it would be an unauthenticated bind, which OpenLDAP accepts;
  - the role is re-read at each login. An account disabled by an admin stays disabled. Names are lowercased. **(v1.3)** A login without a local account costs an argon2id computation, like a local account: the response time does not tell which local accounts exist;
  - directory unreachable → "directory unavailable" (503), not counted as a failed attempt;
  - **local accounts remain usable in LDAP mode**: they are the fallback, together with `reset-password`. Switching to local or LDAP mode therefore requires a local admin;
  - a step-by-step test in the admin checks the connection, the service account, the search, the bind, the groups and the role.
- Cookie-based sessions, `HttpOnly`, `Secure` over HTTPS, `SameSite=Strict` + CSRF token (`X-CSRF-Token` header) on state-changing requests. Only the SHA-256 hash of the token is stored. Sliding expiry after 12 h of inactivity by default. **(v1.2)** This delay can be set from 1 to 720 h; the expiry is only pushed back when less than half of the delay is left; the absolute lifetime of a session is 7 days. The other sessions of an account are closed on a password change, a role change, when the account is disabled and by `estopo admin reset-password`. **(v1.3)** A change of the authentication mode closes every session, except the one of the local administrator making it; the last admin checks and the writes they guard are serialized. Every state-changing request must carry `Content-Type: application/json` (with a body) or `X-Requested-With: estopo` (without a body) and come from the same origin, in every mode; otherwise it is refused (403 `cross_site`): a reverse proxy must preserve the `Host` header. If the authentication settings or the sessions cannot be read, the request is refused (503 `auth_unavailable`), never served as in mode "none". Language and theme remembered in the profile.
- **(v1.0)** Login throttling: 5 free failures, then a 2 s wait doubled at each failure, capped at 15 min; a success resets the counter. **(v1.2)** The counters apply to the user + address pair and to the address (5 failures per 15 min), and to the user whatever the address (50 failures per 15 min). **(v1.3)** A success only resets the counters of the account; the one of the address only gets the attempt back. Names compare as in the lookup of local accounts (Unicode case folding).
- HTTPS if `ESTOPO_TLS_CERT`/`ESTOPO_TLS_KEY` are provided (full-chain file accepted). **Local and LDAP modes are refused over plain HTTP** (both at startup and when changing mode), unless listening on loopback or `ESTOPO_ALLOW_INSECURE_AUTH=true` (warning banner, including on the login screen). **(v1.2)** When estopo terminates TLS, it sends `Strict-Transport-Security`. HTTP server timeouts: read 30 s, write 10 min, idle 120 s.
- **(v1.2)** First start: if `ESTOPO_ADMIN_PASSWORD` is set while no authentication mode was ever saved and no account exists, the local account `admin` is created with this password and local mode is enabled. At least 12 characters and a safe transport (HTTPS, loopback or `ESTOPO_ALLOW_INSECURE_AUTH`) are required, otherwise the start is refused. The variable is ignored afterwards, and on an instance started with `ESTOPO_STANDBY_OF`. No password is generated in the logs any more. Recovery: `estopo admin reset-password <user>`.

## 11. High availability

- A **primary** instance collects; a passive **standby** **never** contacts the ES clusters.
- The standby pulls from the primary (HTTPS + shared token): `meta.db` (a snapshot whenever it changes), then the **capture records** from the spool, which it ingests with the same code (§6.7). Maximum loss on failover: a few minutes.
- **Rebuilding** a standby: copy of the sealed partitions + snapshot of the current partition, then catch-up through the records. **(v1.2)** Everything is downloaded and verified before the local partitions are replaced: up to 2× the disk space of the cluster is needed during the operation.
- **Manual promotion** (admin button on the standby). No automatic failover (avoids split-brain). The old primary, if it comes back, must be demoted by hand; if it sees a promoted standby on the configured peer, it suspends its collection and says so.
- Since the configuration lives in `meta.db`, it follows; **certificate files must exist at the same paths** on the standby.
- **(v1.0) Implementation**:
  - **Replication API** of the primary (`GET /api/v1/replication/...`, `Bearer` token of at least 16 characters, compared in constant time; with no token configured, the API returns 404). It exposes the state, the configuration (deterministic JSON + ETag), the partition catalogue, the spool range and the last record ingested per cluster, a consistent snapshot of a partition (`VACUUM INTO`), the list of records after a given date, and each record.
  - **Token**: the token never travels in clear text. On the primary side, the replication API requires HTTPS, a loopback listener or `ESTOPO_ALLOW_INSECURE_AUTH`. On the standby side, the primary's URL must be `https://`, except towards loopback or with the same exemption. **(v1.2)** `ESTOPO_REPLICATION_CA` (otherwise system CAs) is used for every replication call: from the standby to the primary, and from the primary to the standby it watches. A file that cannot be read or holds no certificate prevents the start.
  - **What is replicated**: every 30 s, the standby imports, in a single transaction, the configuration tables (`cluster`, `setting`, `family_rule`, `user`, `node_ack`, `audit`). LDAP accounts created locally are kept, unless the replicated audit shows their deletion on the primary after their creation. **(v1.3)** The sessions of the standby end as on the primary: those of an account whose password, role or source changes, or that is disabled or deleted, and all of them on a change of the authentication mode. Caches are invalidated only when the "topological" configuration changes, not on every login recorded in the audit log. Both instances must have the same `meta.db` schema (same estopo version).
  - **Per-cluster rebuild**, automatic when the standby has no history, or when it lags further behind than what the primary's spool still holds (72 h), including when that spool holds nothing more of a cluster whose history goes further. It can be triggered manually ("Rebuild") for all clusters. A rebuild asked for (button, demotion) is recorded in `ha_state`: neither a restart before it is done nor the end of a rebuild already in progress cancels it. A cluster purged on the primary is also purged on the standby: the purge deletes the partitions and the spooled records of the cluster, which therefore neither comes back at the next start nor stays announced to the standby.
  - **(v1.2) Instance identifier and block**: every data directory carries a random instance identifier. The standby remembers that of its primary at the first synchronisation. If the primary answers with another identifier, or announces no cluster while the standby holds history, replication is **blocked**: nothing is imported or deleted (field `replication.blocked`, shown in Admin › High availability; metric `estopo_replication_blocked`), until an explicit rebuild or a promotion; a primary that does not answer leaves the block in place. Promotion does not wait for a primary that no longer answers.
  - A standby (or an old primary) is **read-only**: any change is refused (409), except HA actions.
  - **Fencing**: the primary watches its standby (URL entered in the admin; after a promotion, the new primary watches the old one). When the peer has been promoted **more recently**, the instance stops collecting (`fenced`, red banner). The most recent promotion wins, which rules out mutual fencing. The check also runs at startup, **before** collection is restarted.
  - **Actions**: on a standby, promote and rebuild; on an old primary, demote (it becomes a standby again and copies the entire history from the new primary, its own records being lost) or resume (refused as long as the peer claims to be a more recent primary).
  - `ESTOPO_STANDBY_OF` only matters at first start: afterwards, the local state (`ha_state`) takes precedence.

> Design choice: continuously copying the current week's partition (which changes every 5 min) would cost several GB of IO per hour; shipping the records is lighter for the same result.

## 12. Operations

### 12.1 Environment variables (only what is needed before reading the database)

| Variable | Default | Purpose |
|---|---|---|
| `ESTOPO_LISTEN` | `127.0.0.1:8080` (v1.2) | listen address and port; exposing the service requires setting it |
| `ESTOPO_DATA_DIR` | `./data` (next to the binary) | data directory |
| `ESTOPO_TLS_CERT` / `ESTOPO_TLS_KEY` | — | HTTPS for the UI (certificate with full chain accepted) |
| `ESTOPO_ADMIN_PASSWORD` | — | (v1.2) at the very first start (no authentication setting, no account): creates the local account `admin` and enables local mode (§10); ignored afterwards |
| `ESTOPO_ALLOW_INSECURE_AUTH` | `false` | allows authentication, and the replication token, over plain HTTP |
| `ESTOPO_LOG_LEVEL` / `ESTOPO_LOG_FORMAT` | `info` / `text` | logs (`json` possible) |
| `ESTOPO_REPLICATION_TOKEN_FILE` | — | file holding the shared replication token (§11): enables a primary's replication API, mandatory on a standby |
| `ESTOPO_STANDBY_OF` | — | primary URL: first start as a standby (requires the token) |
| `ESTOPO_REPLICATION_CA` | system CAs | (v1.2) CA of the certificate of the other instance, on the standby as well as on the primary; unreadable or empty: start refused |
| `ESTOPO_PPROF_LISTEN` | — | (v1.0) **loopback** address of the Go profiler (diagnostics), disabled by default |
| `ESTOPO_SECRETS_DIR` | — | (v1.2) when set, every file path typed in the admin (clusters, default CA, LDAP) must be inside this directory |

Everything else is configured in the admin.

### 12.2 Example systemd service (provided in `deploy/`, not installed automatically)

```ini
# /etc/systemd/system/estopo.service
[Unit]
Description=ES.Topo - Elasticsearch topology history
After=network-online.target
Wants=network-online.target

[Service]
User=estopo
Group=estopo
EnvironmentFile=/etc/estopo/estopo.env
ExecStart=/opt/estopo/estopo serve
Restart=on-failure
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/estopo

[Install]
WantedBy=multi-user.target
```

```sh
# /etc/estopo/estopo.env
ESTOPO_LISTEN=0.0.0.0:8443
ESTOPO_DATA_DIR=/var/lib/estopo
ESTOPO_TLS_CERT=/etc/estopo/tls/estopo-fullchain.pem
ESTOPO_TLS_KEY=/etc/estopo/tls/estopo.key
ESTOPO_LOG_FORMAT=json
ESTOPO_SECRETS_DIR=/etc/estopo/secrets
```

**(v1.2)** The files in `deploy/` are the reference; the header of the unit gives the installation commands (system user, directories `/opt/estopo`, `/etc/estopo` and `/var/lib/estopo`).

### 12.3 Monitoring

`/healthz` (alive + collection status + HA role); `/metrics` in Prometheus format (optional, disabled by default); logs on stdout (journald).

**(v1.0)** `/metrics` is enabled in Admin › System. It is served without authentication, like most Prometheus targets, and exposes no topology data: dates, durations and success of probes and captures, suspension per cluster, ingestion (records, errors, durations, queue), history size per cluster, HA role, replication lag, Go memory. **(v1.2)** Added: `estopo_ingest_skipped_total` (records given up after repeated ingestion failures) and `estopo_replication_blocked`.

## 13. Simulator and tests

### 13.1 `essim` simulator

- Go HTTP(S) server mimicking the APIs of §5 for **several simulated clusters**, with 7.17 / 8.x / 9.x format variants.
- Configurable topology; reference dataset: cluster A ~200 nodes, 3 DCs, hot/warm/cold, 4 nodes per hot machine and 8 per warm/cold machine, DC + machine awareness attributes, daily indices over 90 days for N sources (~250,000 shards); cluster B ~100,000 shards; cluster C, a mostly idle cold cluster.
- **Accelerated clock** and scenarios: daily rollover and deletion by retention, rebalancing, loss of a node / machine / rack / DC followed by its return, node replacement (new generation), rolling upgrade (mixed versions), index and cluster settings changes, blocking by `total_shards_per_node`, CCS disconnection, slowness / timeouts / 503 / no master, `cluster_uuid` change.
- **mTLS** with a generated test PKI (root + intermediate, as in an enterprise setup).
- **Backfill** mode: collector and simulator share an accelerated clock to produce a history much faster than real time (performance tests, demonstration data). **(v1.2)** Order of magnitude: about half an hour for the 14-day demonstration dataset (`essim backfill`), a few hours for the 30-day reference dataset.

### 13.2 Tests

- Unit: parsing (7.17/8/9 fixtures), interval ingestion, events, severity, families, constraints, losses, retention.
- End-to-end scenarios against `essim` (behaviours of §15).
- Format validation against **real ES 7.17, 8.x, 9.x** in Docker containers (`scripts/capture-fixtures.sh`: secured 4-node cluster, root → intermediate PKI, PKI + native + file realms, trial license; the Go client is tested with all three authentication methods).
- **(v1.0)** LDAP: fake LDAPS server in Go for unit tests; real OpenLDAP (Debian package) over LDAPS and StartTLS via `testenv/ldap/run.sh`.
- End-to-end UI tests (Playwright, development only).
- Binary startup test in Rocky/Alma 8, 9, 10 containers (`testenv/distros/run.sh`: HTTPS, unprivileged user, no network).

## 14. Performance targets (on the reference dataset, 30 days backfilled, development PC)

| Measurement | Target | **(v1.0) Measured** (cluster A, 245,000 copies, p95) |
|---|---|---|
| Ingestion of a full capture of 250 k shards (excluding ES response time) | < 30 s | 0.82 s on average, 5.3 s at most |
| Resident memory, 15 clusters being collected | ~~< 1.5 GB~~ indicative (v1.0) | peak 1.41 GiB, 0.93 GiB in steady state |
| Cluster summary at T (warm / cold cache) | < 300 ms / < 2 s (p95) | 54 ms / 1.0 to 1.6 s |
| Node detail | < 300 ms | 5 ms |
| Filtered shard page | < 500 ms | 10 ms |
| A/B diff over 7 days (summary) | < 3 s | 2.3 to 2.8 s |
| Losses over 24 h | < 2 s | 1.35 s |
| Search autocompletion | < 150 ms | 2 ms |
| Disk size for 30 days | measured and ≤ 2× the §3 estimate, otherwise the model is revised | 1.015 GB, about 1× |

- **(v1.0)** "Warm cache" = a date already viewed. Moving to a date never viewed before costs the reconstruction of the state (about 0.6 s for A).
- **(v1.0)** The 1.5 GB target is indicative: the size of the state cache can be set in the administration.
- Reconstructed states are cached: an LRU of **64 states** by default (configurable, about 20 MB per state for a 250,000-shard cluster). The home summary is cached too, and recomputed at each new capture (home page with 15 clusters: 3 ms when warm). The current state of each cluster stays in memory.
- **(v1.2)** Order of magnitude to remember, measured on simulated data: about 1 GB for 30 days of history of 373,000 shard copies.

## 15. Expected behaviours (verifiable cases)

| # | Input | Expected result |
|---|---|---|
| B1 | Two identical full captures | No new interval; only `capture`, `health` and metrics outside the deadband are written. |
| B2 | A node disappears between two probes (5 min) | `node_left` event with a window ≤ 5 min; full capture triggered (subject to `min_interval`); node marked as missing (severity 2). |
| B3 | A machine (8 nodes) disappears | One `machine_lost` event grouping the 8 nodes; the Losses screen groups the lost copies by machine, then by node. |
| B4 | Primary copy lost, replica present elsewhere | Classified as *lost*; the replica is classified as *promoted*; the "after" state shows the new reallocated copy with its date. |
| B5 | Index deliberately deleted | Copies classified as *deleted*, absent from the Losses screen by default. |
| B6 | Shard moved N1 → N2 → N1 between A and B | The A/B diff shows nothing; the **path** shows both moves with their windows. |
| B7 | `_cat/shards` times out | Capture `partial`/`failed`, no interval closed, "unknown" band in the UI, `capture_failed` event. |
| B8 | `cluster_uuid` change behind a URL | Collection suspended, admin alert, nothing ingested. |
| B9 | Same node name, new ES id | `node_new_generation` event, generation +1, continuous history under the same name. |
| B10 | Node removed for good, then acknowledged | No longer counted as missing from the acknowledgement date; action recorded in the audit log. |
| B11 | Index recreated under the same name | Two distinct entities (UUID); searching by name shows both lives. |
| B12 | `total_shards_per_node` too low / eligible nodes | Index classified as *infeasible* with an explanation; *tight* if losing a machine would make it infeasible. |
| B13 | 30-day retention exceeded | Partitions entirely outside retention are deleted; the others remain readable on their own (keyframe). |
| B14 | Max size exceeded | Oldest partitions deleted until under the threshold; event and admin banner. |
| B15 | Continuous churn (bursts of relocations) | No more than one full capture per `min_interval`. |
| B16 | Attempted non-GET call in the ES client | Refused by the client (unit test). |
| B17 | Local mode over plain HTTP, non-loopback listener | Startup refused with an explicit message, unless `ESTOPO_ALLOW_INSECURE_AUTH=true`. |
| B18 | Standby, primary stopped, promotion | The standby starts collecting; the history contains all captures up to the last record received. |
| B19 | Rolling upgrade 8.x → 9.x | `node_upgraded` events; the home page and the cluster view show the mixed versions. |
| B20 | Cluster setting change (e.g. `allocation.enable=primaries`) | `cluster_settings_changed` event, key-by-key diff displayed. |
| B21 | CCS remote disconnected | `remote_disconnected` event; visible in the cluster view and the diff. |
| B22 | XLSX export of a diff | File opens, one sheet per category, columns and labels in the UI language. |

## 16. Acceptance criteria

1. `go test ./...` passes (unit tests + scenarios against `essim`).
2. `CGO_ENABLED=0 go build` produces `estopo` and `essim` for linux/amd64, windows/amd64 and, **(v1.2)**, darwin/arm64; `estopo` starts and responds on `/healthz` in Rocky/Alma 8, 9 and 10 containers.
3. Format tests pass against real ES 7.17, 8.x and 9.x.
4. 30-day backfill of the reference dataset: §14 targets measured and reported (table of §14).
5. B1–B22 covered by automated tests (UI via Playwright for the relevant screens).
6. UI usable without any external network access (verified: no cross-origin requests in Playwright).
7. README EN/FR, SPEC EN/FR, systemd example and `build-release.sh` up to date.

## 17. Implementation steps

**(v1.0)** The implementation reordered the steps: real ES formats from the start, client before simulator, backfill and bench before the UI. Originally proposed order:

1. **Skeleton**: local git repository, modules, layout, configuration through env, logs, `build-release.sh`, AGPL-3 license.
2. **Simulator** `essim`: topology, 7.17/8/9 formats, accelerated clock, scenarios, test PKI.
3. **ES client and collection**: TLS/mTLS/API key/basic, read-only, JSON streaming, light probe, full capture, triggers, records and spool.
4. **Storage and ingestion**: `meta.db`, partitions, intervals, keyframes, deadband, blobs, events, retention/max size.
5. **Queries and API**: state at T (cache), diff, path, losses, journal, search, aggregations, CSV/JSON/XLSX exports.
6. **Backfill and bench**: 30-day reference dataset, §14 measurements, adjustments.
7. **UI (1)**: foundation (i18n, theme, navigation, permalinks), home, cluster view + time band, node, family/index.
8. **UI (2)**: diff and path, losses, journal, search, flows (Sankey, trajectories), allocation constraints.
9. **Admin and local authentication**, HTTPS, sessions, audit.
10. **LDAP**.
11. **HA**: record-based replication, rebuild, promotion.
12. **Finishing touches**: validation against real ES, RHEL-like startup tests, EN/FR documentation, release.

Each step ends with an executable verification (tests, build, bench) before moving on to the next.

## 18. Open questions and points to verify

- **Actual cost** of `_cat/shards` and `_all/_settings/<list>` on a real 250 k-shard cluster: to be measured via the Collectors page. Plan B if too expensive: only read the settings of new indices or of those whose `settings_version` has changed (filtered `_cluster/state/metadata`).
- `fs.io_stats`: present but empty in the test containers, so not verified; to be confirmed on a real cluster (Collectors page). `os.cgroup` is present (quota `-1` when unlimited).
- **(v1.0, verified)** `http.compression`: no gzip response over TLS with the default settings, on all three versions. The client requests gzip and can decode it, without depending on it.
- **(v1.0, verified)** PKI realm: works with the trial license in the test containers. The license required in production is **not verified**.
- **(v1.0, verified)** Minimum privileges (§5.6) confirmed on 7.17, 8.19 and 9.5.
- **(v1.0)** Manual spool replay after fixing an ingestion bug (§6.7): postponed, to be discussed again.
- **(v1.0)** HA: the copy time of a large partition and operation between two machines over real HTTPS remain to be measured (tested in-process, over loopback HTTP and in TLS unit tests).
- Frozen tier / searchable snapshots: sizes not significant, to be handled when the time comes.
- Demonstration data: `essim backfill` and the `demo` scenario (§13).
- Idea for later, out of scope: a "what happens if I lose machine / rack X?" simulation based on the constraints of §7.5.
