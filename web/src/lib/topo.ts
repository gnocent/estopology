// Helpers shared by the views of the physical hierarchy (cluster → zone →
// tier → machine → node): paths, links, tooltips and sums.
import type { Group, NodeView } from './api';
import { t } from './i18n';
import { bytes, num, pct } from './format';
import { enc, href } from './router.svelte';

type Q = Record<string, string | number | undefined | null>;

/** Page of a group, named by the labels that lead to it; none = the cluster. */
export function groupHref(cluster: string, labels: string[], params: Q = {}): string {
  const base = `/c/${enc(cluster)}`;
  return href(labels.length ? `${base}/g/${labels.map(enc).join('/')}` : base, params);
}

export function nodeHref(cluster: string, name: string, params: Q = {}): string {
  return href(`/c/${enc(cluster)}/node/${enc(name)}`, params);
}

/** The groups met along the labels, or null when the path does not exist. */
export function findGroup(root: Group, labels: string[]): Group[] | null {
  const out: Group[] = [];
  let g = root;
  for (const lb of labels) {
    const next = (g.children ?? []).find((c) => c.label === lb);
    if (!next) return null;
    out.push(next);
    g = next;
  }
  return out;
}

/** Labels of the groups that hold the node at position i (zone, tier, machine). */
export function pathOfNode(root: Group, i: number): string[] {
  const out: string[] = [];
  let g = root;
  for (;;) {
    const next = (g.children ?? []).find((c) => (c.nodes ?? []).includes(i));
    if (!next) return out;
    out.push(next.label);
    g = next;
  }
}

/** Name of a kind of group: the awareness attribute, "tier" or "machine". */
export function kindName(g: { kind: string; attr?: string }): string {
  if (g.kind === 'attr') return g.attr || t('group.zone');
  if (g.kind === 'tier') return t('group.tier');
  if (g.kind === 'machine') return t('group.machineKind');
  return g.kind;
}

export interface Sums {
  nodes: number;
  present: number;
  missing: number;
  machines: number;
  shards: number;
  primaries: number;
  initializing: number;
  relocatingIn: number;
  relocatingOut: number;
  diskUsed: number;
  diskTotal: number;
  cpuAvg: number;
  cpuMax: number;
  heapAvg: number;
  heapMax: number;
  measured: number;
  aboveWatermark: number;
  versions: string[];
}

export function sums(nodes: NodeView[]): Sums {
  const s: Sums = {
    nodes: nodes.length, present: 0, missing: 0, machines: 0, shards: 0, primaries: 0, initializing: 0, relocatingIn: 0, relocatingOut: 0,
    diskUsed: 0, diskTotal: 0, cpuAvg: 0, cpuMax: 0, heapAvg: 0, heapMax: 0, measured: 0, aboveWatermark: 0, versions: [],
  };
  const machines = new Set<string>();
  const versions = new Set<string>();
  for (const n of nodes) {
    if (n.missing || !n.present) s.missing++;
    else s.present++;
    if (n.machine) machines.add(n.machine);
    if (n.version) versions.add(n.version);
    s.shards += n.shards;
    s.primaries += n.primaries;
    s.initializing += n.initializing;
    s.relocatingIn += n.relocating_in;
    s.relocatingOut += n.relocating_out;
    if (n.above_high_watermark) s.aboveWatermark++;
    const m = n.metrics;
    if (m) {
      s.measured++;
      s.diskUsed += m.disk_used;
      s.diskTotal += m.disk_total;
      s.cpuAvg += m.proc_cpu;
      s.cpuMax = Math.max(s.cpuMax, m.proc_cpu);
      s.heapAvg += m.heap_pct;
      s.heapMax = Math.max(s.heapMax, m.heap_pct);
    }
  }
  if (s.measured) {
    s.cpuAvg /= s.measured;
    s.heapAvg /= s.measured;
  }
  s.machines = machines.size;
  s.versions = [...versions].sort();
  return s;
}

const hiddenAttr = (k: string) => k.startsWith('xpack') || k.includes('config_version');

/** Tooltip of a node: every value with its label and unit. */
export function nodeTip(n: NodeView): string {
  const m = n.metrics;
  const lines = [n.name];
  if (n.missing || !n.present) lines.push(t('tile.missing'));
  lines.push(`${t('node.machine')} : ${n.machine || '—'}${n.ip ? ` · ${t('node.ip')} : ${n.ip}` : ''}`);
  if (n.roles?.length) lines.push(`${t('node.roles')} : ${n.roles.join(', ')}`);
  if (n.version) lines.push(`${t('node.version')} : ${n.version}`);
  const attrs = Object.entries(n.attrs ?? {}).filter(([k]) => !hiddenAttr(k));
  if (attrs.length) lines.push(`${t('node.attrs')} : ${attrs.map(([k, v]) => `${k}=${v}`).join(', ')}`);
  lines.push(t('tile.shards', { n: num(n.shards), p: num(n.primaries) }));
  if (n.relocating_in + n.relocating_out + n.initializing > 0)
    lines.push(t('tile.moving', { i: n.initializing, rin: n.relocating_in, rout: n.relocating_out }));
  if (m) {
    lines.push(`${t('node.disk')} : ${pct(n.disk_percent)} (${bytes(m.disk_used)} / ${bytes(m.disk_total)})`);
    lines.push(`${t('node.cpu')} : ${pct(m.proc_cpu)} · ${t('node.oscpu')} : ${pct(m.os_cpu)} · ${t('node.heap')} : ${pct(m.heap_pct)}`);
  }
  if (n.above_high_watermark) lines.push(t('tile.watermark'));
  lines.push(t('tile.open'));
  return lines.join('\n');
}

/** Tooltip of a group frame: what it holds, in a few lines. */
export function groupTip(g: Group, members: NodeView[]): string {
  const s = sums(members);
  const lines = [`${kindName(g)} ${g.label}`, t('group.count', { n: s.nodes, missing: s.missing })];
  if (g.kind !== 'machine' && s.machines) lines.push(t('group.machines', { n: s.machines }));
  lines.push(t('tile.shards', { n: num(s.shards), p: num(s.primaries) }));
  if (s.diskTotal) lines.push(`${t('node.disk')} : ${pct((100 * s.diskUsed) / s.diskTotal)} (${bytes(s.diskUsed)} / ${bytes(s.diskTotal)})`);
  if (s.measured) lines.push(`${t('node.cpu')} : ${t('group.avgMax', { avg: pct(s.cpuAvg), max: pct(s.cpuMax) })}`);
  lines.push(t('group.open'));
  return lines.join('\n');
}
