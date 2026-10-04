*(Version française : [high-availability_fr.md](high-availability_fr.md))*

# High availability (advanced guide)

This guide explains how to run two ES.Topo instances, one that collects and one that follows it, and how to switch from one to the other. It assumes that you know how to install an instance over HTTPS with authentication enabled (see [Installation](installation.md) and [Authentication](authentication.md)).

In the examples, the two machines are called `estopo-a.example.org` (the primary at the start) and `estopo-b.example.org` (the standby at the start), and listen on port 8443.

## What the pair does, and what it does not do

- The **primary** collects the Elasticsearch clusters and accepts configuration changes.
- The **standby** **never** contacts the clusters. Every 30 seconds, it fetches the configuration and the new readings from the primary, and builds the same history. It is read-only.
- **Promotion is manual**: an administrator clicks a button on the standby. There is **no automatic failover**, and ES.Topo does not provide an address shared by both instances: users open the address of the instance they want, or you put a mechanism of your choice in front.
- An old primary that comes back after a promotion notices it, stops collecting (it **fences** itself) and waits for an administrator to demote it. This only works if it knows where the other instance is: see [Limits and risks](#limits-and-risks).
- The standby is **not a backup**: what is deleted on the primary is deleted on the standby too.

## Prerequisites

| Prerequisite | Why |
|---|---|
| **Same version** of ES.Topo on both machines | The standby refuses to follow a primary whose databases do not have the same structure. |
| **HTTPS on both instances** | The replication token must not travel in clear. The standby calls the primary, and the primary calls the standby to watch it. |
| Each machine can reach the other on its port | In both directions. |
| An identical **token file** on both machines, of at least 16 characters | It is the shared secret that authorises replication. |
| The **CA** that signed the certificate of the other instance, **on both machines**, when it is not a CA known to the system | `ESTOPO_REPLICATION_CA` variable. If the file cannot be read or holds no certificate, the start is refused. |
| The **same secret files at the same paths** on both machines (certificates, keys, password files of the clusters, CA, LDAP files) | The configuration is replicated, not the files. After a promotion, the standby must be able to read the same paths. |
| **Clocks on time** (NTP) on both machines | Fencing compares the promotion dates of the two instances. |
| On the standby, as much disk space as on the primary, plus a margin | See [Rebuild](#rebuild). |

## Setup

### 1. The token

On `estopo-a`, create the token, then copy the same file to `estopo-b` over a safe channel:

```bash
openssl rand -hex 32 > /etc/estopo/replication.token
chown root:estopo /etc/estopo/replication.token
chmod 0640 /etc/estopo/replication.token
```

Whoever holds this token can read the whole configuration and the whole history of an instance, including the password hashes: protect it like a private key.

### 2. The primary

In `/etc/estopo/estopo.env` of `estopo-a`, add:

```sh
ESTOPO_REPLICATION_TOKEN_FILE=/etc/estopo/replication.token
# Only if the certificate of estopo-b is not signed by a system CA:
ESTOPO_REPLICATION_CA=/etc/estopo/tls/ca.pem
```

Restart (`systemctl restart estopo`). Check: in **Administration › High availability**, the "Replication token" line says "yes" and the "Role" is "Primary".

### 3. The standby

Install ES.Topo on `estopo-b` like a normal instance (binary, service, HTTPS certificate), with an **empty** data directory, and copy the secret files there at the same paths. In its environment file:

```sh
ESTOPO_LISTEN=0.0.0.0:8443
ESTOPO_DATA_DIR=/var/lib/estopo
ESTOPO_TLS_CERT=/etc/estopo/tls/estopo-fullchain.pem
ESTOPO_TLS_KEY=/etc/estopo/tls/estopo.key
ESTOPO_SECRETS_DIR=/etc/estopo/secrets
ESTOPO_REPLICATION_TOKEN_FILE=/etc/estopo/replication.token
ESTOPO_REPLICATION_CA=/etc/estopo/tls/ca.pem
ESTOPO_STANDBY_OF=https://estopo-a.example.org:8443
```

Do not set `ESTOPO_ADMIN_PASSWORD`: a standby gets its accounts and its authentication mode from the primary.

Start the service. The standby first copies the whole history of the primary, then follows the new readings. In its journal: `following primary instance_id=…`, then `partitions copied from the primary` for each cluster.

`ESTOPO_STANDBY_OF` is only used at the **first** start: the role is then saved in the data directory and wins over the variable. A promoted standby therefore stays primary even if the variable is still there (a warning says so in the journal).

### 4. Declaring the standby on the primary

On `estopo-a`, in **Administration › High availability**, "Watched standby" section: type `https://estopo-b.example.org:8443` and click "Save".

Expected result, under the field: "Standby seen as Standby on …". If the line shows "Standby unreachable: …", the text gives the reason: wrong name or port, firewall, certificate refused (`ESTOPO_REPLICATION_CA` on the primary), different token (`invalid replication token`).

**This step is not optional.** It is what lets the primary notice that the standby was promoted and stop collecting.

### 5. Checking

Sign in on `estopo-b` with an account of the primary (the accounts arrive at the first synchronisation). You must see:

- the banner "Standby instance: read-only, the configuration and history come from the primary.";
- the same clusters and the same history as on the primary;
- in **Administration › High availability**: "Role: Standby", the "Primary followed", a recent "Last synchronisation" and a small "Lag".

## What is replicated

Every 30 seconds, the standby:

1. checks that the primary is indeed the instance it follows (see [Replication blocked](#replication-blocked));
2. copies the configuration if it changed: clusters, settings (retention, whitelist, authentication and LDAP, default CA…), family rules, accounts, acknowledgements, audit trail;
3. for each cluster, fetches the readings it does not have yet and ingests them exactly as the primary does;
4. copies the complete history files of a cluster when it has nothing for it, or when it is more than 72 hours behind (the time during which the primary keeps the raw readings);
5. deletes the history of a cluster that the primary purged.

**Not** replicated, because they describe the instance itself: the sessions (you sign in separately on each instance), the alerts, the role and the high-availability state. The secret files and the environment variables are not replicated either.

A standby (like a fenced old primary) refuses every change: clusters, accounts, settings, connection test, LDAP test, password change. The message is `this instance is standby: changes are made on the primary`. Only the high-availability actions, signing in and signing out remain possible.

## Reading the High availability page

**Administration › High availability**, on any instance:

| Item | Meaning |
|---|---|
| "Role" | "Primary", "Standby" or "Old primary (collection suspended)". After a promotion: "promoted on …". |
| "Primary followed" | On a standby: the URL of the primary. |
| "Replication token" | "yes", or "not configured (ESTOPO_REPLICATION_TOKEN_FILE)". |
| "Reason" | On an old primary: why it fenced itself. |

On a standby, "Replication" section:

| Item | Meaning |
|---|---|
| "Last synchronisation" | Last cycle completed without error. |
| "Lag" | Gap between the newest reading on the primary and the newest ingested here (the worst cluster). |
| "Copying partitions…" | A rebuild is in progress. |
| "Last error" | Error of the last cycle, with its date. |
| "Replication blocked: …" | See [Replication blocked](#replication-blocked). |
| Table per cluster | "Last record received", "Last record on the primary", "Records received" and "Partition copies" since the start. |
| Buttons | "Promote to primary", "Rebuild". |

On a primary or an old primary, "Watched standby" section: the URL of the other instance, the result of the last check, and the buttons "Demote to standby" and, when fenced, "Resume collection".

## Procedures

### Planned switch

To make `estopo-b` the primary, for example before maintenance on `estopo-a`:

1. On `estopo-b`, check that the "Lag" is small and that there is no error or block.
2. Stop `estopo-a` (`systemctl stop estopo`). Wait one minute, so that the standby has fetched the last readings.
3. On `estopo-b`, click "Promote to primary" and confirm. The standby banner disappears, the role becomes "Primary (promoted on …)" and the collection starts: check it in **Administration › Collectors**.
4. When `estopo-a` restarts, it queries `estopo-b`, sees the promotion and fences itself (red banner). Then click, on `estopo-a`, "Demote to standby" (see below).

### Primary lost: promote

1. Make sure the primary is really stopped or unable to collect.
2. On the standby, **Administration › High availability**, "Promote to primary", then confirm.

The standby becomes primary with everything it had received. What the old primary had read without having had time to pass it on (a few tens of seconds at most in normal operation) is missing from the new history. The promotion does not wait for a primary that no longer answers. The old primary automatically becomes the instance watched by the new one.

### Return of the old primary

At start-up, then every 30 seconds, a primary queries the instance it watches. If it sees that this instance is primary and was promoted more recently than itself, it:

- stops collecting;
- takes the role "Old primary (collection suspended)";
- shows the banner "Collection suspended: a standby has been promoted since. Demote this instance (Administration › High availability).";
- refuses every change.

To make it a standby again: on this old primary, **Administration › High availability**, check the "URL of the new primary" (by default, the watched instance), click "Demote to standby" and confirm. The instance then copies the **whole** history of the new primary. The readings it alone held are lost.

### Rebuild

"Rebuild", on a standby, copies the whole history again from the primary. Use it when the standby looks inconsistent, or to get out of a block (next section).

The standby first downloads all the files of a cluster, verifies them, then replaces its own: during the operation, up to **twice** the disk space of that cluster is needed. On the primary side, each file is first copied into the `tmp` subdirectory of the data directory before being sent: allow room for one extra weekly file. The page shows "Copying partitions…".

### Resume collection

"Resume collection" puts a fenced old primary back into service **without** demoting it. Only use this button if the other instance is no longer primary (for example, it was itself demoted or destroyed). It is refused as long as the other instance answers and says it is a more recent primary; it is accepted if it does not answer: it is up to you to make sure it no longer collects.

### Replacing a lost instance

If a machine is lost for good, install a new standby from an empty data directory (step 3), with `ESTOPO_STANDBY_OF` naming the current primary. Then, on the primary, update "Watched standby" if the address changed.

## Replication blocked

Every data directory carries a random instance identifier, created with it. At its first synchronisation, the standby remembers the identifier of its primary. It then **blocks** replication, importing nothing and deleting nothing, in two cases:

- the primary answers with **another identifier**: its data directory was replaced (machine reinstalled, volume lost, address pointing to another instance);
- the primary announces **no cluster** while the standby holds history.

Without this safeguard, a primary that came back empty would empty the standby within one cycle.

You see it:

- in **Administration › High availability** of the standby: box "Replication blocked:" followed by the reason, and "Nothing is imported or deleted while the block lasts. …";
- in the journal of the standby: `replication stopped until an administrator rebuilds this standby`;
- through the metric `estopo_replication_blocked`, which is 1.

To get out of it, decide which instance holds the right history:

| Situation | Action |
|---|---|
| The primary lost its data and **the standby has the right history** | On the standby: "Promote to primary". Then make the other machine a standby again ("Demote to standby" on it, or reinstallation with `ESTOPO_STANDBY_OF`). |
| The primary was replaced or emptied **on purpose**, and the standby must become its copy | On the standby: "Rebuild". It accepts the primary as it is: if the primary is empty, the standby is emptied. |
| The address of the primary names another instance by mistake | Fix the name resolution or the routing; the block is lifted at the next cycle. |

## Limits and risks

- **No automatic failover.** If the primary goes down, nothing is collected until someone promotes the standby.
- **Without "Watched standby" filled in on the primary, there is no fencing.** An old primary that comes back after a promotion resumes collecting: two instances then collect in parallel and their histories diverge. The watch also requires the token on the primary. If this happens: on the old primary, click "Demote to standby" and type the URL of the new primary.
- **A partial loss on the primary propagates.** The block only triggers if the primary changed its identifier or no longer announces any cluster. A purged cluster, a shortened retention or a wrong configuration change are replicated normally. Take backups (see [Operations](operations.md#backup-and-restore)).
- **Demoting loses** the readings the instance alone held.
- **Disk space**: up to twice the size of a cluster during its rebuild.
- **The token gives full read access**, password hashes included.
- **If the primary has no authentication, neither has the standby**, and any visitor can promote it. Enable authentication before setting up the pair.
- Replication is covered by automated tests that run two instances on one machine; the time needed to copy very large files between two real machines has not been measured.

## Monitoring

Enable `/metrics` (see [Operations](operations.md#metrics)) and watch, on the standby:

| Metric | Suggested alert |
|---|---|
| `estopo_replication_blocked` | equals 1: intervention needed |
| `estopo_replication_lag_seconds` | high for a long time (for example more than 900) |
| `estopo_replication_last_sync_timestamp_seconds` | too old: the standby can no longer synchronise |
| `estopo_ha_role{role="…"}` | on each instance: an unexpected role (`fenced`, or two `primary`) |

`/healthz` also gives the role of each instance.

## Upgrading both instances

Both instances must run the same version. Proceed in this order:

1. stop the standby;
2. stop the primary, replace its binary, restart it;
3. replace the binary of the standby, restart it.

While the versions differ, the standby refuses to synchronise and says so in "Last error" (`run the same estopo version on both`); it resumes by itself once the versions match.

## Troubleshooting

| Message | Cause and remedy |
|---|---|
| At start-up: `ESTOPO_STANDBY_OF requires ESTOPO_REPLICATION_TOKEN_FILE` or `a standby needs ESTOPO_REPLICATION_TOKEN_FILE` | The token file is not configured on the standby. |
| At start-up: `replication token …: at least 16 characters are required` | Token too short. |
| At start-up: `replication CA …: no certificate found` or `replication CA: open …` | The `ESTOPO_REPLICATION_CA` file is empty, unreadable or not in PEM format. |
| At start-up: `replication over plain HTTP on a non-loopback address is refused…` | The instance has a token but does not serve HTTPS. |
| At start-up: `primary URL: …: the scheme must be https` (or `the replication token would travel in clear`) | `ESTOPO_STANDBY_OF` must be an `https://` URL. |
| "Last error": `primary unreachable: …` | Network, name, port, certificate. `x509: certificate signed by unknown authority`: set `ESTOPO_REPLICATION_CA`. If the machine has `HTTPS_PROXY` variables, the replication client follows them: add the other instance to `NO_PROXY`. |
| "Last error": `… invalid replication token` | The two token files differ. |
| "Last error": `… is not a primary (role standby)` | The followed instance is not primary: both instances are standbys. Promote the one that must collect. |
| "Last error": `… 404 …` on `/api/v1/replication/…` | The primary has no token configured: its replication interface is disabled. |
| "Last error": `meta.db schema … run the same estopo version on both` | Different versions. |
| "Demote to standby" button: `a standby needs ESTOPO_REPLICATION_TOKEN_FILE` | This instance has no token: add it and restart. |
| "Resume collection" button: `… is still the primary (promoted at …): demote it first` | The other instance is still primary. |
