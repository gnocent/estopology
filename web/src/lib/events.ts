// Human description of journal events (type + payload).
import type { EsEvent } from './api';
import { tk } from './i18n';

function p(ev: EsEvent, k: string): string {
  const v = ev.payload?.[k];
  return v === undefined || v === null ? '' : String(v);
}

export function eventTitle(ev: EsEvent): string {
  const vars: Record<string, string | number> = {};
  if (ev.type === 'health_changed') {
    vars.from = p(ev, 'from');
    vars.to = p(ev, 'to');
  }
  if (ev.type === 'node_new_generation') vars.n = p(ev, 'generation');
  if (ev.type === 'node_name_duplicate') vars.n = p(ev, 'count');
  if (ev.type === 'machine_lost') vars.n = ((ev.payload?.nodes as unknown[]) ?? []).length;
  return tk(`event.${ev.type}`, vars, ev.type);
}

/** Short subject: node, index name, shard, remote. */
export function eventSubject(ev: EsEvent): string {
  switch (ev.subject_kind) {
    case 'shard': {
      const idx = p(ev, 'index');
      const shard = p(ev, 'shard');
      const pr = ev.payload?.primary ? 'p' : 'r';
      return `${idx}[${shard}]${pr}`;
    }
    case 'index':
      return p(ev, 'name') || ev.subject_ref;
    default:
      return ev.subject_ref;
  }
}

/** Details: from → to for moves, node for losses, changed keys. */
export function eventDetail(ev: EsEvent): string {
  switch (ev.type) {
    case 'shard_relocated':
      return `${p(ev, 'from')} → ${p(ev, 'to')}`;
    case 'shard_lost':
    case 'shard_promoted':
      return p(ev, 'node');
    case 'shard_assigned':
      return p(ev, 'node');
    case 'shard_unassigned':
      return p(ev, 'reason');
    case 'index_settings_changed':
    case 'cluster_settings_changed': {
      const ch = (ev.payload?.changes as { key: string; from?: string; to?: string }[]) ?? [];
      return ch.map((c) => `${c.key}: ${c.from ?? '∅'} → ${c.to ?? '∅'}`).join(' ; ');
    }
    case 'node_upgraded': {
      const from = ev.payload?.from as Record<string, string> | undefined;
      const to = ev.payload?.to as Record<string, string> | undefined;
      return from && to ? `${from.es} → ${to.es}` : '';
    }
    case 'machine_lost':
      return ((ev.payload?.nodes as string[]) ?? []).join(', ');
    case 'capture_failed':
      return p(ev, 'error');
    default:
      return '';
  }
}
