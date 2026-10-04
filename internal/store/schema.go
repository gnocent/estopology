package store

// Schemas are append-only lists of migration steps: never edit a released
// step, add a new one.

// metaSchema is meta.db (SPEC §6.2).
var metaSchema = []string{`
CREATE TABLE cluster (
  id            TEXT PRIMARY KEY,           -- stable id: spool and partition directories
  name          TEXT NOT NULL,
  config        TEXT NOT NULL,              -- JSON: URLs, auth, paths, timeouts (no secret values)
  enabled       INTEGER NOT NULL DEFAULT 1,
  suspended     INTEGER NOT NULL DEFAULT 0,
  suspend_reason TEXT,
  bound_uuid    TEXT,
  schedule      TEXT,                       -- JSON, NULL = defaults
  hierarchy     TEXT,                       -- JSON levels, NULL = defaults (SPEC §7.4)
  machine_attr  TEXT,                       -- node attribute naming the machine (SPEC §5.4)
  node_reference TEXT,                      -- JSON declared node list, optional
  created_ms    INTEGER NOT NULL,
  removed_ms    INTEGER                     -- removed from the configuration, history kept
);
CREATE TABLE setting (
  key   TEXT PRIMARY KEY,                   -- retention, whitelist, auth mode, ...
  value TEXT NOT NULL
);
CREATE TABLE family_rule (
  id         INTEGER PRIMARY KEY,
  cluster_id TEXT NOT NULL,
  position   INTEGER NOT NULL,
  pattern    TEXT NOT NULL,                 -- regex; the first capture group or ?P<family> names the family
  UNIQUE (cluster_id, position)
);
CREATE TABLE user (
  name        TEXT PRIMARY KEY,
  role        TEXT NOT NULL,                -- reader, admin
  password    TEXT,                         -- argon2id (local accounts)
  source      TEXT NOT NULL,                -- local, ldap
  prefs       TEXT,                         -- JSON: language, theme
  created_ms  INTEGER NOT NULL,
  disabled    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE session (
  token_hash  TEXT PRIMARY KEY,
  user_name   TEXT NOT NULL,
  csrf        TEXT NOT NULL,
  created_ms  INTEGER NOT NULL,
  expires_ms  INTEGER NOT NULL
);
CREATE TABLE node_ack (
  id         INTEGER PRIMARY KEY,
  cluster_id TEXT NOT NULL,
  node_name  TEXT NOT NULL,
  from_ms    INTEGER NOT NULL,              -- node considered removed from this date (SPEC §7.2)
  comment    TEXT,
  author     TEXT NOT NULL,
  created_ms INTEGER NOT NULL,
  revoked_ms INTEGER,
  revoked_by TEXT
);
CREATE INDEX node_ack_cluster ON node_ack (cluster_id, node_name);
CREATE TABLE partition (
  cluster_id TEXT NOT NULL,
  week       TEXT NOT NULL,                 -- 2026-W40
  from_ms    INTEGER NOT NULL,
  to_ms      INTEGER NOT NULL,              -- exclusive
  path       TEXT NOT NULL,                 -- relative to the data directory
  size_bytes INTEGER NOT NULL DEFAULT 0,
  sealed     INTEGER NOT NULL DEFAULT 0,
  created_ms INTEGER NOT NULL,
  PRIMARY KEY (cluster_id, week)
);
CREATE TABLE ha_state (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE audit (
  id       INTEGER PRIMARY KEY,
  ts       INTEGER NOT NULL,
  actor    TEXT NOT NULL,
  action   TEXT NOT NULL,
  target   TEXT,
  details  TEXT
);
CREATE INDEX audit_ts ON audit (ts);
CREATE TABLE alert (
  id         INTEGER PRIMARY KEY,
  ts         INTEGER NOT NULL,
  kind       TEXT NOT NULL,                 -- retention_size, cluster_suspended, ...
  cluster_id TEXT,
  message    TEXT NOT NULL,
  resolved_ms INTEGER
);
`}

// partitionSchema is one cluster × ISO week partition (SPEC §6.3). Times are
// UTC milliseconds; valid_to IS NULL means "still open at the end of what
// this partition covers".
var partitionSchema = []string{`
CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE capture (
  id          INTEGER PRIMARY KEY,
  ts          INTEGER NOT NULL,
  end_ts      INTEGER NOT NULL,
  kind        TEXT NOT NULL,                -- light, full, keyframe
  trigger     TEXT NOT NULL,
  status      TEXT NOT NULL,                -- ok, partial, failed
  duration_ms INTEGER NOT NULL,
  api_stats   TEXT,                         -- JSON per-call statistics
  error       TEXT
);
CREATE INDEX capture_ts ON capture (ts);
CREATE TABLE health (
  ts                 INTEGER PRIMARY KEY,
  status             TEXT NOT NULL,
  nodes              INTEGER, data_nodes INTEGER,
  active_primary     INTEGER, active INTEGER,
  relocating         INTEGER, initializing INTEGER,
  unassigned         INTEGER, unassigned_primary INTEGER,
  delayed_unassigned INTEGER, pending_tasks INTEGER
) WITHOUT ROWID;
CREATE TABLE node (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  es_id      TEXT NOT NULL,
  generation INTEGER NOT NULL,
  first_seen INTEGER NOT NULL,
  UNIQUE (name, es_id)
);
CREATE TABLE node_presence (
  node_id    INTEGER NOT NULL,
  valid_from INTEGER NOT NULL,
  valid_to   INTEGER
);
CREATE INDEX node_presence_node ON node_presence (node_id, valid_from);
CREATE INDEX node_presence_to ON node_presence (valid_to);
CREATE TABLE node_info (
  node_id     INTEGER NOT NULL,
  valid_from  INTEGER NOT NULL,
  valid_to    INTEGER,
  roles       TEXT, ip TEXT, transport TEXT, http_addr TEXT, host TEXT, machine TEXT,
  es_version  TEXT, os_name TEXT, os_kernel TEXT, os_arch TEXT, processors INTEGER,
  jvm_version TEXT, start_ms INTEGER,
  attrs_blob  TEXT
);
CREATE INDEX node_info_node ON node_info (node_id, valid_from);
CREATE INDEX node_info_to ON node_info (valid_to);
CREATE TABLE node_metric (
  node_id INTEGER NOT NULL, ts INTEGER NOT NULL,
  proc_cpu INTEGER, os_cpu INTEGER, load1 REAL, cgroup_cpu_ns INTEGER, heap_pct INTEGER,
  disk_total INTEGER, disk_used INTEGER, disk_avail INTEGER, disk_indices INTEGER, shards INTEGER,
  io_r_ops INTEGER, io_w_ops INTEGER, io_r_kb INTEGER, io_w_kb INTEGER,
  PRIMARY KEY (node_id, ts)
) WITHOUT ROWID;
CREATE TABLE idx (
  id              INTEGER PRIMARY KEY,
  uuid            TEXT NOT NULL UNIQUE,
  name            TEXT NOT NULL,
  family          TEXT NOT NULL,
  creation_date   INTEGER,
  version_created TEXT,
  hidden          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_name ON idx (name);
CREATE INDEX idx_family ON idx (family);
CREATE TABLE index_iv (
  index_id      INTEGER NOT NULL,
  valid_from    INTEGER NOT NULL,
  valid_to      INTEGER,
  status        TEXT NOT NULL,
  pri           INTEGER NOT NULL,
  rep           INTEGER NOT NULL,
  settings_blob TEXT
);
CREATE INDEX index_iv_index ON index_iv (index_id, valid_from);
CREATE INDEX index_iv_from ON index_iv (valid_from);
CREATE INDEX index_iv_to ON index_iv (valid_to);
CREATE TABLE index_metric (
  index_id INTEGER NOT NULL, ts INTEGER NOT NULL,
  docs INTEGER, store_bytes INTEGER, pri_store_bytes INTEGER,
  PRIMARY KEY (index_id, ts)
) WITHOUT ROWID;
CREATE TABLE shard_iv (
  id                   INTEGER PRIMARY KEY,
  index_id             INTEGER NOT NULL,
  shard                INTEGER NOT NULL,
  prirep               TEXT NOT NULL,       -- p, r
  node_id              INTEGER,             -- NULL when unassigned
  slot                 INTEGER NOT NULL DEFAULT 0, -- rank among unassigned copies of (index, shard, prirep)
  state                TEXT NOT NULL,
  reloc_target_node_id INTEGER,
  unassigned_reason    TEXT,
  unassigned_at        INTEGER,             -- exact date given by ES
  unassigned_details_blob TEXT,
  valid_from           INTEGER NOT NULL,
  valid_to             INTEGER
);
CREATE INDEX shard_iv_index ON shard_iv (index_id, shard);
CREATE INDEX shard_iv_node ON shard_iv (node_id, valid_from);
CREATE INDEX shard_iv_from ON shard_iv (valid_from);
CREATE INDEX shard_iv_to ON shard_iv (valid_to);
CREATE INDEX shard_iv_open ON shard_iv (index_id) WHERE valid_to IS NULL;
CREATE TABLE shard_metric (
  index_id INTEGER NOT NULL, shard INTEGER NOT NULL, prirep TEXT NOT NULL, node_id INTEGER NOT NULL,
  ts INTEGER NOT NULL, docs INTEGER, store_bytes INTEGER,
  PRIMARY KEY (index_id, shard, prirep, node_id, ts)
) WITHOUT ROWID;
CREATE TABLE cluster_settings_iv (
  valid_from INTEGER NOT NULL,
  valid_to   INTEGER,
  blob       TEXT NOT NULL
);
CREATE TABLE remote_iv (
  alias           TEXT NOT NULL,
  valid_from      INTEGER NOT NULL,
  valid_to        INTEGER,
  mode            TEXT,
  connected       INTEGER,
  nodes_connected INTEGER,
  blob            TEXT
);
CREATE INDEX remote_iv_alias ON remote_iv (alias, valid_from);
CREATE TABLE event (
  id           INTEGER PRIMARY KEY,
  ts_lo        INTEGER,                     -- last capture where the old state was seen
  ts_hi        INTEGER NOT NULL,            -- first capture where the new state is seen
  exact_ts     INTEGER,                     -- exact date given by ES, if any
  type         TEXT NOT NULL,
  severity     INTEGER NOT NULL,
  subject_kind TEXT NOT NULL,               -- cluster, node, machine, index, shard, remote, capture
  subject_ref  TEXT NOT NULL,               -- natural key: node name, index uuid, ...
  group_ref    TEXT,                        -- machine, node left, ... (grouping)
  payload      TEXT                         -- JSON
);
CREATE INDEX event_ts ON event (ts_hi);
CREATE INDEX event_type ON event (type, ts_hi);
CREATE INDEX event_subject ON event (subject_kind, subject_ref);
CREATE TABLE blob (
  hash TEXT PRIMARY KEY,
  data BLOB NOT NULL
) WITHOUT ROWID;
`}
