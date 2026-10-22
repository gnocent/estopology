*(Version française : [operations_fr.md](operations_fr.md))*

# Operating ES.Topo

This guide is for the person who runs the service: where the data is, how much it weighs, how to keep it and back it up, how to monitor the instance, and how to use the simulator. It assumes the installation described in [Installation](installation.md): `estopo` service, data in `/var/lib/estopo`, configuration in `/etc/estopo`.

## Layout of the data directory

Everything ES.Topo writes is in the data directory (`ESTOPO_DATA_DIR`):

```
<data directory>/
  meta.db                              configuration, accounts, sessions, acknowledgements, audit trail, catalogue
  clusters/<id>/<YYYY>-W<ww>.db        the history: one file per cluster and per ISO week (UTC)
  spool/<id>/…rec.zst                  compressed raw readings, kept 72 hours
  tmp/                                 copies in transit (high availability); emptied at start-up
```

- The `.db` files are SQLite databases. `-wal` and `-shm` files go with them while they are open: they are part of them.
- The file of the **current week** is modified at each reading. When a new week starts, the file of the previous week is **sealed**: it no longer changes. The page **Administration › Retention and storage** marks sealed files with a padlock.
- The `spool` is used to replay, at restart, a reading that was received but not yet ingested, and to feed a standby instance.
- **Not** in this directory: the environment file, the certificate of the service and the secret files (under `/etc/estopo` in the installation described).

## Sizing

The only figures available come from simulated data; take them as orders of magnitude.

- **Disk**: about 1 GB for 30 days of history of 373,000 shard copies (three simulated clusters). The size depends mostly on the number of shard copies and on how much the cluster moves, not on the volume of indexed data.
- **Memory**: about 1.4 GiB at peak with 15 simulated clusters being collected, one of them with 245,000 shard copies.
- The page **Administration › Retention and storage** gives, for each cluster, the size used, the "Bytes/day" and a "30-day projection" and "1-year projection": this is the measure to follow on your own clusters.
- The number of reconstructed states kept in memory is set in **Administration › System**, "States kept in memory": 64 by default, from 2 to 4096, about 20 MB per state of a 250,000-shard cluster. The change is applied at restart.

## Retention and maximum size

In **Administration › Retention and storage**:

| Field | Default | Effect |
|---|---|---|
| "Retention (days)" | 30 | How long the history is kept, from 7 to 3650 days. |
| "Maximum size (GB, 0 = none)" | 0 | Size cap of the whole data directory. |

How it works:

- Retention runs at start-up, then once an hour.
- It deletes **whole weeks**: a weekly file is only deleted when its entire week is outside the retention period. The history that can be viewed is therefore a little longer than the configured duration.
- When the maximum size is exceeded, the oldest weekly files, all clusters together, are deleted until the directory is back under the cap. The size counted is that of the whole directory (`meta.db`, `spool` and `tmp` included), but only history files are deleted.
- The latest file of each cluster is never deleted (the current week, or the previous one until the first reading of the new week). If the cap is still exceeded in spite of everything, an alert is shown on the same page.
- The history of a cluster removed from the configuration is kept until its retention.
- The audit trail is kept one year; the raw readings of the `spool`, 72 hours.

## Backup and restore

**What to back up**: the data directory, and `/etc/estopo` (environment file, certificate, secrets), which is not part of it.

**Safe method: service stopped.**

```bash
systemctl stop estopo
tar -C /var/lib -czf /backup/estopo-data-$(date +%F).tar.gz estopo
systemctl start estopo
```

Stopping interrupts the collection for a moment; at restart, a probe and a full capture are taken again.

**Without stopping the service**, only the sealed files (finished weeks) can be copied as they are. `meta.db` and the files of the current week are being written: a plain copy may be inconsistent. ES.Topo does not provide a hot backup command.

**Restoring**:

```bash
systemctl stop estopo
rm -r /var/lib/estopo
tar -C /var/lib -xzf /backup/estopo-data-2026-10-01.tar.gz
systemctl start estopo
```

Check that the files still belong to the `estopo` user. The period between the backup and the restore is missing from the history.

A standby instance is not a backup: it also reproduces deletions. The effect of restoring a primary on its standby has not been verified; after a restore, click "Rebuild" on the standby (see [High availability](high-availability.md#rebuild)).

## /healthz

`GET /healthz` always answers without authentication, with code 200 as soon as the service is up:

```json
{"status":"ok","version":"0.8.1-beta","role":"primary","uptime":"3h12m5s","clusters":4,"suspended":1,"stale":1}
```

| Field | Meaning |
|---|---|
| `status` | always `ok` when the service answers |
| `version` | version of the binary |
| `role` | `primary`, `standby` or `fenced` (fenced old primary) |
| `uptime` | time since start-up |
| `clusters` | number of clusters being collected (absent when 0, hence on a standby) |
| `suspended` | clusters whose collection is suspended after an identity change (absent when 0) |
| `stale` | clusters without a probe for more than 15 minutes (absent when 0). This threshold is fixed: with a probe period set beyond 15 minutes, the cluster is always counted here. |

The HTTP code does not reflect the state of the collection: to alert on a cluster that no longer answers, read the fields or use `/metrics`.

## /metrics

`/metrics` exposes metrics in the Prometheus text format. It is **disabled by default** (404 answer).

To enable it: **Administration › System**, "Prometheus metrics", tick "Expose /metrics". The effect is immediate. The setting is replicated to a standby.

`/metrics` is served **without authentication**. It holds no topology data (no node name, no index name), but it holds the ids of the configured clusters. If this bothers you, filter access to this path in your firewall or your reverse proxy.

| Metric | Labels | Meaning |
|---|---|---|
| `estopo_build_info` | `version`, `commit` | equals 1; carries the version |
| `estopo_uptime_seconds` | | time since start-up |
| `estopo_ha_role` | `role` | equals 1 for the current role (`primary`, `standby`, `fenced`) |
| `estopo_cluster_suspended` | `cluster` | 1 when the collection of the cluster is suspended (identity changed) |
| `estopo_history_ahead_seconds` | `cluster` | how far the last reading ingested is ahead of the clock of the instance, 0 within an hour: the clock went back, and new readings are refused until it catches up |
| `estopo_probe_last_timestamp_seconds` | `cluster` | start of the last light probe |
| `estopo_probe_last_duration_seconds` | `cluster` | duration of the last light probe |
| `estopo_probe_last_ok` | `cluster` | 1 if the last probe fully succeeded |
| `estopo_capture_last_timestamp_seconds` | `cluster` | start of the last full capture |
| `estopo_capture_last_duration_seconds` | `cluster` | duration of the last full capture |
| `estopo_capture_last_ok` | `cluster` | 1 if the last full capture fully succeeded |
| `estopo_storage_bytes` | `cluster` | size of the history of the cluster |
| `estopo_replication_lag_seconds` | | standby: lag behind the primary |
| `estopo_replication_last_sync_timestamp_seconds` | | standby: last complete synchronisation |
| `estopo_replication_blocked` | | standby: 1 when it refuses to replicate (primary replaced or emptied) |
| `estopo_ingest_records_total` | `cluster`, `kind` | readings ingested since start-up (`kind`: `light` or `full`) |
| `estopo_ingest_errors_total` | `cluster`, `kind` | failed ingestion attempts |
| `estopo_ingest_skipped_total` | `cluster`, `kind` | readings given up after three failed ingestions: each one is a gap in the history |
| `estopo_ingest_seconds_total` | `cluster`, `kind` | time spent ingesting |
| `estopo_ingest_max_seconds` | `cluster`, `kind` | longest ingestion of a reading |
| `estopo_ingest_queue` | | readings waiting to be ingested |
| `estopo_go_heap_bytes` | | Go memory in use |
| `estopo_go_sys_bytes` | | memory obtained from the system by Go |
| `estopo_go_goroutines` | | number of goroutines |

The per-cluster metrics only exist on the instance that collects; the replication metrics, only on a standby.

Example Prometheus configuration:

```yaml
scrape_configs:
  - job_name: estopo
    scheme: https
    metrics_path: /metrics
    tls_config:
      ca_file: /etc/prometheus/ca.pem
    static_configs:
      - targets: ['estopo.example.org:8443']
```

Suggested alerts:

| Condition | Meaning |
|---|---|
| `estopo_probe_last_ok == 0` for more than 15 minutes | a cluster no longer answers properly |
| `time() - estopo_probe_last_timestamp_seconds > 900` | the collection of a cluster has stopped |
| `estopo_cluster_suspended == 1` | a cluster changed identity: a decision is needed (see [Declaring a cluster](clusters.md#cluster-suspended-after-an-identity-change)) |
| `increase(estopo_ingest_skipped_total[1h]) > 0` | readings were lost |
| `estopo_history_ahead_seconds > 0` | the clock of the instance went back: new readings are refused until it catches up; check the clock of the server (NTP) |
| `estopo_replication_blocked == 1` | the standby stopped replicating: see [High availability](high-availability.md#replication-blocked) |
| `estopo_replication_lag_seconds > 900` | the standby is falling behind |

## Logs

- ES.Topo writes its logs to **standard output**, and nowhere else: no file, no rotation. With systemd, read them with `journalctl -u estopo`.
- `ESTOPO_LOG_FORMAT=json` produces one JSON line per message; `ESTOPO_LOG_LEVEL=debug` details every ingested reading.
- There is no HTTP request log. If you need one, put a reverse proxy in front of ES.Topo.

Useful messages:

| Message | Meaning |
|---|---|
| `starting` | start-up, with the version, the listen address, the data directory |
| `apply configuration` (error) | the collection of a cluster could not start (unreadable secret file, refused URL…) |
| `cluster identity changed, collection suspended` | a cluster changed its `cluster_uuid` |
| `ingest` (error) | a reading could not be ingested; it is retried |
| `retention` | weekly files were deleted |
| `spool replayed` | at start-up, pending readings were ingested |
| `LDAP authentication unavailable` | the directory could not be used for a sign-in |
| `authentication unavailable` (error) | the authentication settings or the sessions could not be read |
| `replication request with a wrong token` | a replication request presented a wrong token |
| `promoted to primary`, `demoted to standby`, `a more recent primary exists` | role changes (high availability) |

## What needs a restart

| Change | Restart |
|---|---|
| An environment variable | yes |
| Renewal of the HTTPS certificate or key | yes |
| Content of a secret file of a cluster (API key, password, certificate, CA) | yes |
| Replication token or CA | yes |
| "Simultaneous full captures (all clusters)" | yes |
| "States kept in memory" | yes |
| Clusters, schedule, hierarchy, family rules, whitelist | no, immediate |
| Authentication mode, accounts, LDAP settings, password of the LDAP service account | no, immediate |
| Retention | no; applied at the next hourly pass |
| "Expose /metrics" | no, immediate |

The service stops cleanly on `SIGTERM` (what `systemctl stop` sends) or Ctrl+C, with ten seconds to finish the requests in progress.

## Reverse proxy

ES.Topo can be placed behind a reverse proxy (nginx, HAProxy, Apache…), on these conditions:

- **Pass the `Host` header on unchanged.** ES.Topo compares the origin announced by the browser with this header and refuses changes if they differ (403 answer, code `cross_site`).
- **Serve ES.Topo at the root** of the host name: there is no configurable path prefix.
- Let long responses through: an export or a partition copy may last several minutes.
- Add the `Strict-Transport-Security` header yourself if the proxy terminates HTTPS: ES.Topo only sends it when it serves HTTPS itself.

Minimal example for nginx, to adapt, with ES.Topo listening on `127.0.0.1:8080` on the same machine:

```nginx
server {
    listen 443 ssl;
    server_name estopo.example.org;
    ssl_certificate     /etc/nginx/tls/estopo-fullchain.pem;
    ssl_certificate_key /etc/nginx/tls/estopo.key;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $http_host;
        proxy_read_timeout 600s;
    }
}
```

Consequences for authentication (client address seen by the sign-in throttling, transport rule, cookie): see [Authentication](authentication.md#behind-a-reverse-proxy). High-availability replication goes from instance to instance: do not put a proxy that would alter the requests in between.

## Audit trail

**Administration › System**, "Audit log", lists the last 200 actions: date, "Actor", "Action", "Target", "Details". Entries are kept one year and replicated to a standby.

| Action | What happened |
|---|---|
| `login`, `login.failure`, `login.ldap_unavailable` | sign-in succeeded, failed (the target is the client address), directory unavailable |
| `user.save`, `user.delete`, `user.password`, `user.reset_password`, `user.bootstrap` | account management; the actor of `reset_password` is `cli`, that of `bootstrap` is `system` |
| `auth.mode`, `auth.ldap_test` | change of the authentication mode or settings; LDAP test |
| `cluster.save`, `cluster.remove`, `cluster.purge`, `cluster.capture`, `cluster.resume`, `cluster.reattach` | configuration of and actions on a cluster |
| `family_rules.save`, `node.ack`, `node.ack.revoke` | family rules, acknowledgements |
| `retention.save`, `whitelist.save`, `tls.save`, `collector.save`, `cache.save`, `metrics.save` | global settings |
| `ha.promote`, `ha.demote`, `ha.rebuild`, `ha.resume`, `ha.peer`, `ha.fenced` | high availability |

Without authentication, the actor is always `anonymous`.

## Simulator and demonstration data

`essim` is shipped with every release (`essim-linux-amd64`, `essim-windows-amd64.exe`, `essim-macos-arm64`). It imitates the Elasticsearch APIs that ES.Topo uses, in the 7.17, 8.x and 9.x formats, to try the product, demonstrate it or test it. It has no place on a production machine.

Four typical clusters are available:

| Name | Content |
|---|---|
| `small` | small cluster, two data centres |
| `c` | 39 nodes, about 33,000 shard copies |
| `b` | 91 nodes, about 96,000 shard copies |
| `a` | 207 nodes, about 245,000 shard copies (slow to simulate) |

### Built history

`essim backfill` creates an ES.Topo data directory that already holds several days of history, ending now, with incidents:

```bash
./essim-linux-amd64 backfill -data /tmp/estopo-demo
ESTOPO_DATA_DIR=/tmp/estopo-demo ./estopo-linux-amd64 serve
```

By default: 14 days, clusters `b`, `c` and `small`, and the `demo` scenario (nodes that crash and come back, a machine lost for 3 hours, maintenance with `allocation.enable=primaries`, a rolling upgrade from 8.19 to 9.5, rebuilt nodes, an index setting that makes an index unallocatable, an API failure, the voluntary deletion of an index, a data centre lost for 50 minutes, and two incidents still in progress at the end). Allow about half an hour, mostly for cluster `b`; the steps are printed as they happen. In the directory produced, the clusters are disabled (their simulators are gone): you explore the history.

| Option | Default | Effect |
|---|---|---|
| `-data` | required | directory to create |
| `-days` | 14 | number of days of history |
| `-clusters` | `b,c,small` | clusters to simulate |
| `-versions` | `a=7.17.29,b=8.19.22,c=9.5.4,small=8.19.22` | Elasticsearch version per cluster |
| `-scenario` | `demo` | `demo`, a JSON file, or `none` |
| `-end` | now | end of the history (RFC 3339 date) |
| `-force` | no | replaces the directory if it already exists |

The log of the build is written to a file next to the directory, with the same name and the `.log` extension.

### Live simulated clusters

Start simulators with an accelerated clock:

```bash
./essim-linux-amd64 -clusters b,c -insecure -speed 60
```

Each cluster listens on its own port, starting at 19200: here `b` on `http://127.0.0.1:19200` and `c` on `http://127.0.0.1:19201`. Declare them in **Administration › Clusters** with these URLs, the authentication "None" and the machine attribute `machine`.

| Option | Default | Effect |
|---|---|---|
| `-clusters` | `small` | clusters to simulate |
| `-version` | `8.19.22` | Elasticsearch version of every cluster |
| `-versions` | | version per cluster, for example `a=7.17.29,b=9.5.4` |
| `-listen` | `127.0.0.1` | listen address |
| `-port` | 19200 | port of the first cluster; the next ones follow |
| `-insecure` | no | HTTP without authentication |
| `-pki` | `essim-pki` | directory of the test PKI, created if missing |
| `-basic` | `estopo:estopo-test` | credentials accepted for password authentication |
| `-apikey` | | accepted API key |
| `-start` | now | simulated start date |
| `-speed` | 1 | simulated seconds per real second |
| `-scenario` | | incidents to play while serving: `demo` or a JSON file whose dates count from the start |

Without `-insecure`, `essim` serves HTTPS with a test certificate authority, and prints the paths of the CA, the client certificate and its key to type into the form of the cluster.

### Actions

`essim action` triggers an incident on a running simulated cluster:

```bash
./essim-linux-amd64 action -url http://127.0.0.1:19200 topology
./essim-linux-amd64 action -url http://127.0.0.1:19200 node-leave node=hot-dc1-m02-2 for=30m
./essim-linux-amd64 action -url http://127.0.0.1:19200 machine-leave machine=warm-dc2-m02
./essim-linux-amd64 action -url http://127.0.0.1:19201 fault path=/_cat/shards status=503 for=20m
./essim-linux-amd64 action -url http://127.0.0.1:19200 rolling-upgrade version=9.5.4 every=10m
```

`topology` lists the machines and the nodes, to pick a target. A target is designated by the **end of its name**. Durations are in simulated time: with `-speed 60`, `for=30m` lasts 30 real seconds. `for=…` undoes the action after this delay.

| Action | Parameters | Effect |
|---|---|---|
| `node-leave` | `node=` | the node stops; its copies become unassigned |
| `node-join` | `node=` | the node comes back with its data |
| `node-replace` | `node=` | the node is rebuilt (new identifier, empty disk) |
| `machine-leave`, `machine-join` | `machine=` | every node of the machine stops, or comes back |
| `dc-leave`, `dc-join` | `dc=` | every node of the data centre stops, or comes back |
| `rolling-upgrade` | `version=`, `every=` (10m), `down=` (3m) | the nodes restart one by one in the new version |
| `cluster-setting` | `key=`, `value=`, `transient=true` | changes a cluster setting (empty value: removes it) |
| `index-setting` | `index=`, `key=`, `value=` | changes a setting of an index (or of the latest of a family) |
| `close-index`, `delete-index` | `index=` | closes or deletes the index |
| `remote` | `remote=`, `connected=true` | disconnects or reconnects a remote cluster (CCS) |
| `fault` | `path=`, `status=` | the API answers this HTTP code for the paths starting with `path` |
| `clear-faults` | | removes every API fault |
| `uuid-change` | | the cluster changes identity: ES.Topo suspends its collection |

These commands go through a control interface (`/_essim/…`) that only answers calls coming from the machine itself. ES.Topo never calls it.

### Scenario format

A scenario is a JSON file: a list of steps, each one being an action at a date.

```json
{
  "name": "my-scenario",
  "steps": [
    {"at": "-2d09:15", "cluster": "b", "do": "dc-leave", "dc": "dc2", "for": "50m"},
    {"at": "-40m", "cluster": "*", "do": "node-leave", "node": "hot-dc1-m01-2"}
  ]
}
```

- `at`: the date of the step, counted from the start of the simulation, or backwards from the end when negative. Examples: `40m`, `3h`, `6d`, `2d09:15` (2 days, 9 hours and 15 minutes).
- `cluster`: `a`, `b`, `c`, `small`, or `*` for all.
- `do` and the other fields: the action and its parameters, as in the table above.

Negative dates only make sense for `essim backfill`, which knows the end of the history. Live (`essim -scenario`), dates must count from the start.
