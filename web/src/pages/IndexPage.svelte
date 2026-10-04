<script lang="ts">
  import TimeBand from '../components/TimeBand.svelte';
  import Icon from '../components/Icon.svelte';
  import { tip } from '../lib/tip';
  import { api, type IndexDetail, type ShardView, type Hit } from '../lib/api';
  import { router, href, enc } from '../lib/router.svelte';
  import { t } from '../lib/i18n';
  import { dateTime, utc, num, bytes } from '../lib/format';
  import ShardPath from '../components/ShardPath.svelte';
  import Sparkline from '../components/Sparkline.svelte';
  import type { FamilyPoint } from '../lib/api';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';

  let { cluster, uuid }: { cluster: string; uuid: string } = $props();
  const at = $derived(router.num('at'));
  // Period of the shard path: a preset ("last …", ending at the chosen date)
  // or two dates typed in. Nothing is drawn before the index was created nor
  // after it was deleted.
  const spans = [1, 6, 24, 168, 744];
  const span = $derived(router.num('span') ?? 168);
  const custom = $derived(router.num('from') !== undefined || router.num('to') !== undefined);
  const loadedAt = Date.now();
  const to = $derived(router.num('to') ?? at ?? loadedAt);
  const from = $derived(router.num('from') ?? to - span * 3_600_000);
  function local(ms: number): string {
    return new Date(ms - new Date(ms).getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
  }
  function parse(v: string): number | null {
    const x = new Date(v).getTime();
    return Number.isFinite(x) ? x : null;
  }
  const spanLabel = (h: number) => (h === 1 ? t('win.lastHour') : h < 48 ? t('win.last', { h }) : t('win.days', { d: h / 24 }));
  const shardParam = $derived(router.num('shard')); // from the search "shard N de X à T"

  let d = $state<IndexDetail | null>(null);
  let error = $state('');

  $effect(() => {
    const ctrl = new AbortController();
    api.index(cluster, uuid, { at, from, to }, ctrl.signal).then(
      (x) => ((d = x), (error = '')),
      (e) => (e as Error).name !== 'AbortError' && (error = (e as Error).message),
    );
    return () => ctrl.abort();
  });

  // Other lives of this name: deleted and re-created indices (SPEC §5.4, B11).
  let lives = $state<Hit[]>([]);
  const name = $derived(d?.history.name ?? '');
  $effect(() => {
    if (!name) return;
    const ctrl = new AbortController();
    api.lives(cluster, name, ctrl.signal).then(
      (l) => (lives = (l ?? []).sort((a, b) => Number(a.label) - Number(b.label))),
      () => {},
    );
    return () => ctrl.abort();
  });

  const shardNums = $derived([...new Set((d?.shards ?? []).map((s) => s.shard))].sort((a, b) => a - b));
  function cell(shard: number, node: string): ShardView[] {
    return (d?.shards ?? []).filter((s) => s.shard === shard && (s.node === node || s.reloc_target === node));
  }
  const unassigned = $derived((d?.shards ?? []).filter((s) => !s.node));
  const periods = $derived(d?.history.periods ?? []);
  const created = $derived(d?.history.creation_ms ?? 0);
  const deleted = $derived(periods.length ? (periods[periods.length - 1].to ?? 0) : 0);
  const pathFrom = $derived(Math.max(from, created));
  const pathTo = $derived(deleted && deleted < to ? deleted : to);

  // Documents and sizes of the index over the same period.
  let hist = $state<FamilyPoint[]>([]);
  $effect(() => {
    if (!d || pathTo <= pathFrom) {
      hist = [];
      return;
    }
    const ctrl = new AbortController();
    api.indexHistory(cluster, uuid, pathFrom, pathTo, 300, ctrl.signal).then((p) => (hist = p ?? []), () => (hist = []));
    return () => ctrl.abort();
  });
  const serie = (f: (p: FamilyPoint) => number) => hist.map((p) => ({ ts: p.ts, v: p.indices > 0 ? f(p) : -1 }));
  function settingsDiff(i: number): string[] {
    if (i === 0) return [];
    const a = periods[i - 1].settings ?? {};
    const b = periods[i].settings ?? {};
    const keys = [...new Set([...Object.keys(a), ...Object.keys(b)])].sort();
    return keys.filter((k) => a[k] !== b[k]).map((k) => `${k}: ${a[k] ?? '∅'} → ${b[k] ?? '∅'}`);
  }

  // Tooltip of a copy in the matrix.
  function copyTip(c: ShardView): string {
    return [
      `${c.primary ? t('prirep.primary') : t('prirep.replica')} · ${c.state}`,
      c.reloc_target ? t('index.tip.relocating', { node: c.reloc_target }) : '',
      `${t('col.size')} : ${bytes(c.store_bytes)}`,
    ]
      .filter(Boolean)
      .join('\n');
  }
</script>

<TimeBand {cluster} {at} onchange={(d) => router.set({ at: d ?? null })} />
<p><a href={href(`/c/${enc(cluster)}`, { at })}>← {cluster}</a></p>
{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
{#if !d}
  <p class="muted loading">{t('common.loading')}</p>
{:else}
  <h1 class="mono"><Icon name="index" size={20} /> {t('index.title', { name: d.history.name })}</h1>
  <PageHelp id="index" />
  <p class="muted">
    <a href={href(`/c/${enc(cluster)}/family/${enc(d.history.family)}`, { at })}>{d.history.family}</a>
    · UUID {uuid} · {t('col.created')} <span use:tip={utc(d.history.creation_ms)}>{dateTime(d.history.creation_ms)}</span>
    {#if d.index}· {t('col.status')} {d.index.status} · {num(d.index.docs)} {t('unit.docs')} · {t('col.size')} {bytes(d.index.store_bytes)}{/if}
  </p>
  {#if lives.length > 1}
    <p class="lives">
      {t('index.lives')} :
      {#each lives as l (l.value)}
        {#if l.value === uuid}<strong class="mono">{l.value}</strong>{:else}<a class="mono" href={href(`/c/${enc(cluster)}/index/${enc(l.value)}`, { at })}>{l.value}</a>{/if}
        <span class="muted">({dateTime(Number(l.label))})</span>
      {/each}
    </p>
  {/if}

  <section class="card">
    <h2><Icon name="home" /> {t('index.matrix')} <Info text={t('index.matrixHelp')} /></h2>
    <div class="legend">
      <span><span class="copy primary">P</span> {t('index.legend.primary')}</span>
      <span><span class="copy">R</span> {t('index.legend.replica')}</span>
      <span><span class="copy moving">R</span> {t('index.legend.moving')}</span>
      <span><span class="copy target">R</span> {t('index.legend.target')}</span>
      <span><span class="copy unassigned">R</span> {t('index.legend.unassigned')}</span>
    </div>
    {#if d.shards.length === 0}<p class="muted">{t('common.nodata')}</p>{/if}
    <div class="scroll">
      <table class="matrix">
        <thead>
          <tr>
            <th></th>
            {#each d.nodes as n (n)}<th class="node"><a href={href(`/c/${enc(cluster)}/node/${enc(n)}`, { at })}>{n}</a></th>{/each}
            {#if unassigned.length}<th class="node">{t('col.unassigned')}</th>{/if}
          </tr>
        </thead>
        <tbody>
          {#each shardNums as s (s)}
            <tr>
              <th>{t('index.shardN', { n: s })}</th>
              {#each d.nodes as n (n)}
                <td>
                  {#each cell(s, n) as c (c.primary + (c.node ?? '') + c.state)}
                    <span
                      class="copy"
                      class:primary={c.primary}
                      class:moving={c.state !== 'STARTED'}
                      class:target={c.reloc_target === n}
                      use:tip={copyTip(c)}
                      >{c.primary ? 'P' : 'R'}</span
                    >
                  {/each}
                </td>
              {/each}
              {#if unassigned.length}
                <td>
                  {#each unassigned.filter((c) => c.shard === s) as c, i (i)}
                    <span class="copy unassigned" use:tip={t('index.tip.unassigned', { kind: c.primary ? t('prirep.primary') : t('prirep.replica'), reason: c.reason || '—', date: dateTime(c.unassigned_at) })}>{c.primary ? 'P' : 'R'}</span>
                  {/each}
                </td>
              {/if}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  <div class="periodbar">
    <div class="row period">
      <strong>{t('win.period')}</strong>
      {#each spans as h (h)}
        <button aria-pressed={!custom && span === h} onclick={() => router.set({ span: h === 168 ? null : h, from: null, to: null })}>{spanLabel(h)}</button>
      {/each}
      <label class="row">
        <span class="muted">{t('path.from')}</span>
        <input type="datetime-local" value={local(from)} onchange={(e) => router.set({ from: parse((e.target as HTMLInputElement).value), to })} />
      </label>
      <label class="row">
        <span class="muted">{t('path.to')}</span>
        <input type="datetime-local" value={local(to)} onchange={(e) => router.set({ to: parse((e.target as HTMLInputElement).value), from })} />
      </label>
      <Info text={t('path.periodHelp')} />
    </div>
    {#if pathFrom > from}<p class="muted small">{t('path.clamped', { date: dateTime(created) })}</p>{/if}
    {#if pathTo < to}<p class="muted small">{t('path.clampedEnd', { date: dateTime(deleted) })}</p>{/if}
  </div>

  <section class="card">
    <h2><Icon name="chart" /> {t('index.history')} <span class="muted small">{dateTime(pathFrom)} → {dateTime(pathTo)}</span> <Info text={t('index.historyHelp')} /></h2>
    <div class="charts">
      <Sparkline height={130} label={t('family.docs')} zoom {at} points={serie((p) => p.docs)} format={(v) => num(Math.round(v))} />
      <Sparkline height={130} label={t('family.size')} zoom {at} points={serie((p) => p.store_bytes)} format={(v) => bytes(v)} />
      <Sparkline height={130} label={t('index.priSize')} zoom {at} points={serie((p) => p.pri_store_bytes)} format={(v) => bytes(v)} />
    </div>
  </section>

  <section class="card">
    <h2><Icon name="path" /> {t('path.title')} <span class="muted small">{dateTime(pathFrom)} → {dateTime(pathTo)}</span></h2>
    <p class="muted small">{t('path.help')}</p>
    {#if shardParam !== undefined}<p class="small">{t('path.onlyShard', { n: shardParam })} <a href={href(`/c/${enc(cluster)}/index/${enc(uuid)}`, { at })}>{t('path.allShards')}</a></p>{/if}
    {#if pathTo > pathFrom}<ShardPath {cluster} {uuid} shard={shardParam} from={pathFrom} to={pathTo} />
    {:else}<p class="muted">{t('path.outside')}</p>{/if}
  </section>

  <section class="card">
    <h2><Icon name="sliders" /> {t('index.settings')} <Info text={t('index.settingsHelp')} /></h2>
    <table class="data">
      <thead><tr><th>{t('col.from')}</th><th>{t('col.to')}</th><th>{t('col.status')}</th><th>{t('col.pri')}/{t('col.rep')}</th><th>{t('col.key')}</th></tr></thead>
      <tbody>
        {#each periods as p, i (p.from)}
          <tr>
            <td use:tip={utc(p.from)}>{dateTime(p.from)}</td>
            <td>{p.to ? dateTime(p.to) : t('node.open')}</td>
            <td>{p.status}</td>
            <td>{p.pri}/{p.rep}</td>
            <td class="mono small">
              {#if i === 0}
                {#each Object.entries(p.settings ?? {}).sort() as [k, v] (k)}<div>{k} = {v}</div>{/each}
              {:else}
                {#each settingsDiff(i) as line (line)}<div>{line}</div>{/each}
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </section>
{/if}

<style>
  .periodbar {
    margin: 0.2rem 0 0.8rem;
  }
  .periodbar p {
    margin: 0.2rem 0 0;
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
  .period {
    gap: 0.4rem;
    margin-bottom: 0.5rem;
  }
  .lives {
    font-size: 0.85rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 0.6rem;
  }
  section {
    margin-bottom: 1rem;
  }
  h2 {
    margin-top: 0;
  }
  .scroll {
    overflow-x: auto;
  }
  .matrix {
    border-collapse: collapse;
    font-size: 0.8rem;
  }
  .matrix th,
  .matrix td {
    border: 1px solid var(--border);
    padding: 2px 4px;
    text-align: center;
  }
  .matrix th.node {
    writing-mode: vertical-rl;
    transform: rotate(180deg);
    white-space: nowrap;
    font-weight: 500;
  }
  .copy {
    display: inline-block;
    width: 1.3rem;
    border-radius: 3px;
    background: var(--green);
    color: #fff;
    font-weight: 600;
    margin: 1px;
  }
  .copy:not(.primary) {
    background: transparent;
    color: var(--green);
    border: 1px solid var(--green);
  }
  .copy.moving {
    background: var(--blue);
    border-color: var(--blue);
    color: #fff;
  }
  .copy.target {
    opacity: 0.55;
  }
  .copy.unassigned {
    background: var(--red);
    border-color: var(--red);
    color: #fff;
  }
  .small {
    font-size: 0.8rem;
  }
</style>
