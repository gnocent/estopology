<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import { tip } from '../lib/tip';
  import { api, type NodeDetail } from '../lib/api';
  import { router, href, enc } from '../lib/router.svelte';
  import { t } from '../lib/i18n';
  import { dateTime, utc, num } from '../lib/format';
  import Sparkline from '../components/Sparkline.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';
  import Intervals from '../components/Intervals.svelte';
  import Crumbs from '../components/Crumbs.svelte';
  import TimeBand from '../components/TimeBand.svelte';
  import { pathOfNode, groupHref } from '../lib/topo';

  let { cluster, name }: { cluster: string; name: string } = $props();

  const at = $derived(router.num('at'));
  const to = $derived(router.num('to') ?? Math.min(Date.now(), (at ?? Date.now()) + 86_400_000));
  const from = $derived(router.num('from') ?? to - 7 * 86_400_000);

  let d = $state<NodeDetail | null>(null);
  let error = $state('');

  $effect(() => {
    const ctrl = new AbortController();
    api.node(cluster, name, { at, from, to, points: 1200 }, ctrl.signal).then(
      (x) => ((d = x), (error = '')),
      (e) => {
        if ((e as Error).name === 'AbortError') return;
        d = null;
        error = (e as Error).message;
      },
    );
    return () => ctrl.abort();
  });

  // Where the node sits (zone, tier, machine): the way back up.
  let labels = $state<string[]>([]);
  let clusterName = $state('');
  $effect(() => {
    const ctrl = new AbortController();
    api.state(cluster, at, ctrl.signal).then(
      (sv) => {
        const i = sv.nodes.findIndex((n) => n.name === name);
        labels = i >= 0 ? pathOfNode(sv.hierarchy, i) : [];
        clusterName = sv.name;
      },
      () => (labels = []),
    );
    return () => ctrl.abort();
  });

  const gens = $derived(d?.history.generations ?? []);
  const infos = $derived(d?.history.infos ?? []);
  const metrics = $derived(d?.history.metrics ?? []);
  // Versions, roles and attributes, newest period first; what differs from
  // the period before is marked.
  type Tag = { kind: string; text: string; tip?: string; changed: boolean };
  const hiddenAttr = (k: string) => k.startsWith('xpack') || k.includes('config_version');
  const periods = $derived.by(() => {
    const out = infos.map((p, i) => {
      const prev = i > 0 ? infos[i - 1] : undefined;
      const roles = (p.roles ?? '').split(',').map((r) => r.trim()).filter(Boolean);
      const before = new Set((prev?.roles ?? '').split(',').map((r) => r.trim()));
      const tags: Tag[] = [];
      tags.push({ kind: 'version', text: `ES ${p.version}`, tip: t('node.version'), changed: !!prev && prev.version !== p.version });
      if (p.jvm) tags.push({ kind: 'soft', text: `JVM ${p.jvm}`, tip: t('node.jvm'), changed: !!prev && prev.jvm !== p.jvm });
      if (p.os) tags.push({ kind: 'soft', text: p.os, tip: `${t('node.os')}${p.kernel ? ` · ${p.kernel}` : ''}`, changed: !!prev && (prev.os !== p.os || prev.kernel !== p.kernel) });
      const roleTags: Tag[] = roles.map((r) => ({ kind: `role ${r.replace(/^data_/, '')}`, text: r, changed: !!prev && !before.has(r) }));
      const attrTags: Tag[] = Object.entries(p.attrs ?? {})
        .filter(([k]) => !hiddenAttr(k))
        .sort()
        .map(([k, v]) => ({ kind: 'attr', text: `${k} = ${v}`, changed: !!prev && (prev.attrs ?? {})[k] !== v }));
      const moved = !!prev && (prev.machine !== p.machine || prev.ip !== p.ip);
      return { p, tags, roleTags, attrTags, moved };
    });
    return out.reverse();
  });

  let famFilter = $state('');
  const families = $derived((d?.families ?? []).filter((f) => f.family.toLowerCase().includes(famFilter.trim().toLowerCase())));

  const series = (f: (m: (typeof metrics)[number]) => number) => metrics.map((m) => ({ ts: m.ts, v: f(m) }));
</script>

<TimeBand {cluster} {at} onchange={(d) => router.set({ at: d ?? null })} />
<Crumbs {cluster} {clusterName} {labels} current={name} {at} />
<h1><Icon name="node" size={20} /> {t('node.title', { name })}</h1>
<PageHelp id="node" />
{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
{#if !d}
  {#if !error}<p class="muted loading">{t('common.loading')}</p>{/if}
{:else}
  <div class="grid">
    <div class="left">
    <section class="card">
      <h2><Icon name="clock" /> {t('node.presence')} <Info text={t('node.presenceHelp')} /></h2>
      <Intervals
        {from}
        {to}
        at={at}
        rows={gens.map((g) => ({
          label: t('node.generation', { n: g.generation }) + ' · ' + g.es_id.slice(0, 8),
          items: (g.presence ?? []).map((p) => ({ from: p.from, to: p.to })),
        }))}
      />
      <p class="muted small">{t('common.period')} : {dateTime(from)} → {dateTime(to)}</p>
      {#if gens.length > 1}<p class="small">{t('node.generationsNote', { n: gens.length - 1 })} <Info text={t('generation.help')} /></p>{/if}
    </section>

    <section class="card">
      <h2><Icon name="tag" /> {t('node.attributes')} <Info text={t('node.attributesHelp')} /></h2>
      {#snippet period(x: (typeof periods)[number])}
        <div class="tags">
          {#each x.tags as g (g.text)}<span class="tag {g.kind}" class:changed={g.changed} use:tip={g.changed ? `${g.tip ?? g.text}\n${t('node.changed')}` : g.tip}>{g.text}</span>{/each}
        </div>
        <div class="lbl">{t('node.roles')}</div>
        <div class="tags">
          {#each x.roleTags as g (g.text)}<span class="tag {g.kind}" class:changed={g.changed} use:tip={g.changed ? t('node.changed') : undefined}>{g.text}</span>{/each}
        </div>
        {#if x.attrTags.length}
          <div class="lbl">{t('node.attrs')}</div>
          <div class="tags">
            {#each x.attrTags as g (g.text)}<span class="tag {g.kind}" class:changed={g.changed} use:tip={g.changed ? t('node.changed') : undefined}>{g.text}</span>{/each}
          </div>
        {/if}
        <dl class:changed={x.moved}>
          <dt>{t('node.machine')}</dt>
          <dd>{#if x.p.machine && labels.length}<a href={groupHref(cluster, labels, { at })}>{x.p.machine}</a>{:else}{x.p.machine || '—'}{/if}</dd>
          <dt>{t('node.ip')}</dt><dd class="mono">{x.p.ip || '—'}</dd>
          <dt>{t('node.started')}</dt><dd use:tip={utc(x.p.start_ms)}>{dateTime(x.p.start_ms)}</dd>
        </dl>
      {/snippet}
      {#each periods as x, i (x.p.es_id + x.p.from)}
        {#if i === 0}
          <p class="when" use:tip={utc(x.p.from)}>{x.p.to ? t('node.fromTo', { a: dateTime(x.p.from), b: dateTime(x.p.to) }) : t('node.since', { date: dateTime(x.p.from) })}</p>
          {@render period(x)}
        {:else}
          <details class="older">
            <summary use:tip={utc(x.p.from)}>{t('node.fromTo', { a: dateTime(x.p.from), b: dateTime(x.p.to) })} · ES {x.p.version}</summary>
            {@render period(x)}
          </details>
        {/if}
      {:else}
        <p class="muted">{t('common.none')}</p>
      {/each}
    </section>
    </div>

    <section class="card">
      <h2><Icon name="chart" /> {t('node.metrics')} <Info text={t('node.metricsHelp')} /></h2>
      <div class="sparks">
        <Sparkline height={150} label={t('node.cpu')} unit=" %" max={100} {at} points={series((m) => m.proc_cpu)} />
        <Sparkline height={150} label={t('node.heap')} unit=" %" max={100} {at} points={series((m) => m.heap_pct)} />
        <Sparkline
          height={150}
          label={t('node.disk')}
          unit=" %"
          max={100}
          {at}
          points={series((m) => (m.disk_total > 0 ? (m.disk_used * 100) / m.disk_total : -1))}
        />
        <Sparkline height={150} label={t('node.shards')} unit={' ' + t('unit.shards')} {at} points={series((m) => m.shards)} />
      </div>
    </section>


    <section class="card wide">
      <h2><Icon name="shard" /> {t('node.content')} <Info text={t('node.contentHelp')} /></h2>
      {#if d.families.length === 0}<p class="muted">{t('common.none')}</p>
      {:else}<input class="famfilter" type="search" bind:value={famFilter} placeholder={t('content.filter', { n: d.families.length })} />{/if}
      <table class="data">
        <thead><tr><th>{t('col.family')} <Info text={t('family.help')} /></th><th class="num">{t('col.copies')}</th><th class="num">{t('col.pri')}</th></tr></thead>
        <tbody>
          {#each families as f (f.family)}
            <tr>
              <td><a href={href(`/c/${enc(cluster)}/family/${enc(f.family)}`, { at })}>{f.family}</a></td>
              <td class="num">{num(f.copies)}</td>
              <td class="num">{num(f.primaries)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  </div>
{/if}

<style>
  .grid {
    display: grid;
    /* Presence is one short lane; the metric charts take the room. */
    grid-template-columns: minmax(300px, 1fr) minmax(0, 3fr);
    gap: 1rem;
  }
  .wide {
    grid-column: 1 / -1;
    overflow-x: auto;
  }
  .left {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    min-width: 0;
  }
  .when {
    margin: 0 0 0.5rem;
    color: var(--muted);
    font-size: 0.85rem;
  }
  .lbl {
    margin: 0.6rem 0 0.25rem;
    font-size: 0.78rem;
    font-weight: 600;
    letter-spacing: 0.02em;
    color: var(--muted);
  }
  .tags {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }
  /* Tag: tinted by what it says (--c), ringed when it changed. */
  .tag {
    --c: var(--muted);
    display: inline-block;
    padding: 0.12rem 0.55rem;
    border-radius: 999px;
    font-size: 0.82rem;
    font-weight: 500;
    color: color-mix(in srgb, var(--c) 72%, var(--text));
    background: linear-gradient(135deg, color-mix(in srgb, var(--c) 24%, transparent), color-mix(in srgb, var(--c) 10%, transparent));
    border: 1px solid color-mix(in srgb, var(--c) 42%, transparent);
    overflow-wrap: anywhere;
  }
  .tag.version {
    --c: var(--accent);
    font-weight: 600;
  }
  .tag.attr {
    --c: var(--accent-2);
    font-family: var(--mono);
    font-size: 0.78rem;
  }
  .tag.role.hot {
    --c: var(--orange);
  }
  .tag.role.warm {
    --c: var(--yellow);
  }
  .tag.role.cold {
    --c: var(--blue);
  }
  .tag.role.frozen {
    --c: var(--accent-2);
  }
  .tag.role.content {
    --c: var(--green);
  }
  .tag.role.master {
    --c: var(--violet);
  }
  .tag.changed {
    box-shadow: 0 0 0 2px var(--accent-ring);
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.2rem 0.8rem;
    margin: 0.7rem 0 0;
    font-size: 0.88rem;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  dl.changed dd {
    font-weight: 600;
  }
  .older {
    margin-top: 0.6rem;
    padding-top: 0.5rem;
    border-top: 1px solid var(--border);
  }
  .older summary {
    cursor: pointer;
    color: var(--muted);
    font-size: 0.85rem;
    margin-bottom: 0.4rem;
  }
  .famfilter {
    width: min(26rem, 100%);
    margin-bottom: 0.4rem;
  }
  .sparks {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.75rem;
  }
  @media (max-width: 1100px) {
    .grid {
      grid-template-columns: 1fr;
    }
    .sparks {
      grid-template-columns: 1fr;
    }
  }
  .small {
    font-size: 0.8rem;
  }
  h2 {
    margin-top: 0;
  }
</style>
