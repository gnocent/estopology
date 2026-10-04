<script lang="ts">
  import TimeBand from '../components/TimeBand.svelte';
  import Icon from '../components/Icon.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';
  import { tip } from '../lib/tip';
  import { api, type IndexView, type Page, type FamilyPoint } from '../lib/api';
  import Sparkline from '../components/Sparkline.svelte';
  import { router, href, enc } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';
  import { dateTime, num, bytes } from '../lib/format';

  let { cluster, family }: { cluster: string; family: string } = $props();
  const at = $derived(router.num('at'));
  const offset = $derived(router.num('offset') ?? 0);

  // Documents, size and number of indices over the 7 days that end one day
  // after the chosen date (or now).
  const to = $derived(Math.min(Date.now(), (at ?? Date.now()) + 86_400_000));
  const from = $derived(to - 7 * 86_400_000);
  let hist = $state<FamilyPoint[]>([]);
  $effect(() => {
    const ctrl = new AbortController();
    api.familyHistory(cluster, family, from, to, 300, ctrl.signal).then((p) => (hist = p ?? []), () => (hist = []));
    return () => ctrl.abort();
  });
  // Before the first reading of the family there is nothing to draw.
  const serie = (f: (p: FamilyPoint) => number) => hist.map((p) => ({ ts: p.ts, v: p.indices > 0 ? f(p) : -1 }));

  let page = $state<Page<IndexView> | null>(null);
  let error = $state('');

  $effect(() => {
    const ctrl = new AbortController();
    api.indices(cluster, { at, family, sort: 'creation', limit: 200, offset }, ctrl.signal).then(
      (p) => ((page = p), (error = '')),
      (e) => (e as Error).name !== 'AbortError' && (error = (e as Error).message),
    );
    return () => ctrl.abort();
  });
</script>

<TimeBand {cluster} {at} onchange={(d) => router.set({ at: d ?? null })} />
<p><a href={href(`/c/${enc(cluster)}`, { at, view: 'logical' })}>← {cluster}</a></p>
<h1><Icon name="family" size={20} /> {t('family.title', { name: family })} <Info text={t('family.help')} /></h1>
<PageHelp id="family" />
<section class="card evolution">
  <h2><Icon name="chart" /> {t('family.history')} <Info text={t('family.historyHelp')} /></h2>
  <div class="charts">
    <Sparkline height={130} label={t('family.docs')} zoom {at} points={serie((p) => p.docs)} format={(v) => num(Math.round(v))} />
    <Sparkline height={130} label={t('family.size')} zoom {at} points={serie((p) => p.store_bytes)} format={(v) => bytes(v)} />
    <Sparkline height={130} label={t('family.indexCount')} {at} points={serie((p) => p.indices)} format={(v) => num(Math.round(v))} />
  </div>
</section>
{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
{#if !page}
  <p class="muted loading">{t('common.loading')}</p>
{:else}
  <div class="card table">
    <table class="data">
      <thead>
        <tr>
          <th>{t('col.name')}</th><th>{t('col.health')} <Info text={t('col.healthHelp')} /></th><th>{t('col.status')}</th><th>{t('col.tier')} <Info text={t('col.tierHelp')} /></th>
          <th class="num">{t('col.pri')} <Info text={t('col.priHelp')} /></th><th class="num">{t('col.rep')} <Info text={t('col.repHelp')} /></th><th class="num">{t('col.unassigned')} <Info text={t('col.unassignedHelp')} /></th>
          <th class="num">{t('col.docs')}</th><th class="num">{t('col.size')} <Info text={t('col.sizeHelp')} /></th><th>{t('col.created')}</th>
        </tr>
      </thead>
      <tbody>
        {#each page.items as ix (ix.uuid)}
          <tr>
            <td><a href={href(`/c/${enc(cluster)}/index/${enc(ix.uuid)}`, { at })} class="mono">{ix.name}</a></td>
            <td><span class="dot {ix.health}" use:tip={tk(`health.${ix.health}`)}></span> {tk(`health.${ix.health}`)}</td>
            <td>{ix.status}</td>
            <td>{ix.tier}</td>
            <td class="num">{ix.pri}</td>
            <td class="num">{ix.rep}</td>
            <td class="num">{ix.unassigned || ''}</td>
            <td class="num">{num(ix.docs)}</td>
            <td class="num">{bytes(ix.store_bytes)}</td>
            <td>{dateTime(ix.creation_ms)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if page.total > page.items.length}
    <div class="row pager">
      <button disabled={offset === 0} onclick={() => router.set({ offset: Math.max(0, offset - 200) || null })}>{t('common.previous')}</button>
      <span class="muted">{offset + 1}–{offset + page.items.length} / {page.total}</span>
      <button disabled={offset + 200 >= page.total} onclick={() => router.set({ offset: offset + 200 })}>{t('common.next')}</button>
    </div>
  {/if}
{/if}

<style>
  .evolution {
    margin-bottom: 0.9rem;
  }
  .evolution h2 {
    margin-top: 0;
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
  .table {
    padding: 0;
    overflow-x: auto;
  }
  .pager {
    margin-top: 0.6rem;
  }
</style>
