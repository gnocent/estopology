<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import { tip } from '../lib/tip';
  import { sankey, sankeyLinkHorizontal, type SankeyNode, type SankeyLink } from 'd3-sankey';
  import { api2, type Flows } from '../lib/api';
  import { router } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';
  import { dateTime, num } from '../lib/format';
  import { abWindow } from '../lib/window.svelte';
  import WindowPicker from '../components/WindowPicker.svelte';
  import EventTable from '../components/EventTable.svelte';
  import ShardPath from '../components/ShardPath.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';

  let { cluster }: { cluster: string } = $props();
  const win = abWindow();
  const level = $derived(router.query.get('level') ?? 'tier');
  const fromG = $derived(router.query.get('from_group') ?? '');
  const toG = $derived(router.query.get('to_group') ?? '');

  let f = $state<Flows | null>(null);
  let error = $state('');

  $effect(() => {
    const ctrl = new AbortController();
    api2.flows(cluster, { a: win.a, b: win.b, level, from_group: fromG, to_group: toG }, ctrl.signal).then(
      (x) => ((f = x), (error = '')),
      (e) => {
        if ((e as Error).name === 'AbortError') return;
        f = null;
        error = (e as Error).message;
      },
    );
    return () => ctrl.abort();
  });

  // Trajectory of one of the shards of the selected flow (URL parameter
  // "shard" = uuid/number, a permalink).
  const moved = $derived.by(() => {
    const seen = new Map<string, string>();
    for (const e of f?.events ?? []) {
      if (e.subject_kind === 'shard' && !seen.has(e.subject_ref)) {
        const p = e.payload ?? {};
        seen.set(e.subject_ref, `${String(p.index ?? e.subject_ref)}[${String(p.shard ?? '')}]`);
      }
    }
    return [...seen.entries()].sort((a, b) => a[1].localeCompare(b[1], undefined, { numeric: true }));
  });
  const shardRef = $derived(router.query.get('shard') ?? '');
  const shardSel = $derived.by(() => {
    const i = shardRef.lastIndexOf('/');
    return i > 0 ? { uuid: shardRef.slice(0, i), shard: Number(shardRef.slice(i + 1)) } : null;
  });

  const W = 900;
  const H = 420;
  type N = { name: string };
  type L = { source: number; target: number; value: number; from: string; to: string };

  // Sources on the left, destinations on the right (distinct node sets).
  const graph = $derived.by(() => {
    if (!f || f.links.length === 0) return null;
    const links = f.links.slice(0, 200);
    const names: string[] = [];
    const idx = new Map<string, number>();
    const id = (k: string) => {
      if (!idx.has(k)) {
        idx.set(k, names.length);
        names.push(k);
      }
      return idx.get(k)!;
    };
    const ls: L[] = links.map((l) => ({ source: id('A:' + l.from), target: id('B:' + l.to), value: l.count, from: l.from, to: l.to }));
    const layout = sankey<N, L>()
      .nodeWidth(12)
      .nodePadding(10)
      .extent([
        [140, 10],
        [W - 140, H - 10],
      ]);
    return layout({ nodes: names.map((n) => ({ name: n })), links: ls.map((l) => ({ ...l })) });
  });

  const maxHist = $derived(Math.max(1, ...(f?.histogram ?? []).map((h) => h.count)));
  const path = sankeyLinkHorizontal();
  const label = (n: SankeyNode<N, L>) => n.name.slice(2) || '∅';
</script>

<h1><Icon name="flows" size={20} /> {t('flows.title')}</h1>
<PageHelp id="flows" />
<WindowPicker />
<div class="row">
  <label class="row"
    ><span class="muted">{t('flows.level')}</span> <Info text={t('flows.levelHelp')} />
    <select value={level} onchange={(e) => router.set({ level: (e.target as HTMLSelectElement).value, from_group: null, to_group: null })}>
      {#each ['dc', 'tier', 'machine', 'node'] as l (l)}<option value={l}>{tk(`flows.level.${l}`)}</option>{/each}
    </select>
  </label>
</div>
{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
{#if !f}
  {#if !error}<p class="muted loading">{t('common.loading')}</p>{/if}
{:else if !graph}
  <p class="muted">{t('flows.none')}</p>
{:else}
  <div class="card">
    <div class="row cols"><strong>{t('flows.origin')}</strong><span class="spacer"></span><span class="muted">{t('flows.sankeyHint')}</span><span class="spacer"></span><strong>{t('flows.destination')}</strong></div>
    <svg viewBox={`0 0 ${W} ${H}`} class="sankey">
      <defs>
        <linearGradient id="flow-grad" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2={W} y2="0">
          <stop offset="0" stop-color="var(--accent)" />
          <stop offset="1" stop-color="var(--accent-2)" />
        </linearGradient>
      </defs>
      {#each graph.links as l, i (i)}
        {@const link = l as SankeyLink<N, L> & L}
        <path
          d={path(l) ?? ''}
          stroke-width={Math.max(1, l.width ?? 1)}
          class="link"
          class:active={link.from === fromG && link.to === toG}
          role="button"
          tabindex="0"
          onclick={() => router.set({ from_group: link.from, to_group: link.to })}
          onkeydown={(e) => e.key === 'Enter' && router.set({ from_group: link.from, to_group: link.to })}
          use:tip={t('flows.linkTip', { from: link.from, to: link.to, n: num(link.value) })}
        ></path>
      {/each}
      {#each graph.nodes as n, i (i)}
        <rect x={n.x0} y={n.y0} width={(n.x1 ?? 0) - (n.x0 ?? 0)} height={Math.max(1, (n.y1 ?? 0) - (n.y0 ?? 0))} class="node" />
        <text
          x={(n.x0 ?? 0) < W / 2 ? (n.x0 ?? 0) - 6 : (n.x1 ?? 0) + 6}
          y={((n.y0 ?? 0) + (n.y1 ?? 0)) / 2}
          text-anchor={(n.x0 ?? 0) < W / 2 ? 'end' : 'start'}
          dominant-baseline="middle">{label(n)} ({num(n.value ?? 0)})</text
        >
      {/each}
    </svg>
  </div>

  <h2><Icon name="chart" /> {t('flows.histogram')} <Info text={t('flows.histogramHelp')} /></h2>
  <p class="muted small">{t('flows.histogramScale', { max: num(maxHist), from: dateTime(win.a), to: dateTime(win.b) })}</p>
  <div class="card hist">
    {#each f.histogram as h (h.from)}
      <div class="bar" style:height={`${(h.count / maxHist) * 100}%`} use:tip={t('flows.barTip', { from: dateTime(h.from), to: dateTime(h.to), n: num(h.count) })}></div>
    {/each}
  </div>

  {#if fromG}
    <h2>{t('flows.selected', { from: fromG, to: toG })}</h2>
    <div class="card"><EventTable {cluster} events={f.events ?? []} /></div>
  {/if}

  {#if moved.length || shardSel}
    <h2><Icon name="path" /> {t('flows.trajectory')}</h2>
    <div class="card">
      <select value={shardRef} onchange={(e) => router.set({ shard: (e.target as HTMLSelectElement).value || null })}>
        <option value="">{t('flows.pickShard')}</option>
        {#each moved as [ref, label] (ref)}<option value={ref}>{label}</option>{/each}
      </select>
      {#if shardSel}<ShardPath {cluster} uuid={shardSel.uuid} shard={shardSel.shard} from={win.a} to={win.b} />{/if}
    </div>
  {/if}
{/if}

<style>
  .cols {
    margin-bottom: 0.3rem;
    font-size: 0.9rem;
  }
  .small {
    font-size: 0.85rem;
    margin: 0 0 0.3rem;
  }
  .sankey {
    width: 100%;
    height: auto;
    font-size: 11px;
  }
  .sankey text {
    fill: var(--text);
  }
  .link {
    fill: none;
    stroke: url(#flow-grad);
    stroke-opacity: 0.35;
    cursor: pointer;
    transition: stroke-opacity var(--fast) var(--ease);
  }
  .link:hover,
  .link.active {
    stroke-opacity: 0.8;
  }
  .link:focus-visible {
    outline: none;
    stroke-opacity: 0.8;
  }
  .node {
    fill: var(--text);
    opacity: 0.75;
    rx: 2px;
  }
  .hist {
    display: flex;
    align-items: flex-end;
    gap: 2px;
    height: 120px;
  }
  .bar {
    flex: 1;
    background: linear-gradient(180deg, var(--accent-2), var(--accent));
    min-height: 1px;
    border-radius: 3px 3px 0 0;
    opacity: 0.8;
    transition: opacity var(--fast) var(--ease);
  }
  .bar:hover {
    opacity: 1;
  }
</style>
