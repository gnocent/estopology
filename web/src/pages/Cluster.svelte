<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import { tip } from '../lib/tip';
  import { api, ApiError, type StateView, type Matrix } from '../lib/api';
  import { router, href, enc } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';
  import { dateTime, utc, num } from '../lib/format';
  import { colorModes, type ColorMode } from '../lib/colors';
  import TimeBand from '../components/TimeBand.svelte';
  import PhysicalView from '../components/PhysicalView.svelte';
  import LogicalView from '../components/LogicalView.svelte';
  import Severity from '../components/Severity.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';
  import { nodeHref, groupHref, kindName } from '../lib/topo';
  import type { Group } from '../lib/api';
  import type { Column, Placed } from '../components/LogicalView.svelte';

  let { cluster }: { cluster: string } = $props();

  const at = $derived(router.num('at'));
  const view = $derived(router.query.get('view') === 'logical' ? 'logical' : 'physical');
  const mode = $derived((router.query.get('color') as ColorMode) || 'status');
  // Logical view: which families, and how many of them.
  // The text as typed (kept in the URL untrimmed, so the field is not
  // rewritten while typing) and the filter that is applied.
  const fq = $derived(router.query.get('fq') ?? '');
  const familyFilter = $derived(fq.trim());
  const top = $derived(router.num('top') ?? 30);
  const byIndex = $derived(router.query.get('rows') === 'index');
  let typing: ReturnType<typeof setTimeout> | undefined;
  function filterFamilies(v: string) {
    clearTimeout(typing);
    typing = setTimeout(() => router.set({ fq: v.trim() ? v : null }), 250);
  }
  $effect(() => () => clearTimeout(typing));
  // Columns of the logical view: the nodes, or the groups of one level of
  // the hierarchy (zone, tier, machine) with the copies of their nodes added up.
  const colDepth = $derived(router.num('cols') ?? -1);
  const levels = $derived.by(() => {
    const out: Group[] = [];
    let g: Group | undefined = sv?.hierarchy;
    while (g?.children?.length) {
      g = g.children[0];
      out.push(g);
    }
    return out;
  });
  const columns = $derived.by(() => {
    const state = sv;
    const mx = matrix;
    if (!state || !mx || colDepth < 0 || colDepth >= levels.length) return undefined;
    const pos = new Map(mx.nodes.map((n, i) => [n, i]));
    const out: (Column & { path: string[] })[] = [];
    const walk = (g: Group, labels: string[], above: Placed['path'], depth: number) => {
      for (const c of g.children ?? []) {
        const path = [...labels, c.label];
        if (depth === colDepth || !c.children?.length) {
          const nodes = (c.nodes ?? []).map((i) => pos.get(state.nodes[i]?.name)).filter((x): x is number => x !== undefined);
          out.push({ label: c.label, kind: kindName(c), path, nodes, above });
        } else walk(c, path, [...above, { label: c.label, kind: c.kind, name: kindName(c) }], depth + 1);
      }
    };
    walk(state.hierarchy, [], [], 0);
    return out;
  });
  // One column per node: the nodes in the order of the hierarchy, each with
  // the groups above it (drawn as bands over the columns).
  const layout = $derived.by(() => {
    const state = sv;
    const mx = matrix;
    if (!state || !mx) return undefined;
    const pos = new Map(mx.nodes.map((n, i) => [n, i]));
    const out: Placed[] = [];
    const walk = (g: Group, path: Placed['path']) => {
      if (g.children?.length) {
        for (const c of g.children) walk(c, [...path, { label: c.label, kind: c.kind, name: kindName(c) }]);
        return;
      }
      for (const i of g.nodes ?? []) {
        const p = pos.get(state.nodes[i]?.name);
        if (p !== undefined) out.push({ node: p, path });
      }
    };
    walk(state.hierarchy, []);
    return out;
  });
  // Page of a row of the logical view: the family, or the index by its UUID.
  function rowHref(name: string): string {
    const uuid = matrix?.uuids?.[matrix.families.indexOf(name)];
    return href(uuid ? `/c/${enc(cluster)}/index/${enc(uuid)}` : `/c/${enc(cluster)}/family/${enc(name)}`, { at });
  }
  const machine = $derived(router.query.get('machine') ?? undefined);

  let sv = $state<StateView | null>(null);
  let matrix = $state<Matrix | null>(null);
  let matrixError = $state('');
  let matrixLoading = $state(false);
  let error = $state('');
  let loading = $state(false);

  $effect(() => {
    const ctrl = new AbortController();
    loading = true;
    api.state(cluster, at, ctrl.signal).then(
      (s) => {
        sv = s;
        error = '';
        loading = false;
      },
      (e) => {
        if ((e as Error).name === 'AbortError') return;
        error = e instanceof ApiError && e.status === 404 ? t('common.nodata') : (e as Error).message;
        sv = null;
        loading = false;
      },
    );
    return () => ctrl.abort();
  });

  $effect(() => {
    if (view !== 'logical') return;
    const ctrl = new AbortController();
    matrixLoading = true;
    api.matrix(cluster, at, top, familyFilter, byIndex ? 'index' : '', ctrl.signal).then(
      (m) => {
        matrix = m;
        matrixError = '';
        matrixLoading = false;
      },
      (e) => {
        if ((e as Error).name === 'AbortError') return;
        matrix = null;
        matrixError = (e as Error).message;
        matrixLoading = false;
      },
    );
    return () => ctrl.abort();
  });

  const concerns = $derived(sv?.assessment.concerns ?? []);
</script>

<TimeBand {cluster} {at} onchange={(d) => router.set({ at: d ?? null })} />
<PageHelp id="cluster" />

{#if error}
  <div class="banner warn">{error}</div>
{:else if !sv}
  <p class="muted loading">{t('common.loading')}</p>
{:else}
  {#if sv.unknown}<div class="banner warn">{t('cluster.unknownBand')}</div>{/if}
  <div class="layout" class:dim={loading || (view === 'logical' && matrixLoading)}>
    <div class="main">
      <div class="row head">
        <h1><Icon name="topology" size={20} /> {sv.name}</h1>
        <Severity a={sv.assessment} />
        <span class="muted">
          {t('cluster.counts', {
            nodes: num(sv.counts.nodes_present),
            indices: num(sv.counts.indices),
            shards: num(sv.counts.shards),
            unassigned: num(sv.counts.unassigned),
          })}
        </span>
        <span class="spacer"></span>
        <div class="seg">
          <button aria-pressed={view === 'physical'} onclick={() => router.set({ view: null })}>{t('cluster.physical')}</button>
          <button aria-pressed={view === 'logical'} onclick={() => router.set({ view: 'logical' })}>{t('cluster.logical')}</button>
        </div>
        <Info text={t('cluster.viewsHelp')} />
        {#if view === 'physical'}
          <label class="row">
            <span class="muted">{t('cluster.colorBy')}</span>
            <select value={mode} onchange={(e) => router.set({ color: (e.target as HTMLSelectElement).value })}>
              {#each colorModes as m (m)}<option value={m}>{tk(`cluster.color.${m}`)}</option>{/each}
            </select>
          </label>
        {/if}
      </div>
      <p class="muted small" use:tip={utc(sv.capture_ts)}>
        {t('cluster.capture', { date: dateTime(sv.capture_ts), probe: dateTime(sv.probe_ts) })}
        <Info text={t('cluster.captureHelp')} />
      </p>
      <div class="strip">
        <div class="card mini">
          <h2><Icon name="activity" /> {t('home.col.state')} <Info text={t('severity.help')} /></h2>
          {#if concerns.length === 0}<p>{t('level.0')}</p>{/if}
          <ul class="plain">
            {#each concerns as c (c.code)}
              <li><span class="dot" style:background={`var(--sev-${c.level})`}></span> {tk(`concern.${c.code}`, { n: c.count })}</li>
            {/each}
          </ul>
        </div>
        {#if sv.missing_nodes.length}
          <div class="card mini">
            <h2><Icon name="losses" /> {t('cluster.missing')} <span class="badge">{sv.missing_nodes.length}</span></h2>
            <p class="chips">
              {#each sv.missing_nodes.slice(0, 12) as n (n)}<a href={nodeHref(cluster, n, { at })}>{n}</a>{/each}
              {#if sv.missing_nodes.length > 12}<span class="muted">+{sv.missing_nodes.length - 12}</span>{/if}
            </p>
          </div>
        {/if}
        {#if sv.remotes.length}
          <div class="card mini">
            <h2><Icon name="globe" /> {t('cluster.remotes')} <Info text={t('cluster.remotesHelp')} /></h2>
            <ul class="plain">
              {#each sv.remotes as r (r.alias)}
                <li>
                  <span class="dot" class:green={r.connected} class:red={!r.connected}></span>
                  {r.alias} <span class="muted">({r.mode}, {r.connected ? t('cluster.remote.connected') : t('cluster.remote.disconnected')})</span>
                </li>
              {/each}
            </ul>
          </div>
        {/if}
        <div class="card mini">
          <h2><Icon name="tag" /> {t('home.col.versions')}</h2>
          <p>{#each sv.versions ?? [] as v (v)}<span class="badge">{v}</span> {/each}</p>
        </div>
      </div>
      {#if view === 'physical'}
        <PhysicalView
          {cluster}
          {at}
          root={sv.hierarchy}
          nodes={sv.nodes}
          {mode}
          highlight={machine}
          onselect={(n, el) => router.go(nodeHref(cluster, n, { at }), false, el)}
        />
      {:else if matrix}
        <div class="row filter">
          <div class="seg">
            <button aria-pressed={!byIndex} onclick={() => router.set({ rows: null })}>{t('matrix.rows.family')}</button>
            <button aria-pressed={byIndex} onclick={() => router.set({ rows: 'index' })}>{t('matrix.rows.index')}</button>
          </div>
          <Info text={t('matrix.rowsHelp')} />
          <label class="row">
            <span class="muted">{t('matrix.filter')}</span>
            <input type="search" value={fq} placeholder={byIndex ? t('matrix.filterHintIndex') : t('matrix.filterHint')} oninput={(e) => filterFamilies((e.target as HTMLInputElement).value)} />
          </label>
          <Info text={t('matrix.filterHelp')} />
          <label class="row">
            <span class="muted">{t('matrix.top')}</span>
            <select value={String(top)} onchange={(e) => router.set({ top: (e.target as HTMLSelectElement).value === '30' ? null : (e.target as HTMLSelectElement).value })}>
              {#each [30, 60, 120, 500] as n (n)}<option value={String(n)}>{n === 500 ? t('matrix.topAll') : n}</option>{/each}
            </select>
          </label>
          <label class="row">
            <span class="muted">{t('matrix.cols')}</span>
            <select value={String(colDepth)} onchange={(e) => router.set({ cols: (e.target as HTMLSelectElement).value === '-1' ? null : (e.target as HTMLSelectElement).value })}>
              <option value="-1">{t('matrix.cols.node')}</option>
              {#each levels as g, i (i)}<option value={String(i)}>{t('matrix.cols.by', { kind: kindName(g) })}</option>{/each}
            </select>
          </label>
          <Info text={t('matrix.colsHelp')} />
          <span class="muted">{t(byIndex ? 'matrix.shownIndices' : 'matrix.shown', { n: Math.min(matrix.total, top), total: num(matrix.total) })}</span>
        </div>
        <LogicalView
          m={matrix}
          rowKind={byIndex ? t('matrix.row.index') : t('matrix.row.family')}
          rowIcon={byIndex ? 'index' : 'family'}
          rowHref={rowHref}
          onfamily={(f) => router.go(rowHref(f))}
          {columns}
          {layout}
          onnode={(n) => router.go(nodeHref(cluster, n, { at }))}
          oncolumn={(i) => columns && router.go(groupHref(cluster, columns[i].path, { at }))}
        />
      {:else if matrixError}
        <div class="banner warn">{t('common.error', { msg: matrixError })}</div>
      {:else}
        <p class="muted loading">{t('common.loading')}</p>
      {/if}
    </div>

  </div>
{/if}

<style>
  .layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 1rem;
    align-items: start;
    transition: opacity 0.2s;
  }
  .layout.dim {
    opacity: 0.6;
  }
  .head h1 {
    margin: 0;
  }
  .small {
    font-size: 0.82rem;
    margin: 0.2rem 0 0.8rem;
  }
  .seg {
    display: flex;
  }
  .seg button:first-child {
    border-radius: var(--radius) 0 0 var(--radius);
  }
  .seg button:last-child {
    border-radius: 0 var(--radius) var(--radius) 0;
    margin-left: -1px;
  }
  .filter {
    margin-bottom: 0.5rem;
  }
  .filter input {
    width: 18rem;
  }
  .strip {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
    gap: 0.6rem;
    margin-bottom: 0.8rem;
  }
  .mini {
    padding: 0.6rem 0.8rem;
  }
  .mini h2 {
    font-size: 0.95rem;
    margin: 0 0 0.3rem;
  }
  .mini p {
    margin: 0.2rem 0;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 0.8rem;
  }

  ul.plain {
    list-style: none;
    padding: 0;
    margin: 0.3rem 0;
  }

  ul.plain li {
    padding: 0.15rem 0;
  }
  @media (max-width: 1100px) {
    .layout {
      grid-template-columns: 1fr;
    }
  }
</style>
