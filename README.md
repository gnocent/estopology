*(Version française : [README_fr.md](README_fr.md))*

# ES.Topo

ES.Topo keeps the history of the **topology** of your Elasticsearch clusters (7.17 to 9.x) and lets you explore it over time. Where were the shards at 3:12 a.m.? Which nodes left, and with which copies? What moved between yesterday and now, round trips included? A single binary with no dependencies collects read-only, stores the history in SQLite files and serves a fully embedded web interface: it also works on a network without Internet access.

> **Version 0.8.0-beta.** This is the first public release. The product has been tested against real test clusters running Elasticsearch 7.17, 8.19 and 9.5 and on simulated data, not yet in production at scale. Expect defects, read the [known limits](CHANGELOG.md#known-limits) and do not expose it on a network before reading [SECURITY.md](SECURITY.md). The data format and the API may still change before version 1.0.

![ES.Topo demonstration: picking a date on the time band, physical view of the cluster, zooming into a machine then a node, logical view by index family, differences between two dates and lost shard copies](docs/demoestopo.gif)

*An incident replayed on simulated data: going back in time, drilling down to the node, then comparing two dates.*

## Why

During an incident (lost nodes, unassigned shards, massive rebalancing, an upgrade going wrong), Elasticsearch shows the **present** state. Monitoring tools give metrics, not the topology. ES.Topo answers the questions about the **past**:

- where each shard copy was at a given date, and on which nodes, machines and zones;
- which nodes existed, with which roles, attributes and versions, and when that changed;
- what changed between two dates, including what came back to where it started;
- which copies were lost with a node or a machine, and what became of them.

ES.Topo never modifies a cluster: its client only sends read requests.

## Features

- **Adaptive, read-only collection**: a light probe every 5 minutes; a full capture when something changes (health, nodes, moving shards, cluster settings), at most every 10 minutes and at least every hour. These durations are set per cluster.
- **Time band** on every dated view: health strip, event density and a cursor. The date is in the page address: every view is a permalink.
- **Physical view**: the nodes arranged by zone, tier and machine, coloured as you choose (shard state, disk, CPU, heap, shard count, version). One page per zone, tier, machine and node, with a "Zoom out" button and a breadcrumb.
- **Logical view**: matrix of the shard copies, one row per index family or per index, one column per node or per zone, tier or machine; filter on names.
- **Diff between two dates** and **path taken**, which also shows the round trips a comparison cannot see.
- **Losses**: copies that disappeared with their node or machine, and what became of them (replica promoted, reallocation, copy still unassigned).
- **Event journal**, with the uncertainty window of each change, or the exact date when Elasticsearch provides it.
- **Relocation flows** (Sankey diagram) and the trajectory of a shard.
- **Allocation constraints**: indices whose rules leave not enough nodes, or barely enough.
- **Charts** per index family, per index, per node and per group of nodes.
- **Search** for a node, an IP address, a machine, an index, a family or a given shard; CSV, JSON and XLSX **exports**.
- Interface in French and English, light or dark theme, help in every screen ("Understanding this screen").
- **Authentication**: none, local accounts or an LDAP directory (OpenLDAP); reader and admin roles.
- **High availability**: a passive standby instance, promoted by hand.
- **Operations**: retention by duration and by size, `/healthz`, `/metrics` (Prometheus, to be enabled), audit trail.
- **Simulator** `essim` to try the product without a real cluster.

## Requirements

- **To run it: nothing to install.** The binary is static; the interface, the fonts and the database engine are inside.
- **Platforms**: Linux x86-64 for production (start-up tested on AlmaLinux and Rocky Linux 8, 9 and 10). Windows x64 and macOS arm64 (Apple Silicon) binaries are provided for trials and development.
- **Elasticsearch**: 7.17, 8.x and 9.x. The responses of versions 7.17.29, 8.19.22 and 9.5.4 are tested from captures of real clusters.
- **Browser**: a recent version of Chrome, Edge or Firefox.

## Quick start (5 minutes)

The commands are for Linux. For Windows and macOS, see the [installation guide](docs/installation.md).

1. **Download** `estopo-linux-amd64` and `SHA256SUMS` from the [releases page](../../releases).
2. **Verify** the file, then make it executable:

   ```bash
   sha256sum -c SHA256SUMS --ignore-missing
   chmod +x estopo-linux-amd64
   ```

   The first command must print `estopo-linux-amd64: OK`.
3. **Run it.** Without any setting, it listens on `127.0.0.1:8080` (this machine only) and creates a `data` directory next to the binary:

   ```bash
   ./estopo-linux-amd64 serve
   ```

4. **Open** `http://127.0.0.1:8080`. A banner reminds you that authentication is disabled: as long as the listener stays local and you are the only user of the machine, this is acceptable for a trial.
5. **Add a cluster**: **Administration › Clusters**, "New cluster", then "Test the connection" and "Save". The guide [Declaring a cluster](docs/clusters.md) explains the account to create on the Elasticsearch side and every field of the form. The first capture arrives in less than a minute.

For a lasting installation (systemd service, HTTPS, authentication), follow the [installation guide](docs/installation.md).

### Trying it with simulated data

`essim`, shipped with `estopo`, builds a 14-day history containing incidents (lost nodes and machine, rolling upgrade, lost data centre…):

```bash
chmod +x essim-linux-amd64
./essim-linux-amd64 backfill -data /tmp/estopo-demo -clusters c,small
ESTOPO_DATA_DIR=/tmp/estopo-demo ./estopo-linux-amd64 serve
```

Then open `http://127.0.0.1:8080`. The `-clusters c,small` option limits the trial to two small clusters; without it, the build adds a 91-node cluster and lasts about half an hour. The other uses of the simulator (live simulated clusters, incidents triggered by hand, scenarios) are described in the [operations guide](docs/operations.md#simulator-and-demonstration-data).

## Documentation

| Document | For whom | Other language |
|---|---|---|
| [Installation](docs/installation.md) | anyone installing ES.Topo: binary, service, HTTPS, first start | [Français](docs/installation_fr.md) |
| [Declaring a cluster](docs/clusters.md) | the Elasticsearch administrator: read-only account, form, schedule, families | [Français](docs/clusters_fr.md) |
| [Authentication](docs/authentication.md) (advanced) | whoever opens access to several people: local accounts, LDAP, sessions | [Français](docs/authentication_fr.md) |
| [High availability](docs/high-availability.md) (advanced) | whoever wants a second instance ready to take over | [Français](docs/high-availability_fr.md) |
| [Operations](docs/operations.md) | whoever runs the service: storage, backup, monitoring, simulator | [Français](docs/operations_fr.md) |
| [Security](SECURITY.md) | everyone, before exposing the service | [Français](SECURITY_fr.md) |
| [Changelog](CHANGELOG.md) | content of each version, known limits | [Français](CHANGELOG_fr.md) |
| [Specification](SPEC.md) | whoever wants to understand the design in detail or contribute | [Français](SPEC_fr.md) |

## Security in short

- ES.Topo only **reads** the Elasticsearch clusters.
- By default it only listens on `127.0.0.1` and **has no authentication**: every visitor is an administrator. Enable authentication **before** exposing it on the network.
- Authentication (local accounts or LDAP) is refused on a non-local HTTP listener: HTTPS is required.
- Passwords, keys and tokens are never typed into the interface: you give the **path of a file**.
- State-changing requests that come from another website are refused.

The security model, the hardening checklist, the known limits and how to report a vulnerability are in [SECURITY.md](SECURITY.md).

## Building and developing

You need Go (the version given in `go.mod`) and, only to modify the interface, Node.js 20 with npm. The compiled interface (`web/dist`) is under version control: `go build ./cmd/estopo` is enough to get a binary.

```bash
./scripts/check.sh               # gofmt, vet, tests, builds for Linux, Windows and macOS
UI_CHECK=1 ./scripts/check.sh    # also: type check and build of the interface
RACE=1 ./scripts/check.sh        # also: race detector (needs gcc)
./build-release.sh               # release binaries in dist/
cd web && npm ci --ignore-scripts && npm run build   # rebuild the interface
cd web && npm run test:e2e       # Playwright tests of the interface, against essim and estopo
```

`build-release.sh` produces `dist/linux-amd64/`, `dist/windows-amd64/` and `dist/macos-arm64/` (`estopo` and `essim` binaries, licence, notices, SHA-256 checksums), as well as `dist/release/`, which holds the published files: `estopo-linux-amd64`, `estopo-windows-amd64.exe`, `estopo-macos-arm64`, the same for `essim`, `SHA256SUMS`, `LICENSE` and `THIRD_PARTY_NOTICES.md`.

Test environments, which need Docker:

- `scripts/capture-fixtures.sh <version>`: starts a real secured Elasticsearch cluster, captures its responses and tests the client with the three authentication methods;
- `testenv/ldap/run.sh`: tests authentication against a real OpenLDAP, over LDAPS and StartTLS;
- `testenv/distros/run.sh`: starts the binary on AlmaLinux and Rocky Linux 8, 9 and 10.

Two GitHub automations exist. On every push to the main branch and on every pull request, `scripts/check.sh` runs and `web/dist` is checked against the sources. On every `v*` tag, the binaries are built and published.

## Project layout

```
cmd/estopo/    main binary (serve, version, admin reset-password)
cmd/essim/     Elasticsearch cluster simulator
internal/      collection, storage, queries, API, authentication, high availability, simulation
web/           sources of the interface (Svelte); web/dist is the compiled version, embedded in the binary
deploy/        example systemd unit and environment file
docs/          guides
scripts/       verification, capture of real responses, licence notices
testenv/       test environments (Elasticsearch, OpenLDAP, Linux distributions)
testdata/      responses of real Elasticsearch 7.17, 8.19 and 9.5 clusters
```

## Licence

ES.Topo is distributed under the [GNU Affero General Public License v3](LICENSE) (AGPL-3.0). The licences of the libraries and fonts included in the binaries are reproduced in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md), shipped with every release.

## No warranty

This software is provided "as is", without warranty of any kind, as stated in sections 15 and 16 of the licence. This is a beta version: try it on a test environment first, back up its data directory, and check for yourself what it displays before relying on it to decide on an action on a cluster. ES.Topo is an independent project, not affiliated with Elastic N.V.; Elasticsearch is a trademark of its owner.
