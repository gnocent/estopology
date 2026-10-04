*(Version française : [CHANGELOG_fr.md](CHANGELOG_fr.md))*

# Changelog

This file describes what each published version of ES.Topo contains. The binaries of each version are on the [releases page](../../releases).

## 0.8.0-beta

First public release. This is a beta version: the product has been tested against real test clusters running Elasticsearch 7.17.29, 8.19.22 and 9.5.4 and on simulated data, not yet in production at scale. The data format and the API may still change before version 1.0.

### Collection

- Read-only collection of several Elasticsearch 7.17, 8.x and 9.x clusters; the client refuses any request other than `GET` or `HEAD`.
- Periodic light probe (health, nodes, metrics, allocation, cluster settings, remote clusters) and full capture (nodes, indices, index settings, shard copies).
- Adaptive triggering of the full capture on a change of health, nodes, moving shards or cluster settings, with a minimum delay, a maximum delay and jitter, set per cluster.
- Authentication to Elasticsearch by client certificate, API key or password; secrets read from files; default CA or CA per cluster; several URLs per cluster.
- An incomplete or failed capture is recorded as such: nothing is concluded from a partial answer.
- The collection is suspended when the identity of the cluster (`cluster_uuid`) changes behind a URL.

### Storage

- History in SQLite files, one per cluster and per week, with no external dependency.
- Validity-interval model: only changes are written; the state at any date is found with one query.
- Retention by duration and by maximum size of the data directory.
- Raw readings kept 72 hours and replayed after a crash.

### Exploration screens

- Multi-cluster home sorted by severity, with a 7-day health strip.
- Time band on every dated view; every view is a permalink.
- Physical view by zone, tier and machine; one page per zone, tier, machine and node, with zoom out.
- Logical view as a matrix, by index family or by index, columns per node or grouped, with a filter.
- Family and index pages: charts, shard matrix, settings history, shard path.
- Diff between two dates and path taken; lost copies grouped by node or machine; event journal; relocation flows; allocation constraints.
- Global search; CSV, JSON and XLSX exports.
- Interface in French and English, light or dark theme, help in every screen. Everything is embedded: no external resource.

### Administration

- Declaration of the clusters with a connection test and a privilege check.
- Schedule, navigation hierarchy, machine attribute, family rules with preview, node reference and acknowledgements, per cluster.
- Index settings whitelist; state of the collectors with the duration and size of each call; storage and projections; audit trail.

### Authentication

- Three modes: none, local accounts, LDAP directory (OpenLDAP, over LDAPS or StartTLS); reader and admin roles.
- Creation of the first administrator with `ESTOPO_ADMIN_PASSWORD` at the first start.
- argon2id passwords, expiring sessions, sign-in throttling, change of one's own password, reset from the command line.
- In LDAP mode, local accounts remain usable as a fallback.

### High availability

- Passive standby instance that copies the configuration and the readings of the primary; manual promotion.
- Fencing of an old primary that sees a more recent promotion; demotion, rebuild, resume.
- Replication blocked when the primary was replaced or emptied, until an administrator decides.

### Operations

- A single static binary for Linux x86-64; Windows x64 and macOS arm64 binaries for trials and development.
- Start-up settings through environment variables; example systemd unit.
- `/healthz`; `/metrics` in the Prometheus format, to be enabled; logs on standard output, as text or JSON.

### Simulator

- `essim` simulates Elasticsearch clusters in the 7.17, 8.x and 9.x formats, with an accelerated clock, failures and incident scenarios.
- `essim backfill` builds a demonstration data directory; `essim action` triggers incidents on a simulated cluster.

### Hardening before release

- Default listener on `127.0.0.1`.
- Requests are refused when authentication cannot be evaluated, instead of falling back to no authentication.
- State-changing requests coming from another site are refused, in every mode.
- `ESTOPO_SECRETS_DIR` to confine the files named in the administration.
- Maximum session lifetime; sessions closed on a password or role change; protection of the last local administrator.
- A password or an API key is never sent over plain HTTP; Elasticsearch responses are bounded.
- Timeouts on the HTTP server, HSTS header, stricter content security policy, formula neutralisation in CSV exports.

### Known limits

- **Beta version.** No production experience at scale; the real cost of the collection on a very large cluster has not been measured.
- **No automatic failover** with high availability; without a watched instance filled in on the primary, an old primary that comes back resumes collecting.
- **Without authentication, every visitor is an administrator**; this is the default mode, on a local listener. See [SECURITY.md](SECURITY.md).
- **LDAP**: only OpenLDAP is tested; with the default filters, only direct group membership counts.
- **Reverse proxy**: no path prefix; the `X-Forwarded-For` header is not read, so the per-address sign-in throttling is shared.
- **Secrets**: the content of a cluster secret file is only read again at restart. TLS verification of the clusters cannot be disabled.
- **Backup**: no hot backup command; no going back to an earlier version after an upgrade.
- **Index families** are derived from the index name alone; data streams, the frozen tier, searchable snapshots and CCR are not handled specifically.
- **Allocation constraints**: this is an estimate, which does not replace `_cluster/allocation/explain`.
- **Removed cluster**: its history can no longer be purged from the interface; purge before removing.
- **Connection test** on a cluster without security: behaviour not verified.
- **`/healthz`**: the "no recent probe" threshold is fixed at 15 minutes.
- **Unsigned binaries**; the Windows and macOS binaries are meant for trials.
- **HTTP API** undocumented and subject to change.
