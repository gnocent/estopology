<script lang="ts">
  // One group of the physical hierarchy (zone, tier or machine) on its own:
  // its nodes drawn large, its sums, the table of its nodes, what it holds
  // and what happened to it.
  import { api, ApiError, type StateView, type GroupDetail, type EsEvent, type NodeView, type GroupMetricPoint } from '../lib/api';
  import Sparkline from '../components/Sparkline.svelte';
  import { router, href, enc } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';
  import { bytes, num, pct, dateTime } from '../lib/format';
  import { colorModes, type ColorMode } from '../lib/colors';
  import { findGroup, groupHref, kindName, nodeHref, nodeTip, sums } from '../lib/topo';
  import { tip } from '../lib/tip';
  import Crumbs from '../components/Crumbs.svelte';
  import EventTable from '../components/EventTable.svelte';
  import Icon from '../components/Icon.svelte';
  import Info from '../components/Info.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import PhysicalView from '../components/PhysicalView.svelte';
  import TimeBand from '../components/TimeBand.svelte';

  let { cluster, labels }: { cluster: string; labels: string[] } = $props();

  const at = $derived(router.num('at'));
  const mode = $derived((router.query.get('color') as ColorMode) || 'status');
  const key = $derived(labels.join('\u0000'));

  let sv = $state<StateView | null>(null);
  let detail = $state<GroupDetail | null>(null);
  let events = $state<EsEvent[]>([]);
  let error = $state('');

  $effect(() => {
    const ctrl = new AbortController();
    api.state(cluster, at, ctrl.signal).then(
      (s) => ((sv = s), (error = '')),
      (e) => {
        if ((e as Error).name === 'AbortError') return;
        error = e instanceof ApiError && e.status === 404 ? t('common.nodata') : (e as Error).message;
        sv = null;
      },
    );
    return () => ctrl.abort();
  });

  const week = 7 * 86_400_000;
  $effect(() => {
    void key;
    const ctrl = new AbortController();
    const to = at ?? Date.now();
    detail = null;
    api.group(cluster, labels, at, ctrl.signal).then((d) => (detail = d), () => {});
    api.groupEvents(cluster, labels, to - week, to, 100, ctrl.signal).then((p) => (events = p.events ?? []), () => (events = []));
    return () => ctrl.abort();
  });

  // Metrics of the nodes of the group over the 7 days that end one day after
  // the chosen date (or now).
  const histTo = $derived(Math.min(Date.now(), (at ?? Date.now()) + 86_400_000));
  let metrics = $state<GroupMetricPoint[]>([]);
  $effect(() => {
    void key;
    const ctrl = new AbortController();
    api.groupHistory(cluster, labels, at, histTo - week, histTo, 400, ctrl.signal).then((p) => (metrics = p ?? []), () => (metrics = []));
    return () => ctrl.abort();
  });
  const serie = (f: (p: GroupMetricPoint) => number) => metrics.map((p) => ({ ts: p.ts, v: f(p) }));

  const chain = $derived(sv ? findGroup(sv.hierarchy, labels) : null);
  const group = $derived(chain ? chain[chain.length - 1] : null);
  const members = $derived(group && sv ? (group.nodes ?? []).map((i) => sv!.nodes[i]).filter(Boolean) : []);
  const s = $derived(sums(members));

  // Table of the nodes, sorted by the column that was clicked.
  type Col = 'name' | 'machine' | 'shards' | 'primaries' | 'disk' | 'cpu' | 'heap';
  let sort = $state<{ col: Col; dir: 1 | -1 }>({ col: 'name', dir: 1 });
  const value: Record<Col, (n: NodeView) => number | string> = {
    name: (n) => n.name,
    machine: (n) => n.machine,
    shards: (n) => n.shards,
    primaries: (n) => n.primaries,
    disk: (n) => n.disk_percent,
    cpu: (n) => n.metrics?.proc_cpu ?? -1,
    heap: (n) => n.metrics?.heap_pct ?? -1,
  };
  const rows = $derived(
    [...members].sort((a, b) => {
      const x = value[sort.col](a);
      const y = value[sort.col](b);
      const c = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y), undefined, { numeric: true });
      return c * sort.dir;
    }),
  );
  function by(col: Col) {
    sort = { col, dir: sort.col === col ? (-sort.dir as 1 | -1) : col === 'name' || col === 'machine' ? 1 : -1 };
  }
  let famFilter = $state('');
  const families = $derived((detail?.families ?? []).filter((f) => f.family.toLowerCase().includes(famFilter.trim().toLowerCase())));
  const arrow = (col: Col) => (sort.col === col ? (sort.dir > 0 ? ' ▲' : ' ▼') : '');
</script>

<TimeBand {cluster} {at} onchange={(d) => router.set({ at: d ?? null })} />

{#if error}
  <div class="banner warn">{error}</div>
{:else if !sv}
  <p class="muted loading">{t('common.loading')}</p>
{:else if !group}
  <Crumbs {cluster} clusterName={sv.name} labels={[]} current={labels[labels.length - 1] ?? ''} {at} />
  <div class="banner warn">{t('group.unknown', { name: labels.join(' / '), date: dateTime(sv.at) })}</div>
{:else}
  <Crumbs {cluster} clusterName={sv.name} labels={labels.slice(0, -1)} current={group.label} {at} />
  <h1><Icon name={`group.${group.kind}`} size={20} /> <span class="muted kind">{kindName(group)}</span> {group.label}</h1>
  <PageHelp id="group" />

  <div class="summary">
    <div class="card stat" class:bad={s.missing > 0} use:tip={t('group.sum.nodesHelp')}>
      <div class="v"><span class="ico"><Icon name="node" size={18} /></span>{num(s.present)}{#if s.missing}<span class="sub"> / {num(s.nodes)}</span>{/if}</div>
      <div class="muted">{s.missing ? t('group.sum.nodesMissing', { n: s.missing }) : t('group.sum.nodes')}</div>
    </div>
    {#if group.kind !== 'machine'}
      <div class="card stat" use:tip={t('group.sum.machinesHelp')}>
        <div class="v"><span class="ico"><Icon name="machine" size={18} /></span>{num(s.machines)}</div>
        <div class="muted">{t('group.sum.machines')}</div>
      </div>
    {/if}
    <div class="card stat" use:tip={t('group.sum.shardsHelp')}>
      <div class="v"><span class="ico"><Icon name="shard" size={18} /></span>{num(s.shards)}</div>
      <div class="muted">{t('group.sum.shards', { p: num(s.primaries) })}</div>
    </div>
    <div class="card stat" class:bad={s.aboveWatermark > 0} use:tip={t('group.sum.diskHelp')}>
      <div class="v"><span class="ico"><Icon name="disk" size={18} /></span>{s.diskTotal ? pct((100 * s.diskUsed) / s.diskTotal) : '—'}</div>
      <div class="muted">
        {t('group.sum.disk', { used: bytes(s.diskUsed), total: bytes(s.diskTotal) })}{#if s.aboveWatermark} · {t('group.sum.watermark', { n: s.aboveWatermark })}{/if}
      </div>
    </div>
    <div class="card stat" use:tip={t('group.sum.cpuHelp')}>
      <div class="v"><span class="ico"><Icon name="chart" size={18} /></span>{s.measured ? pct(s.cpuAvg) : '—'}</div>
      <div class="muted">{t('group.sum.cpu', { max: s.measured ? pct(s.cpuMax) : '—' })}</div>
    </div>
    <div class="card stat" use:tip={t('group.sum.heapHelp')}>
      <div class="v"><span class="ico"><Icon name="activity" size={18} /></span>{s.measured ? pct(s.heapAvg) : '—'}</div>
      <div class="muted">{t('group.sum.heap', { max: s.measured ? pct(s.heapMax) : '—' })}</div>
    </div>
    <div class="card stat" class:zero={s.initializing + s.relocatingIn + s.relocatingOut === 0} use:tip={t('group.sum.movingHelp')}>
      <div class="v"><span class="ico"><Icon name="flows" size={18} /></span>{num(s.initializing + s.relocatingIn + s.relocatingOut)}</div>
      <div class="muted">{t('group.sum.moving', { i: s.initializing, rin: s.relocatingIn, rout: s.relocatingOut })}</div>
    </div>
    <div class="card stat" use:tip={t('group.sum.versionsHelp')}>
      <div class="v versions"><span class="ico"><Icon name="tag" size={18} /></span>{#each s.versions as v (v)}<span class="badge">{v}</span>{:else}—{/each}</div>
      <div class="muted">{t('home.col.versions')}</div>
    </div>
  </div>

  <section class="card">
    <h2><Icon name="chart" /> {t('group.metrics')} <Info text={t(group.kind === 'machine' ? 'group.metricsHelpMachine' : 'group.metricsHelp')} /></h2>
    <div class="charts">
      <Sparkline height={130} label={t('group.m.cpu')} unit=" %" max={100} {at} points={serie((p) => p.proc_cpu)} />
      {#if group.kind === 'machine'}
        <Sparkline height={130} label={t('node.oscpu')} unit=" %" max={100} {at} points={serie((p) => p.os_cpu)} />
        <Sparkline height={130} label={t('node.load')} {at} points={serie((p) => p.load1)} />
      {/if}
      <Sparkline height={130} label={t('group.m.heap')} unit=" %" max={100} {at} points={serie((p) => p.heap_pct)} />
      <Sparkline height={130} label={t('group.m.disk')} unit=" %" max={100} {at} points={serie((p) => (p.disk_total > 0 ? (p.disk_used * 100) / p.disk_total : -1))} />
      <Sparkline height={130} label={t('group.m.shards')} {at} points={serie((p) => p.shards)} format={(v) => `${num(Math.round(v))} ${t('unit.shards')}`} />
      {#if group.kind !== 'machine'}
        <Sparkline height={130} label={t('group.m.nodes')} {at} points={serie((p) => p.nodes)} format={(v) => num(Math.round(v))} />
      {/if}
    </div>
  </section>

  <section class="card">
    <div class="row head">
      <h2><Icon name="topology" /> {t('group.layout')} <Info text={t('group.layoutHelp')} /></h2>
      <span class="spacer"></span>
      <label class="row">
        <span class="muted">{t('cluster.colorBy')}</span>
        <select value={mode} onchange={(e) => router.set({ color: (e.target as HTMLSelectElement).value })}>
          {#each colorModes as m (m)}<option value={m}>{tk(`cluster.color.${m}`)}</option>{/each}
        </select>
      </label>
    </div>
    <PhysicalView
      {cluster}
      {at}
      root={group}
      base={labels}
      nodes={sv.nodes}
      {mode}
      large={members.length <= 48}
      onselect={(n, el) => router.go(nodeHref(cluster, n, { at }), false, el)}
    />
  </section>

  <section class="card">
    <h2><Icon name="node" /> {t('group.nodesTable', { n: members.length })} <Info text={t('group.nodesTableHelp')} /></h2>
    <div class="scroll">
      <table class="data">
        <thead>
          <tr>
            <th><button class="sort" onclick={() => by('name')}>{t('col.node')}{arrow('name')}</button></th>
            <th><button class="sort" onclick={() => by('machine')}>{t('node.machine')}{arrow('machine')}</button></th>
            <th>{t('node.ip')}</th>
            <th>{t('node.roles')}</th>
            <th>{t('node.version')}</th>
            <th class="num"><button class="sort" onclick={() => by('shards')}>{t('node.shards')}{arrow('shards')}</button></th>
            <th class="num"><button class="sort" onclick={() => by('primaries')}>{t('node.primaries')}{arrow('primaries')}</button></th>
            <th class="num"><button class="sort" onclick={() => by('disk')}>{t('node.disk')}{arrow('disk')}</button></th>
            <th class="num"><button class="sort" onclick={() => by('cpu')}>{t('node.cpu')}{arrow('cpu')}</button></th>
            <th class="num"><button class="sort" onclick={() => by('heap')}>{t('node.heap')}{arrow('heap')}</button></th>
            <th>{t('group.col.moving')}</th>
          </tr>
        </thead>
        <tbody>
          {#each rows as n (n.name + n.es_id)}
            <tr class:gone={n.missing || !n.present}>
              <td><a href={nodeHref(cluster, n.name, { at })} use:tip={nodeTip(n)}>{n.name}</a>{#if n.missing || !n.present} <span class="badge bad">{t('cluster.missing')}</span>{/if}</td>
              <td class="muted">{n.machine || '—'}</td>
              <td class="mono">{n.ip || '—'}</td>
              <td class="small">{(n.roles ?? []).join(', ')}</td>
              <td>{n.version || '—'}</td>
              <td class="num">{num(n.shards)}</td>
              <td class="num">{num(n.primaries)}</td>
              <td class="num" class:bad={n.above_high_watermark} use:tip={n.metrics ? `${bytes(n.metrics.disk_used)} / ${bytes(n.metrics.disk_total)}` : ''}>{n.metrics ? pct(n.disk_percent) : '—'}</td>
              <td class="num">{n.metrics ? pct(n.metrics.proc_cpu) : '—'}</td>
              <td class="num">{n.metrics ? pct(n.metrics.heap_pct) : '—'}</td>
              <td class="small">
                {#if n.initializing + n.relocating_in + n.relocating_out}{t('tile.moving', { i: n.initializing, rin: n.relocating_in, rout: n.relocating_out })}{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  <div class="two">
    <section class="card">
      <h2><Icon name="shard" /> {t('group.content')} <Info text={t('group.contentHelp')} /></h2>
      {#if !detail}<p class="muted loading">{t('common.loading')}</p>
      {:else if detail.families.length === 0}<p class="muted">{t('common.none')}</p>
      {:else}
        <input class="famfilter" type="search" bind:value={famFilter} placeholder={t('content.filter', { n: detail.families.length })} />
        <table class="data">
          <thead><tr><th>{t('col.family')} <Info text={t('family.help')} /></th><th class="num">{t('col.copies')}</th><th class="num">{t('col.pri')}</th></tr></thead>
          <tbody>
            {#each families.slice(0, 40) as f (f.family)}
              <tr>
                <td><a href={href(`/c/${enc(cluster)}/family/${enc(f.family)}`, { at })}>{f.family}</a></td>
                <td class="num">{num(f.copies)}</td>
                <td class="num">{num(f.primaries)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#if families.length > 40}<p class="muted small">{t('group.moreFamilies', { n: families.length - 40 })}</p>{/if}
      {/if}
    </section>

    <section class="card">
      <h2><Icon name="events" /> {t('group.events')} <Info text={t('group.eventsHelp')} /></h2>
      {#if events.length === 0}<p class="muted">{t('group.noEvents')}</p>
      {:else}<EventTable {cluster} {events} />{/if}
    </section>
  </div>
{/if}

<style>
  h1 .kind {
    font-weight: 400;
    font-size: 0.95rem;
  }
  .summary {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
    gap: 0.6rem;
    margin-bottom: 0.9rem;
  }
  .stat .v {
    font-size: 1.35rem;
    font-weight: 600;
  }
  .stat .sub {
    font-size: 0.95rem;
    font-weight: 400;
    color: var(--muted);
    margin-left: 0.25rem;
  }
  .versions {
    flex-wrap: wrap;
    gap: 0.25rem;
    font-size: 1rem !important;
  }
  section {
    margin-bottom: 0.9rem;
  }
  section h2 {
    margin-top: 0;
  }
  .head {
    margin-bottom: 0.2rem;
  }
  .head h2 {
    margin-bottom: 0;
  }
  .charts {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 0.9rem;
  }
  @media (max-width: 1100px) {
    .charts {
      grid-template-columns: 1fr;
    }
  }
  .scroll {
    overflow-x: auto;
  }
  .two {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 2fr);
    gap: 0.9rem;
    align-items: start;
  }
  @media (max-width: 1100px) {
    .two {
      grid-template-columns: 1fr;
    }
  }
  .famfilter {
    width: 100%;
    margin-bottom: 0.4rem;
  }
  .sort {
    all: unset;
    cursor: pointer;
  }
  .sort:hover {
    color: var(--accent);
  }
  .small {
    font-size: 0.82rem;
  }
  tr.gone td {
    color: var(--muted);
  }
  td.bad,
  .badge.bad {
    color: var(--red);
    font-weight: 600;
  }
</style>
