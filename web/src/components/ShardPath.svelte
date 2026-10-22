<script lang="ts">
  import { tip } from '../lib/tip';
  // Path of shard copies between two dates (SPEC §7.7, §8.2.4, §8.2.6): one
  // lane per copy, one bar per interval, coloured by node; the round trips a
  // simple A/B diff cannot see (B6) show as successive bars.
  import { api2, type Segment } from '../lib/api';
  import { t } from '../lib/i18n';
  import { dateTime, utc } from '../lib/format';
  import Intervals from './Intervals.svelte';

  let {
    cluster,
    uuid,
    family,
    shard,
    from,
    to,
    maxLanes = 120,
  }: { cluster: string; uuid?: string; family?: string; shard?: number; from: number; to: number; maxLanes?: number } = $props();

  let segs = $state<Segment[] | null>(null);
  let truncated = $state(false);
  let error = $state('');
  let details = $state(false);

  $effect(() => {
    const ctrl = new AbortController();
    segs = null;
    api2.path(cluster, { uuid, family, shard, a: from, b: to }, ctrl.signal).then(
      (r) => ((segs = r.segments ?? []), (truncated = !!r.truncated), (error = '')),
      (e) => (e as Error).name !== 'AbortError' && (error = (e as Error).message),
    );
    return () => ctrl.abort();
  });

  const palette = ['#3b82f6', '#8b5cf6', '#14b8a6', '#f59e0b', '#ec4899', '#84cc16', '#06b6d4', '#f97316', '#6366f1', '#10b981'];
  function nodeColor(node?: string): string {
    if (!node) return 'var(--red)'; // unassigned
    let h = 0;
    for (const c of node) h = (h * 31 + c.charCodeAt(0)) >>> 0;
    return palette[h % palette.length];
  }

  // Lanes: copies of the same (index, shard, primary/replica); intervals that
  // overlap (relocation source and target, several replicas) get their own lane.
  const lanes = $derived.by(() => {
    const groups = new Map<string, Segment[]>();
    for (const s of segs ?? []) {
      const k = `${s.index}[${s.shard}] ${s.primary ? 'P' : 'R'}`;
      if (!groups.has(k)) groups.set(k, []);
      groups.get(k)!.push(s);
    }
    const rows: { label: string; items: { from: number; to?: number; title: string; color: string }[] }[] = [];
    for (const [k, list] of [...groups.entries()].sort((a, b) => a[0].localeCompare(b[0], undefined, { numeric: true }))) {
      list.sort((a, b) => a.from - b.from);
      const ends: number[] = [];
      const byLane: Segment[][] = [];
      for (const s of list) {
        let i = ends.findIndex((e) => e <= s.from);
        if (i < 0) {
          i = ends.length;
          ends.push(0);
          byLane.push([]);
        }
        ends[i] = s.to ?? Number.MAX_SAFE_INTEGER;
        byLane[i].push(s);
      }
      byLane.forEach((l, i) =>
        rows.push({
          label: i === 0 ? k : `${k} #${i + 1}`,
          items: l.map((s) => ({
            from: s.from,
            to: s.to,
            color: nodeColor(s.node),
            title: [
              `${t('col.node')} : ${s.node ?? t('path.unassigned')}`,
              `${t('col.state')} : ${s.state}${s.reloc_target ? ' → ' + s.reloc_target : ''}${s.reason ? ' (' + s.reason + ')' : ''}`,
              `${dateTime(s.from)} → ${s.to ? dateTime(s.to) : t('node.open')}`,
              `${t('path.duration')} : ${span((s.to ?? to) - s.from)}`,
            ].join('\n'),
          })),
        }),
      );
    }
    return rows;
  });
  const shown = $derived(lanes.slice(0, maxLanes));
  // Legend: the nodes met, in their colour (the first ones when many).
  const nodeNames = $derived([...new Set((segs ?? []).map((s) => s.node).filter((n): n is string => !!n))].sort());

  // Duration in a readable unit.
  function span(ms: number): string {
    const m = Math.max(0, Math.round(ms / 60000));
    if (m < 120) return `${m} min`;
    const h = m / 60;
    return h < 48 ? `${h.toFixed(1)} h` : `${(h / 24).toFixed(1)} ${t('unit.days')}`;
  }
</script>

{#if error}<p class="bad">{t('common.error', { msg: error })}</p>
{:else if !segs}<p class="muted loading">{t('common.loading')}</p>
{:else if segs.length === 0}<p class="muted">{t('path.none')}</p>
{:else}
  {#if truncated}<div class="banner warn">{t('path.truncated')}</div>{/if}
  <div class="legend">
    {#each nodeNames.slice(0, 12) as n (n)}<span><span class="sw" style:background={nodeColor(n)}></span>{n}</span>{/each}
    {#if nodeNames.length > 12}<span>{t('path.moreNodes', { n: nodeNames.length - 12 })}</span>{/if}
    <span><span class="sw" style:background="var(--red)"></span>{t('path.unassigned')}</span>
  </div>
  <Intervals rows={shown} {from} {to} />
  {#if lanes.length > shown.length}<p class="muted small">{t('path.moreLanes', { n: lanes.length - shown.length })}</p>{/if}
  <button class="link" onclick={() => (details = !details)}>{details ? t('path.hideDetails') : t('path.showDetails')}</button>
  {#if details}
    <table class="data">
      <thead><tr><th>{t('path.copy')}</th><th>{t('col.node')}</th><th>{t('col.state')}</th><th>{t('col.from')}</th><th>{t('col.to')}</th><th class="num">{t('path.duration')}</th></tr></thead>
      <tbody>
        {#each segs.slice(0, 1000) as s, i (i)}
          <tr>
            <td class="mono">{s.index}[{s.shard}] {s.primary ? t('prirep.primary') : t('prirep.replica')}</td>
            <td>{s.node ?? t('col.unassigned')}{#if s.reloc_target} → {s.reloc_target}{/if}</td>
            <td>{s.state}{#if s.reason} <span class="muted">({s.reason})</span>{/if}</td>
            <td use:tip={utc(s.from)}>{dateTime(s.from)}</td>
            <td>{s.to ? dateTime(s.to) : t('node.open')}</td>
            <td class="num">{span((s.to ?? to) - s.from)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
{/if}

<style>
  .link {
    border: none;
    background: none;
    color: var(--accent);
    padding: 0.3rem 0;
    cursor: pointer;
  }
  .bad {
    color: var(--red);
  }
  .small {
    font-size: 0.82rem;
  }
</style>
