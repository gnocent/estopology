<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';
  import { tip } from '../lib/tip';
  import { api2, type ConstraintView, type Page } from '../lib/api';
  import { router, href, enc } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';
  import { reasonText } from '../lib/reasons';
  import TimeBand from '../components/TimeBand.svelte';
  import ExportMenu from '../components/ExportMenu.svelte';

  let { cluster }: { cluster: string } = $props();
  const at = $derived(router.num('at'));
  const status = $derived(router.query.get('status') ?? 'infeasible,tight');
  const offset = $derived(router.num('offset') ?? 0);

  let data = $state<{ counts: Record<string, number>; page: Page<ConstraintView> } | null>(null);
  let error = $state('');

  $effect(() => {
    const ctrl = new AbortController();
    api2.constraints(cluster, { at, status, limit: 200, offset }, ctrl.signal).then(
      (x) => ((data = x), (error = '')),
      (e) => (e as Error).name !== 'AbortError' && (error = (e as Error).message),
    );
    return () => ctrl.abort();
  });

  const selected = $derived(new Set(status.split(',').filter(Boolean)));
  function toggle(s: string) {
    const n = new Set(selected);
    if (n.has(s)) n.delete(s);
    else n.add(s);
    router.set({ status: [...n].join(',') || 'infeasible,tight,ok', offset: null });
  }
</script>

<TimeBand {cluster} {at} onchange={(d) => router.set({ at: d ?? null })} />
<h1><Icon name="constraints" size={20} /> {t('constraints.title')}</h1>
<PageHelp id="constraints" />
{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
<div class="row">
  {#each ['infeasible', 'tight', 'ok'] as s (s)}
    <button aria-pressed={selected.has(s)} onclick={() => toggle(s)} use:tip={tk(`constraint.help.${s}`)}>
      <span class="dot {s}"></span>
      {tk(`constraint.${s}`)}
      {#if data}<span class="muted">({data.counts[s] ?? 0})</span>{/if}
    </button>
  {/each}
  <span class="spacer"></span>
  <ExportMenu {cluster} what="constraints" params={{ at, status }} />
</div>
{#if data}
  <div class="card table">
    <table class="data">
      <thead>
        <tr>
          <th>{t('col.name')}</th><th>{t('col.family')} <Info text={t('family.help')} /></th><th>{t('col.status')}</th><th>{t('col.reason')} <Info text={t('constraints.reasonHelp')} /></th>
          <th class="num">{t('constraints.required')} <Info text={t('constraints.requiredHelp')} /></th>
          <th class="num">{t('constraints.eligible')} <Info text={t('constraints.eligibleHelp')} /></th>
        </tr>
      </thead>
      <tbody>
        {#each data.page.items as c (c.uuid)}
          <tr>
            <td class="mono"><a href={href(`/c/${enc(cluster)}/index/${enc(c.uuid)}`, { at })}>{c.name}</a></td>
            <td>{c.family}</td>
            <td><span class="dot {c.status}"></span> {tk(`constraint.${c.status}`)}</td>
            <td>{reasonText(c.reason, c.params)}</td>
            <td class="num">{c.required}</td>
            <td class="num" class:bad={c.eligible < c.required}>{c.eligible}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if data.page.total > data.page.items.length}
    <div class="row pager">
      <button disabled={offset === 0} onclick={() => router.set({ offset: Math.max(0, offset - 200) || null })}>{t('common.previous')}</button>
      <span class="muted">{offset + 1}–{offset + data.page.items.length} / {data.page.total}</span>
      <button disabled={offset + 200 >= data.page.total} onclick={() => router.set({ offset: offset + 200 })}>{t('common.next')}</button>
    </div>
  {/if}
{/if}

<style>
  td.bad {
    color: var(--red);
    font-weight: 600;
  }
  .small {
    font-size: 0.85rem;
  }
  .table {
    padding: 0;
    overflow-x: auto;
    margin-top: 0.6rem;
  }
  .dot.infeasible {
    background: var(--red);
  }
  .dot.tight {
    background: var(--orange);
  }
  .dot.ok {
    background: var(--green);
  }
  .pager {
    margin-top: 0.6rem;
  }
</style>
