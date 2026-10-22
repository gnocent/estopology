// Typed client of the estopo REST API (internal/api). Types mirror the Go
// JSON structures; dates are epoch milliseconds (UTC).

export type HealthStatus = 'green' | 'yellow' | 'red';

export interface Health {
  status: HealthStatus;
  nodes: number;
  data_nodes: number;
  active_primary: number;
  active: number;
  relocating: number;
  initializing: number;
  unassigned: number;
  unassigned_primary: number;
  delayed_unassigned: number;
  pending_tasks: number;
}

export interface Concern {
  code: string;
  level: number;
  count: number;
}

export interface Assessment {
  level: number; // 1..6, 0 healthy, -1 unknown
  concerns: Concern[] | null;
}

export interface Counts {
  nodes: number;
  nodes_present: number;
  data_nodes: number;
  indices: number;
  families: number;
  shards: number;
  primaries: number;
  unassigned: number;
  unassigned_primaries: number;
  relocating: number;
  initializing: number;
}

export interface Bucket {
  from: number;
  to: number;
  status?: HealthStatus | '';
  unknown: boolean;
  events: number;
  severe: number;
}

export interface EsEvent {
  ts_lo?: number;
  ts_hi: number;
  exact_ts?: number;
  type: string;
  severity: number;
  subject_kind: string;
  subject_ref: string;
  group_ref?: string;
  payload?: Record<string, unknown>;
}

export interface ClusterSummary {
  id: string;
  name: string;
  enabled: boolean;
  suspended: boolean;
  suspend_reason?: string;
  removed: boolean;
  last_capture_ts: number;
  health?: Health;
  assessment?: Assessment;
  counts?: Counts;
  versions: string[];
  missing_nodes: string[];
  timeline: Bucket[];
  recent_events: EsEvent[];
  error?: string;
}

export interface NodeMetrics {
  ts: number;
  proc_cpu: number;
  os_cpu: number;
  load1: number;
  heap_pct: number;
  disk_total: number;
  disk_used: number;
  disk_avail: number;
  disk_indices: number;
  shards: number;
}

export interface NodeView {
  name: string;
  es_id: string;
  generation: number;
  present: boolean;
  roles: string[] | null;
  tiers: string[] | null;
  ip: string;
  host: string;
  machine: string;
  version: string;
  os: string;
  kernel: string;
  jvm: string;
  start_ms: number;
  attrs: Record<string, string> | null;
  metrics?: NodeMetrics;
  missing: boolean;
  shards: number;
  primaries: number;
  initializing: number;
  relocating_out: number;
  relocating_in: number;
  disk_percent: number;
  above_high_watermark: boolean;
}

export interface Group {
  kind: string;
  attr?: string;
  label: string;
  children?: Group[];
  nodes: number[] | null;
}

export interface Remote {
  alias: string;
  mode: string;
  connected: boolean;
  nodes_connected: number;
  since: number;
}

export interface StateView {
  cluster: string;
  name: string;
  at: number;
  capture_ts: number;
  probe_ts: number;
  unknown: boolean;
  health?: Health;
  assessment: Assessment;
  missing_nodes: string[];
  counts: Counts;
  versions: string[] | null;
  nodes: NodeView[];
  hierarchy: Group;
  remotes: Remote[];
  cluster_settings: Record<string, string>;
}

export interface Matrix {
  families: string[];
  /** Rows are indices: their UUIDs, in the order of families. */
  uuids?: string[];
  total: number;
  nodes: string[];
  counts: number[][];
  primaries: number[][];
  unassigned: number[];
}

export interface Page<T> {
  total: number;
  offset: number;
  items: T[];
}

export interface ShardView {
  uuid: string;
  index: string;
  family: string;
  shard: number;
  primary: boolean;
  node?: string;
  machine?: string;
  state: string;
  reloc_target?: string;
  reason?: string;
  unassigned_at?: number;
  since: number;
  docs: number;
  store_bytes: number;
}

export interface IndexView {
  uuid: string;
  name: string;
  family: string;
  status: string;
  pri: number;
  rep: number;
  creation_ms: number;
  hidden: boolean;
  system: boolean;
  settings: Record<string, string> | null;
  docs: number;
  store_bytes: number;
  pri_store_bytes: number;
  health: HealthStatus;
  shards: number;
  unassigned: number;
  tier: string;
}

export interface FamilyView {
  family: string;
  indices: number;
  shards: number;
  primaries: number;
  unassigned: number;
  store_bytes: number;
  docs: number;
  tiers: string[] | null;
  newest: string;
  system: boolean;
}

export interface Interval {
  from: number;
  to?: number;
}

export interface Generation {
  es_id: string;
  generation: number;
  first_seen: number;
  presence: Interval[] | null;
}

export interface NodeInfoPeriod extends Interval {
  es_id: string;
  roles: string;
  ip: string;
  machine: string;
  version: string;
  os: string;
  kernel: string;
  jvm: string;
  start_ms: number;
  attrs?: Record<string, string>;
}

export interface MetricPoint {
  ts: number;
  proc_cpu: number;
  os_cpu: number;
  load1: number;
  heap_pct: number;
  disk_used: number;
  disk_total: number;
  shards: number;
}

/** One group of the hierarchy: its path, its members and what they hold. */
export interface GroupDetail {
  path: { kind: string; attr?: string; label: string }[];
  nodes: string[];
  machines: string[];
  families: { family: string; copies: number; primaries: number }[];
}

/** Size of a family at one date. */
export interface FamilyPoint {
  ts: number;
  docs: number;
  store_bytes: number;
  pri_store_bytes: number;
  indices: number;
}

/** Metrics of the nodes of a group around one date: means for the percentages, sums for sizes and shards. */
export interface GroupMetricPoint {
  ts: number;
  nodes: number;
  proc_cpu: number;
  os_cpu: number;
  load1: number;
  heap_pct: number;
  disk_used: number;
  disk_total: number;
  shards: number;
}

export interface NodeDetail {
  history: {
    name: string;
    generations: Generation[] | null;
    infos: NodeInfoPeriod[] | null;
    metrics: MetricPoint[] | null;
  };
  node?: NodeView;
  families: { family: string; copies: number; primaries: number }[];
}

export interface IndexPeriod extends Interval {
  status: string;
  pri: number;
  rep: number;
  settings: Record<string, string> | null;
}

export interface IndexDetail {
  history: {
    uuid: string;
    name: string;
    family: string;
    creation_ms: number;
    periods: IndexPeriod[] | null;
    sizes: { ts: number; docs: number; store_bytes: number }[] | null;
  };
  index?: IndexView;
  shards: ShardView[];
  nodes: string[];
}

export interface Hit {
  cluster: string;
  kind: 'node' | 'ip' | 'machine' | 'index' | 'uuid' | 'family' | 'shard'; // shard: built by the UI ("shard N de X à T")
  shard?: number;
  at?: number;
  value: string;
  label?: string;
}

export interface Meta {
  version: string;
  role: string;
  now: number;
  user: { name: string; role: string };
  auth_mode: string;
  event_types: string[];
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

type Params = Record<string, string | number | boolean | string[] | undefined | null>;

export function query(params: Params): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (Array.isArray(v)) v.forEach((x) => q.append(k, x));
    else if (v !== undefined && v !== null && v !== '') q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

// Called on 401 (session expired): the app shows the login screen.
let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(f: () => void) {
  onUnauthorized = f;
}

// CSRF token of the session, sent with every state-changing request.
let csrfToken = '';
export function setCSRF(token: string) {
  csrfToken = token;
}

// System indices are hidden by default (SPEC §5.5); the UI toggle (URL
// parameter system=1) shows them in every view.
let showSystem = () => false;
export function setShowSystem(f: () => boolean) {
  showSystem = f;
}

export async function get<T>(path: string, params: Params = {}, signal?: AbortSignal): Promise<T> {
  if (showSystem() && params.hide_system === undefined) params = { ...params, hide_system: false };
  const res = await fetch(`/api/v1${path}${query(params)}`, {
    signal,
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) {
    if (res.status === 401) onUnauthorized();
    let code = 'http', msg = res.statusText;
    try {
      const body = await res.json();
      code = body.code ?? code;
      msg = body.error ?? msg;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, code, msg);
  }
  return (await res.json()) as T;
}

const enc = encodeURIComponent;

export const api = {
  meta: (s?: AbortSignal) => get<Meta>('/meta', {}, s),
  clusters: (s?: AbortSignal) => get<ClusterSummary[]>('/clusters', {}, s),
  search: (q: string, cluster?: string, s?: AbortSignal) => get<Hit[]>('/search', { q, cluster }, s),
  state: (c: string, at?: number, s?: AbortSignal) => get<StateView>(`/clusters/${enc(c)}/state`, { at }, s),
  timeline: (c: string, from: number, to: number, buckets: number, s?: AbortSignal) =>
    get<Bucket[]>(`/clusters/${enc(c)}/timeline`, { from, to, buckets }, s),
  matrix: (c: string, at?: number, top = 30, q = '', rows = '', s?: AbortSignal) =>
    get<Matrix>(`/clusters/${enc(c)}/matrix`, { at, top, q, rows }, s),
  shards: (c: string, p: Params, s?: AbortSignal) => get<Page<ShardView>>(`/clusters/${enc(c)}/shards`, p, s),
  indices: (c: string, p: Params, s?: AbortSignal) => get<Page<IndexView>>(`/clusters/${enc(c)}/indices`, p, s),
  families: (c: string, p: Params, s?: AbortSignal) => get<Page<FamilyView>>(`/clusters/${enc(c)}/families`, p, s),
  node: (c: string, name: string, p: Params, s?: AbortSignal) =>
    get<NodeDetail>(`/clusters/${enc(c)}/nodes/${enc(name)}`, p, s),
  familyHistory: (c: string, family: string, from: number, to: number, points: number, s?: AbortSignal) =>
    get<FamilyPoint[]>(`/clusters/${enc(c)}/families/${enc(family)}/history`, { from, to, points }, s),
  indexHistory: (c: string, uuid: string, from: number, to: number, points: number, s?: AbortSignal) =>
    get<FamilyPoint[]>(`/clusters/${enc(c)}/indices/${enc(uuid)}/history`, { from, to, points }, s),
  groupHistory: (c: string, labels: string[], at: number | undefined, from: number, to: number, points: number, s?: AbortSignal) =>
    get<GroupMetricPoint[]>(`/clusters/${enc(c)}/group/history`, { g: labels, at, from, to, points }, s),
  group: (c: string, labels: string[], at?: number, s?: AbortSignal) => get<GroupDetail>(`/clusters/${enc(c)}/group`, { g: labels, at }, s),
  groupEvents: (c: string, labels: string[], from: number, to: number, limit: number, s?: AbortSignal) =>
    get<{ events: EsEvent[] | null }>(`/clusters/${enc(c)}/events`, { g: labels, from, to, limit }, s),
  lives: (c: string, name: string, s?: AbortSignal) => get<Hit[] | null>(`/clusters/${enc(c)}/lives`, { name }, s),
  index: (c: string, uuid: string, p: Params, s?: AbortSignal) =>
    get<IndexDetail>(`/clusters/${enc(c)}/indices/${enc(uuid)}`, p, s),
  events: (c: string, p: Params, s?: AbortSignal) =>
    get<{ events: EsEvent[]; next_ts?: number; next_key?: string }>(`/clusters/${enc(c)}/events`, p, s),
};

export function exportURL(cluster: string, what: string, params: Params): string {
  if (showSystem() && params.hide_system === undefined) params = { ...params, hide_system: false };
  return `/api/v1/clusters/${enc(cluster)}/export/${enc(what)}${query(params)}`;
}

// --- analysis screens (plan step 10) -----------------------------------------------

export interface SettingDiff {
  key: string;
  from: string;
  to: string;
}

export interface NodeDiff {
  name: string;
  es_id: string;
  change: 'added' | 'removed' | 'changed';
  machine: string;
  changes?: SettingDiff[];
}

export interface IndexDiff {
  uuid: string;
  name: string;
  family: string;
  change: 'added' | 'removed' | 'changed';
  changes?: SettingDiff[];
}

export interface ShardDiff {
  uuid: string;
  index: string;
  family: string;
  shard: number;
  primary: boolean;
  change: 'moved' | 'lost' | 'new' | 'promoted'; // promoted: replica become primary on its node (from = to)
  from: string[] | null;
  to: string[] | null;
}

export interface RemoteDiff {
  alias: string;
  from: string;
  to: string;
}

export interface DiffSummary {
  nodes_added: number;
  nodes_removed: number;
  nodes_changed: number;
  indices_added: number;
  indices_removed: number;
  indices_changed: number;
  shards_moved: number;
  shards_lost: number;
  shards_new: number;
  shards_promoted: number;
  settings_changed: number;
  cluster_settings_changed: number;
  remotes_changed: number;
}

export interface Diff {
  a: number;
  b: number;
  capture_a: number;
  capture_b: number;
  summary: DiffSummary;
  nodes: NodeDiff[] | null;
  indices: IndexDiff[] | null;
  shards: ShardDiff[] | null;
  cluster_settings: SettingDiff[] | null;
  remotes: RemoteDiff[] | null;
}

export interface DiffResponse {
  diff: Diff;
  totals: { Nodes: number; Indices: number; Shards: number; Settings: number; ClusterSettings: number; Remotes: number };
}

export interface IndexSettingChange {
  uuid: string;
  index: string;
  family: string;
  key: string;
  from: string;
  to: string;
}

export interface Segment {
  index: string;
  uuid: string;
  shard: number;
  primary: boolean;
  node?: string;
  slot?: number;
  state: string;
  reloc_target?: string;
  reason?: string;
  from: number;
  to?: number;
}

export interface PathResult {
  segments: Segment[] | null;
  /** More segments than the server limit (20 000): only the first ones are kept. */
  truncated?: boolean;
}

export interface LostCopy {
  uuid: string;
  index: string;
  shard: number;
  primary: boolean;
  node: string;
  zone?: string;
  size_bytes: number;
  ts_lo?: number;
  ts_hi: number;
  after: 'promoted' | 'reallocated' | 'unassigned' | 'deleted'; // deleted: after_ts = deletion of the index
  after_node?: string;
  after_ts?: number;
  after_reason?: string;
  promoted: boolean;
}

export interface LossGroup {
  kind: 'node' | 'machine';
  name: string;
  nodes: string[] | null;
  left_lo?: number;
  left_hi: number;
  copies: LostCopy[];
  more?: number; // left out of the response (see the export)
}

export interface Losses {
  a: number;
  b: number;
  /** The window holds more events than the server reads at once. */
  truncated?: boolean;
  summary: {
    lost: number;
    primaries_lost: number;
    not_promoted: number;
    promoted: number;
    reallocated: number;
    still_unassigned: number;
    deleted: number;
    deleted_indices: number;
  };
  groups: LossGroup[] | null;
  deleted: { uuid: string; name: string; family: string; ts: number }[] | null;
  deleted_more?: number;
}

export interface Flows {
  level: string;
  links: { from: string; to: string; count: number }[];
  histogram: { from: number; to: number; count: number }[];
  events?: EsEvent[];
}

export interface ConstraintView {
  index: number;
  status: 'infeasible' | 'tight' | 'ok';
  reason?: string;
  params?: Record<string, string>;
  eligible: number;
  required: number;
  uuid: string;
  name: string;
  family: string;
}

export const api2 = {
  diff: (c: string, a: number, b: number, s?: AbortSignal) => get<DiffResponse>(`/clusters/${enc(c)}/diff`, { a, b }, s),
  diffSection: <T>(c: string, a: number, b: number, section: string, offset: number, limit: number, s?: AbortSignal) =>
    get<Page<T>>(`/clusters/${enc(c)}/diff`, { a, b, section, offset, limit }, s),
  path: (c: string, p: Params, s?: AbortSignal) => get<PathResult>(`/clusters/${enc(c)}/path`, p, s),
  losses: (c: string, a: number, b: number, s?: AbortSignal) => get<Losses>(`/clusters/${enc(c)}/losses`, { a, b }, s),
  flows: (c: string, p: Params, s?: AbortSignal) => get<Flows>(`/clusters/${enc(c)}/flows`, p, s),
  constraints: (c: string, p: Params, s?: AbortSignal) =>
    get<{ counts: Record<string, number>; page: Page<ConstraintView> }>(`/clusters/${enc(c)}/constraints`, p, s),
};

/** Sends a state-changing request (PUT, POST, DELETE) with the CSRF token; the server refuses those without a JSON content type or the X-Requested-With header. */
export async function send<T>(method: 'PUT' | 'POST' | 'DELETE', path: string, body?: unknown, params: Params = {}): Promise<T> {
  const res = await fetch(`/api/v1${path}${query(params)}`, {
    method,
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-Requested-With': 'estopo', 'X-CSRF-Token': csrfToken },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    if (res.status === 401) onUnauthorized();
    let code = 'http', msg = res.statusText;
    try {
      const b = await res.json();
      code = b.code ?? code;
      msg = b.error ?? msg;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, code, msg);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export interface SessionInfo {
  mode: 'none' | 'local' | 'ldap';
  authenticated: boolean;
  user?: string;
  role?: 'reader' | 'admin';
  csrf?: string;
  instance?: 'primary' | 'standby' | 'fenced';
  insecure?: boolean;
  prefs?: { lang: 'fr' | 'en'; theme: 'light' | 'dark' | 'auto' };
}

export const sessionApi = {
  get: () => get<SessionInfo>('/session'),
  login: (name: string, password: string) => send<SessionInfo>('POST', '/login', { name, password }),
  logout: () => send<void>('POST', '/logout'),
  changePassword: (old: string, nw: string) => send<void>('PUT', '/me/password', { old, new: nw }),
  savePrefs: (lang: string, theme: string) => send<void>('PUT', '/me/prefs', { lang, theme }),
};

// --- administration (SPEC §9) -------------------------------------------------------

export interface EsConfig {
  URLs: string[];
  Auth: 'cert' | 'apikey' | 'basic' | 'none';
  CertFile?: string;
  KeyFile?: string;
  KeyPassphraseFile?: string;
  APIKeyFile?: string;
  Username?: string;
  PasswordFile?: string;
  CAPath?: string;
  UseSystemCAs?: boolean;
  Timeout?: number; // nanoseconds
  ConnectTimeout?: number;
}

export interface ClusterConfig {
  id: string;
  name: string;
  es: EsConfig;
  enabled: boolean;
  suspended?: boolean;
  suspend_reason?: string;
  bound_uuid?: string;
  schedule?: Schedule | null;
  hierarchy?: { kind: string; attr?: string }[] | null;
  machine_attr?: string;
  node_reference?: string[] | null;
  created_ms?: number;
  removed_ms?: number;
}

export interface CallStat {
  name: string;
  path: string;
  url: string;
  status: number;
  duration_ms: number;
  wire_bytes: number;
  bytes: number;
  gzip: boolean;
  error?: string;
}

export interface CaptureStatus {
  start_ms: number;
  duration_ms: number;
  status: string;
  trigger: string;
  error?: string;
  calls: CallStat[] | null;
}

export interface CollectorStatus {
  id: string;
  name: string;
  suspended: boolean;
  suspend_reason?: string;
  last_probe?: CaptureStatus;
  last_full?: CaptureStatus;
  next_probe_ms: number;
  pending_trigger?: string;
  recent_errors?: string[];
  history_until_ms?: number; // set when the history is ahead of the clock (SPEC §6.7)
  history_ahead_ms?: number;
}

export interface ConnectionTest {
  ok: boolean;
  error?: string;
  cluster_name?: string;
  cluster_uuid?: string;
  version?: string;
  health?: string;
  nodes?: number;
  missing_privileges?: string[];
  bound_uuid?: string;
  uuid_changed: boolean;
  calls: CallStat[];
}

export interface Storage {
  clusters: {
    cluster: string;
    partitions: number;
    bytes: number;
    days: number;
    bytes_per_day: number;
    projection_30d: number;
    projection_1y: number;
  }[];
  partitions: { Cluster: string; Week: string; FromMs: number; ToMs: number; SizeBytes: number; Sealed: boolean }[] | null;
  meta_bytes: number;
  total_bytes: number;
  retention: { max_age_days: number; max_bytes: number };
  alerts: { id: number; ts: number; kind: string; cluster?: string; message: string }[] | null;
}

export interface UserInfo {
  name: string;
  role: 'reader' | 'admin';
  source: string;
  created_ms: number;
  disabled: boolean;
}

export interface Ack {
  id: number;
  cluster: string;
  node: string;
  from_ms: number;
  comment?: string;
  author: string;
  created_ms: number;
  revoked_ms?: number;
  revoked_by?: string;
}

export const adminApi = {
  clusters: () => get<ClusterConfig[]>('/admin/clusters'),
  saveCluster: (c: ClusterConfig) => send<ClusterConfig>('PUT', `/admin/clusters/${enc(c.id)}`, c),
  removeCluster: (id: string) => send<void>('DELETE', `/admin/clusters/${enc(id)}`),
  test: (id: string, es?: EsConfig) => send<ConnectionTest>('POST', `/admin/clusters/${enc(id)}/test`, es),
  capture: (id: string, force: boolean) => send<void>('POST', `/admin/clusters/${enc(id)}/capture`, undefined, { force }),
  resume: (id: string, accept: string) => send<void>('POST', `/admin/clusters/${enc(id)}/resume`, undefined, { accept }),
  reattach: (id: string, newID: string, name: string) =>
    send<ClusterConfig>('POST', `/admin/clusters/${enc(id)}/reattach`, { new_id: newID, name }),
  purge: (id: string) => send<{ partitions_deleted: number }>('POST', `/admin/clusters/${enc(id)}/purge`),
  collectors: () => get<CollectorStatus[]>('/admin/collectors'),
  retention: () => get<{ max_age_days: number; max_bytes: number }>('/admin/retention'),
  saveRetention: (r: { max_age_days: number; max_bytes: number }) => send<void>('PUT', '/admin/retention', r),
  storage: () => get<Storage>('/admin/storage'),
  whitelist: () => get<string[]>('/admin/settings-whitelist'),
  saveWhitelist: (w: string[]) => send<string[]>('PUT', '/admin/settings-whitelist', w),
  auth: () => get<AuthConfig>('/admin/auth'),
  saveAuth: (a: AuthConfig) => send<void>('PUT', '/admin/auth', a),
  ldapTest: (ldap: LDAPConfig, user: string, password: string) =>
    send<LDAPTest>('POST', '/admin/auth/ldap-test', { ldap, user, password }),
  users: () => get<UserInfo[]>('/admin/users'),
  saveUser: (name: string, u: { role: string; password?: string; disabled: boolean }) => send<void>('PUT', `/admin/users/${enc(name)}`, u),
  deleteUser: (name: string) => send<void>('DELETE', `/admin/users/${enc(name)}`),
  acks: (id: string) => get<Ack[]>(`/admin/clusters/${enc(id)}/acks`),
  addAck: (id: string, a: { node: string; from_ms?: number; comment?: string }) => send<{ id: number }>('POST', `/admin/clusters/${enc(id)}/acks`, a),
  revokeAck: (id: string, ack: number) => send<void>('DELETE', `/admin/clusters/${enc(id)}/acks/${ack}`),
  familyRules: (id: string) => get<string[]>(`/admin/clusters/${enc(id)}/family-rules`),
  saveFamilyRules: (id: string, rules: string[]) => send<string[]>('PUT', `/admin/clusters/${enc(id)}/family-rules`, rules),
  familyPreview: (id: string, rules: string[]) =>
    send<Page<{ family: string; indices: number; examples: string[] }>>('POST', `/admin/clusters/${enc(id)}/family-preview`, rules, { limit: 50 }),
  audit: () => get<{ ts: number; actor: string; action: string; target?: string; details?: string }[]>('/admin/audit'),
  ha: () => get<HAStatus>('/admin/ha'),
  metrics: () => get<{ enabled: boolean }>('/admin/metrics'),
  tls: () => get<{ ca_path: string }>('/admin/tls'),
  saveTLS: (ca_path: string) => send<{ ca_path: string; certificates: number }>('PUT', '/admin/tls', { ca_path }),
  collectorSettings: () => get<{ parallelism: number }>('/admin/collector'),
  saveCollectorSettings: (parallelism: number) => send<{ parallelism: number }>('PUT', '/admin/collector', { parallelism }),
  cache: () => get<{ structures: number }>('/admin/cache'),
  saveCache: (structures: number) => send<{ structures: number }>('PUT', '/admin/cache', { structures }),
  saveMetrics: (enabled: boolean) => send<{ enabled: boolean }>('PUT', '/admin/metrics', { enabled }),
  haAction: (action: 'promote' | 'demote' | 'rebuild' | 'resume' | 'peer', url?: string) =>
    send<HAStatus>('POST', `/admin/ha/${action}`, url === undefined ? undefined : { url }),
  system: () =>
    get<{ version: string; commit: string; go_version: string; role: string; uptime_s: number; heap_bytes: number; sys_bytes: number; goroutines: number; data_dir: string; auth_mode: string }>(
      '/admin/system',
    ),
};

export interface LDAPConfig {
  urls: string[];
  start_tls?: boolean;
  ca_file?: string;
  server_name?: string;
  bind_dn: string;
  bind_password_file: string;
  user_base: string;
  user_filter?: string;
  user_attr?: string;
  group_base: string;
  group_filter?: string;
  admin_groups: string[];
  reader_groups: string[];
  timeout_s?: number;
}

export interface AuthConfig {
  mode: string;
  session_hours: number;
  ldap?: LDAPConfig;
}

export interface LDAPTest {
  ok: boolean;
  error?: string;
  steps: { step: string; ok: boolean; detail?: string }[];
  result?: { server: string; user_dn: string; groups: string[]; role: 'reader' | 'admin' };
}

export interface HAStatus {
  role: 'primary' | 'standby' | 'fenced';
  standby_of?: string;
  peer?: string;
  promoted_ms?: number;
  fenced_reason?: string;
  token_configured: boolean;
  /** Random id of this instance, kept in its data directory. */
  instance_id?: string;
  replication?: {
    /** Id of the primary this standby follows. */
    primary_id?: string;
    /** Why replication refuses to go on (another primary, emptied primary). */
    blocked?: string;
    last_sync_ms?: number;
    last_error?: string;
    last_error_ms?: number;
    lag_ms: number;
    rebuilding?: boolean;
    clusters: { cluster: string; cursor_ms: number; primary_ms: number; records: number; rebuilds: number }[];
  };
  peer_check?: { last_ms?: number; peer_role?: string; last_error?: string };
}

/** Collection schedule of a cluster (durations in nanoseconds, Go time.Duration). */
export interface Schedule {
  ProbeEvery: number;
  MinInterval: number;
  MaxInterval: number;
  Jitter: number;
  ShardsThreshold: number;
}
