*(Version française : [clusters_fr.md](clusters_fr.md))*

# Declaring and configuring an Elasticsearch cluster

This guide explains, end to end, how to have a cluster followed by ES.Topo: what to create on the Elasticsearch side, where to put the secrets, how to fill in the form, then how to tune the collection and the display. It assumes that ES.Topo is installed (see [Installation](installation.md)) and that you are an administrator of the application.

## Overview

1. On the Elasticsearch side, create a read-only role, then an identity holding this role: an API key, a user with a password, or a client certificate.
2. On the ES.Topo machine, put the matching secret in a **file**.
3. In **Administration › Clusters**, fill in the form, click "Test the connection", then "Save".
4. Check in **Administration › Collectors** that the first probe and the first capture succeeded.

ES.Topo only reads. Its client refuses to send anything other than a `GET` or `HEAD` request.

## On the Elasticsearch side: the read-only account

The requests below are run in the Kibana console (Dev Tools) or with `curl`, with an account allowed to manage security.

### The role

```
PUT _security/role/estopo_reader
{
  "cluster": ["monitor"],
  "indices": [
    {
      "names": ["*"],
      "privileges": ["monitor", "view_index_metadata"],
      "allow_restricted_indices": true
    }
  ]
}
```

These are exactly the privileges checked by "Test the connection": the cluster privilege `monitor`, and the index privileges `monitor` and `view_index_metadata` on every index, system indices included (`allow_restricted_indices`). No privilege to read documents is requested: ES.Topo reads neither the documents nor the mappings.

Then choose **one** of the three following identities.

### Option 1: an API key

```
POST _security/api_key
{
  "name": "estopo",
  "role_descriptors": {
    "estopo_reader": {
      "cluster": ["monitor"],
      "indices": [
        {
          "names": ["*"],
          "privileges": ["monitor", "view_index_metadata"],
          "allow_restricted_indices": true
        }
      ]
    }
  }
}
```

The response holds `id`, `api_key` and, on recent versions, `encoded`. Write into the secret file **either** the value of `encoded`, **or** the text `id:api_key` (the two values separated by a colon): ES.Topo accepts both forms. An API key cannot have more privileges than the account that creates it.

### Option 2: a user with a password

```
PUT _security/user/estopo
{
  "password": "<long random password>",
  "roles": ["estopo_reader"]
}
```

Write the password, alone, into the secret file.

### Option 3: a client certificate

ES.Topo presents a client certificate during the TLS handshake. The cluster must therefore accept client certificates on its HTTP interface and have a PKI realm configured: this is Elasticsearch configuration, described in its documentation. The role is then granted through a role mapping:

```
PUT _security/role_mapping/estopo_pki
{
  "roles": ["estopo_reader"],
  "enabled": true,
  "rules": {
    "all": [
      { "field": { "realm.name": "pki1" } },
      { "field": { "username": "estopo" } }
    ]
  }
}
```

Replace `pki1` with the name of your PKI realm and `estopo` with the user name this realm derives from the certificate (by default, its CN). This authentication was tested on versions 7.17.29, 8.19.22 and 9.5.4 with a trial licence; the licence level the PKI realm requires in production has not been verified.

## The secret files

ES.Topo never stores a password, an API key or a private key: its configuration only holds the **path** of a file, which the service reads itself.

| File | Expected content |
|---|---|
| API key | one line: the `encoded` value, or `id:api_key` |
| Password | one line: the password (leading and trailing spaces and line breaks are ignored) |
| Client certificate | PEM; may hold the full chain |
| Private key | PEM. If it is encrypted, only the PKCS#8 format with PBES2 and AES (what OpenSSL produces by default) is accepted |
| Key passphrase | one line: the passphrase |
| CA | a PEM file, or a directory whose `.pem`, `.crt` and `.cer` files are all loaded (without going into subdirectories) |

Where to put them, with the installation described in [Installation](installation.md):

```bash
install -o root -g estopo -m 0640 prod-apikey /etc/estopo/secrets/prod-apikey
```

- The directory `/etc/estopo/secrets` belongs to `root`, group `estopo`, mode `0750`; the files have mode `0640`. The service can read them, not modify them.
- **`ESTOPO_SECRETS_DIR`**: when this variable is set (the example environment file sets it to `/etc/estopo/secrets`), every path typed in the administration must name an existing file located inside this directory, symbolic links resolved. Otherwise the save or the test is refused with the message `… not an existing file of the secrets directory (ESTOPO_SECRETS_DIR)`. Without this variable, an administrator of the application can make the service read any file its system account can read: set it.
- **Rotation**: the files are read when the collection of a cluster starts. After changing the content of a file, **restart the service** (`systemctl restart estopo`); the new content is not read otherwise.
- With high availability, the same files must exist at the same paths on both machines.

## The default CA

At the top of **Administration › Clusters**, the field "Default CA of the clusters (file or directory; empty = system CAs)" names the certificate authority or authorities that signed the certificates of your clusters. It is used by every cluster that has no CA of its own.

1. Copy the certificate of your root CA and those of the intermediate CAs, in PEM format, into a directory, for example `/etc/estopo/secrets/ca/`.
2. Type this path and click "Save".
3. Expected result: "Saved: N certificate(s) loaded.". An error means that the path does not exist, cannot be read or holds no certificate.

If the field is empty and a cluster has no CA of its own, the authorities known to the operating system are used.

ES.Topo always verifies the certificate of the cluster and the host name of the URL. **There is no option to disable this verification**: the URL must use a name present in the certificate.

## The form, field by field

In **Administration › Clusters**, click "+ New cluster".

| Field | Meaning | Values and default |
|---|---|---|
| "Id (lowercase letters, digits, - and _)" | Technical name of the cluster in ES.Topo: it names its directories and appears in page addresses. | 63 characters at most, starts with a lowercase letter or a digit. **Cannot be changed** after creation. |
| "Display name" | Name shown in the interface. | Default: the id. |
| "URL(s), one per line" | HTTP(S) addresses of the cluster. | One is enough (a virtual address, for example). If there are several, they are tried in order when the connection fails; the one that answered last is tried first. |
| "ES authentication" | How to identify. | "Client certificate (mTLS)" (default), "API key", "User / password", "None". |
| "Call timeout (s)" | Maximum duration of each call. | 60 seconds. The connection timeout is 10 seconds and cannot be set. |
| "Client certificate file", "Private key file", "Passphrase file (optional)" | For "Client certificate (mTLS)". | File paths. |
| "API key file" | For "API key". | File path. |
| "User", "Password file" | For "User / password". | Account name; file path. |
| "CA: file or directory (empty = default CA, else system CAs)" | CA specific to this cluster. | Empty in the general case. |
| "Add the system CA store" | Also accepts the authorities known to the system, in addition to the given CA. | Unticked. |
| "Node attribute naming the machine (empty = host)" | See [Hierarchy and machine attribute](#navigation-hierarchy-and-machine-attribute). | Empty. |
| "Collection enabled" | Untick to stop the collection without deleting anything. | Ticked. |
| "Declared node reference (optional, one name per line)" | See [Node reference and acknowledgements](#node-reference-and-acknowledgements). | Empty. |
| "Navigation hierarchy (…)" | See [Hierarchy and machine attribute](#navigation-hierarchy-and-machine-attribute). | Empty = automatic. |
| "Schedule" section | See [Schedule](#schedule). | Empty = default values. |

Security rules applied when saving and when the collection starts:

- URLs must start with `https://` or `http://`.
- With "API key" or "User / password", an `http://` URL is **refused**, except towards the local machine (`localhost` or `127.0.0.1`): the secret would travel in clear.
- HTTP redirects are never followed, and the proxy variables of the system are ignored: ES.Topo reaches the clusters directly.

The family rules and the acknowledgements appear below the form once the cluster is saved.

## Testing the connection

The "Test the connection" button tries the configuration **as typed**, even if not saved, without changing anything. It makes three calls: `GET /`, `GET /_cluster/health` and `GET /_security/user/_has_privileges`.

How to read the result:

| Display | Meaning |
|---|---|
| "Connection OK: *name* *version*, *N* nodes, health *colour*." | The cluster answers and the identity is accepted. |
| "Missing privileges: cluster:monitor, index:\*:view_index_metadata…" (orange box) | The identity is accepted but the role is incomplete. Each item names a missing cluster privilege (`cluster:…`) or index privilege (`index:*:…`). Fix the role. |
| "Failed: …" | The call failed. The text that follows is the error; see [Common errors](#common-errors). |

Limit: on a cluster whose security is disabled, the `_has_privileges` call does not exist; the behaviour of the test in that case has not been verified on a real cluster.

On a standby instance, the test is refused: a standby never contacts the clusters.

After "Save", the collection starts at once: a probe, then a first full capture. The cluster appears on the "Clusters" page; until something has arrived, it shows "No capture yet".

## Schedule

ES.Topo takes two kinds of readings:

- the **light probe**, frequent, reads the health, the list of nodes and their metrics;
- the **full capture** also reads every index and every shard. It is triggered when the probe sees a change, and at least from time to time.

"Schedule" section of the form (leave empty to keep the default value):

| Field | Default | Purpose | Limits |
|---|---|---|---|
| "Light probe every (min)" | 5 | Period of the probe. This is the precision with which the departure of a node is dated. | at least 0.5 minute |
| "Full capture at most every (min)" | 10 | Minimum delay between two full captures, so as not to load a struggling cluster. A trigger occurring during this delay is deferred, not lost. | at least the probe period |
| "Full capture at least every (min)" | 60 | A full capture is taken even if nothing changes. | at least the minimum delay |
| "Jitter (% of the period)" | 10 | Share of randomness added to the probe period, so that the clusters are not all queried at the same instant. | 0 to 50 |
| "Trigger threshold (moving shards)" | 0 | A full capture is triggered when the number of relocating, initializing or unassigned shards has changed by more than this threshold since the last full capture. | 0 or more |

What triggers a full capture: the first probe, a change of health, a change of the node set, moving shards beyond the threshold, a change of the cluster settings, the "Capture now" button, and the maximum delay.

The "Capture now" button honours the minimum delay; tick "Force (ignore the minimum interval)" to override it.

The number of simultaneous full captures, all clusters together, is set in **Administration › Collectors**: "Simultaneous full captures (all clusters)", from 1 to 32, 3 by default. This setting is applied at the next restart.

## Navigation hierarchy and machine attribute

The physical view arranges the nodes by levels. By default: the awareness attributes of the cluster (`cluster.routing.allocation.awareness.attributes`, in order), then the tier, then the machine, then the node.

**"Navigation hierarchy" field**: a comma-separated list of words.

- `tier`, `machine` and `node` are built-in levels.
- Any other word is the **name of a node attribute** (`node.attr.<name>` in the Elasticsearch configuration).
- `node`, if present, must come last. A level cannot appear twice.

Examples:

| Input | Effect |
|---|---|
| *(empty)* | awareness attributes, tier, machine, node |
| `dc, tier, machine, node` | data centre, then tier, then machine |
| `dc, rack, tier, machine, node` | a "rack" level between the data centre and the tier |
| `tier, machine, node` | no zone level |

The hierarchy is applied at display time: changing it also rearranges past dates.

**"Node attribute naming the machine (empty = host)" field**: when several Elasticsearch nodes run on one physical machine, ES.Topo groups them. By default, the machine is the `host` field of the node, and its IP address if `host` is empty. If your nodes carry an attribute naming the machine (for example `node.attr.machine`), type its name here. The machine is recorded at each full capture: a change of this field applies to the following captures.

The **tier** of a node comes from its roles (`data_hot`, `data_warm`, `data_cold`, `data_frozen`, `data_content`, `data`). A node without a data role is placed under "master" or "coordinating".

## Index families

A family groups the indices of one series: it is the unit of the logical view and of several screens.

### Automatic detection

The family is derived **from the index name alone**, by removing from the end, as many times as possible:

- a six-digit rollover counter: `-000001`;
- a date: `2026.09.28`, `2026-09-28`, `20260928`, `2026.09`, `2026.w40`;
- a year alone, between **1990 and 2099**, preceded by a separator: `-2026`.

Examples:

| Index | Family |
|---|---|
| `logs-app-2026.09.28` | `logs-app` |
| `weblog-000002` | `weblog` |
| `.ds-logs-app-2026.09.28-000001` | `.ds-logs-app` |
| `metrics-8080` | `metrics-8080` (8080 is not a year between 1990 and 2099) |
| `archive-2031` | `archive` |
| `customers` | `customers` (a name without a suffix is its own family) |

Neither aliases, nor data streams, nor ILM policies are consulted.

### Rules per cluster

When the detection does not fit, open the cluster in **Administration › Clusters**: the "Family rules" card accepts one regular expression per line (RE2 syntax, the one used by Go).

- The first rule matching the index name wins.
- The family is the named group `(?P<family>…)` if there is one, otherwise the first group, otherwise the whole matching text.
- If no rule matches, the automatic detection applies.
- An expression is not anchored: add `^` and `$` where needed.

Examples:

| Rule | Effect |
|---|---|
| `^(?P<family>logs-[a-z]+)-` | `logs-app-eu-2026.09.28` and `logs-app-us-2026.09.28` go into the family `logs-app` |
| `^shop-(\d+)-orders` | `shop-0042-orders-2026.09` goes into the family `0042` (first group) |
| `^tmp-` | every `tmp-…` index goes into the family `tmp-` (matching text) |

Before saving, click "Grouping preview": the table lists the families obtained with the rules as typed, the number of indices in each and up to three examples. The preview works on the current indices of the cluster: a full capture must already have taken place. Then click "Save".

Families are computed at display time with the rules in force: changing a rule also regroups the history.

## Node reference and acknowledgements

ES.Topo reports the **missing nodes**: those that should be there and are not.

- The reference is the set of nodes seen at least once during the **7 days** before the date being viewed.
- The "Declared node reference" field adds names to this reference. A declared node that was never seen therefore shows as missing: useful for an expected node that never joined the cluster.
- A node removed for good stops being counted as missing after 7 days. To avoid waiting, **acknowledge it**.

Acknowledging a node ("Acknowledged nodes (removed for good)" card, below the form):

1. Type the node name in "Node", a "Comment" if you wish, and the date "Removed from" (empty = now).
2. Click "Acknowledge".

The node is no longer counted as missing from that date. The action is written to the audit trail. "Revoke" cancels the acknowledgement.

## Life cycle of a cluster

| Action | Effect | Reversible |
|---|---|---|
| Untick "Collection enabled", then "Save" | The collection stops. The history can still be viewed. | Yes: tick it again. |
| "Remove from the configuration" | The collection stops and the cluster disappears from the administration. On the "Clusters" page it stays visible, marked "Removed from the configuration", until retention has deleted its history. | Yes: create a cluster with the same id, it gets its history back. |
| "Purge the history" | Deletes the whole history of the cluster, at once and for good. | **No.** |

**The order matters.** To delete a cluster completely:

1. untick "Collection enabled" and save (otherwise the next capture creates a history again);
2. click "Purge the history";
3. click "Remove from the configuration".

Once the cluster is removed, its form can no longer be reached, and neither can the purge button.

### Cluster suspended after an identity change

At the first capture, ES.Topo binds the configuration to the unique identifier of the cluster (`cluster_uuid`), shown at the top of the form: "Bound to cluster_uuid …". If, later, the URL answers with another `cluster_uuid` (virtual address repointed, cluster rebuilt), the collection is **suspended**: nothing is recorded, so that two clusters are not mixed in one history. The form shows "Collection suspended: cluster_uuid changed from … to …", and the "Clusters" page shows "Collection suspended".

Two possible answers, in the form of the cluster:

- **It is the same logical cluster, to be followed in the same history**: click "Test the connection" (the test reads the new identifier), then "Accept the new identity".
- **It is another cluster**: click "Handle as a new cluster" and type a new id. A new configuration takes over the same connection settings; the old one is disabled and keeps its history.

## Index settings whitelist

ES.Topo keeps the history of some index settings only, those that explain the allocation. The list is set for all clusters in **Administration › Index settings**, one name per line, the `*` wildcard being accepted. Default list:

```
index.number_of_shards
index.number_of_replicas
index.auto_expand_replicas
index.routing.allocation.*
index.routing.rebalance.enable
index.blocks.*
index.unassigned.node_left.delayed_timeout
index.priority
index.refresh_interval
index.translog.durability
index.lifecycle.name
index.version.created
index.creation_date
index.uuid
index.hidden
```

A name may only contain letters, digits, `.`, `_`, `-` and `*`. Settings absent from the list are neither read nor stored. The "Constraints" screen relies on `index.routing.allocation.*`, `index.number_of_replicas` and `index.auto_expand_replicas`: do not remove them. Cluster settings, for their part, are all kept.

## Diagnosing: the Collectors page

**Administration › Collectors** shows one line per cluster being collected:

| Column | Content |
|---|---|
| "Cluster" | Name and id; the reason of the suspension if any. |
| "Last probe" | Date, duration and status of the last light probe. |
| "Last full capture" | Date, duration, status and trigger (`first`, `health_changed`, `nodes_changed`, `shards_changed`, `cluster_settings_changed`, `manual`, `max_interval`). |
| "Next probe" | Planned date. |
| "Pending capture" | Trigger of a capture deferred by the minimum delay. |
| "Recent errors" | The last two errors. |

The ▼ button details each call to Elasticsearch of the last reading: name, HTTP status, duration and size of the response. This is where the real cost of the collection on a cluster is measured.

Statuses of a reading:

- `ok`: everything was read;
- `partial`: a call failed. The metrics obtained are kept, but nothing is concluded about the structure: no node, index or shard is declared gone;
- `failed`: the cluster did not answer `GET /` or `_cluster/health`. The period shows as "unknown" in the interface.

**A cluster that is saved but absent from this page** could not start its collection, most often because a secret or CA file cannot be read. The save succeeds all the same; the error is in the journal of the service (`journalctl -u estopo | grep "apply configuration"`). "Test the connection" gives the same error directly.

## What is called on Elasticsearch

Every request carries the headers `X-Opaque-Id: estopo` and `User-Agent: estopo/<version>`, which makes it recognisable in the logs and the task list of the cluster.

**Light probe** (every 5 minutes by default):

| Call | Purpose |
|---|---|
| `GET /` | name, version and `cluster_uuid` |
| `GET /_cluster/health` | health and shard counters |
| `GET /_nodes/stats/os,process,jvm,fs` (filtered) | node presence, CPU, heap, disk |
| `GET /_cat/allocation?format=json&bytes=b` | shards and disk per node |
| `GET /_cluster/settings?flat_settings=true` | cluster settings |
| `GET /_remote/info` | remote clusters (CCS) |

**Full capture**: the probe calls (the cluster settings then being read with `include_defaults=true`), plus:

| Call | Purpose |
|---|---|
| `GET /_nodes/os,jvm,http,transport` (filtered) | roles, attributes, versions, addresses |
| `GET /_cat/indices?format=json&bytes=b&expand_wildcards=all&h=…` | indices |
| `GET /_all/_settings/<whitelist>?flat_settings=true&expand_wildcards=all` | index settings |
| `GET /_cat/shards?format=json&bytes=b&h=…` | shard copies |

Load:

- the probe consists of six small requests;
- the full capture weighs mostly through `_cat/shards` and `_all/_settings`: on a cluster of 250,000 shard copies, the `_cat/shards` response is in the order of 60 MB;
- there is at most one full capture per cluster per minimum delay (10 minutes by default), and at most three full captures at the same time, all clusters together;
- the real cost on a large production cluster has not been measured: read it on the Collectors page and lengthen the delays if needed.

If the node answering `_remote/info` does not have the `remote_cluster_client` role, Elasticsearch returns a 400 error: ES.Topo treats it as "CCS information unavailable", not as a failure. If `_nodes` reports that some nodes did not answer, the capture is `partial`.

## Common errors

| Message (in "Failed: …", the Collectors page or the journal) | Cause and remedy |
|---|---|
| `x509: certificate signed by unknown authority` | The CA of the cluster is not known. Fill in the default CA or the one of the cluster. |
| `x509: certificate is valid for …, not …` | The name in the URL is not in the certificate of the cluster. Use a name the certificate holds. |
| `HTTP 401: security_exception: …` | Identity refused: revoked API key, wrong password, certificate not recognised by the realm. |
| `HTTP 403: security_exception: …` | Identity accepted but a privilege is missing. Check the role with "Test the connection". |
| `API key file: open …: permission denied` (or `password file`, `client key`…) | The file cannot be read by the user of the service. Check the owner and the permissions. |
| `… not an existing file of the secrets directory (ESTOPO_SECRETS_DIR)` | The path is outside the secrets directory, or the file does not exist. |
| `URL "…": basic authentication over plain http is refused …` | Password or API key on an `http://` URL. Use `https://`. |
| `client key … is encrypted but no passphrase file is configured` | Fill in "Passphrase file". |
| `unsupported key encryption … (only PBES2 is supported; re-encrypt with openssl pkcs8 -topk8 -v2 aes256)` | Key encrypted in an old format. Convert it with the given command. |
| `no CA certificate found in …` | The CA file or directory holds no PEM certificate. |
| `context deadline exceeded` or `Client.Timeout` | The cluster did not answer in time. Check the network; raise "Call timeout (s)" for a very large cluster. |
| `nodes: N of M nodes did not answer` | Some nodes did not answer `_nodes`: `partial` capture, retried at the next reading. |
| `at least one URL is required`, `invalid cluster id …` | Incomplete form or invalid id. |
| `this instance is standby: changes are made on the primary` | You are on the standby instance. Make the change on the primary. |
