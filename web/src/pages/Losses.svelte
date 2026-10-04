<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';
  import { tip } from '../lib/tip';
  import { api2, type Losses } from '../lib/api';
  import { router, href, enc } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';
  import { dateTime, shortTime, bytes, num } from '../lib/format';
  import { abWindow } from '../lib/window.svelte';
  import WindowPicker from '../components/WindowPicker.svelte';
  import ExportMenu from '../components/ExportMenu.svelte';

  let { cluster }: { cluster: string } = $props();
  const win = $derived(abWindow());
  const showDeleted = $derived(router.query.get('deleted') === '1');

  let l = $state<Losses | null>(null);
  let error = $state('');

  $effect(() => {
    const ctrl = new AbortController();
    l = null;
    api2.losses(cluster, win.a, win.b, ctrl.signal).then(
      (x) => ((l = x), (error = '')),
      (e) => (e as Error).name !== 'AbortError' && (error = (e as Error).message),
    );
    return () => ctrl.abort();
  });

  // Colour of a counter: what was lost is orange, what stayed unavailable
  // red, a promoted replica blue, what was repaired green.
  const tones: Record<string, string> = {
    lost: 'orange',
    primaries_lost: 'orange',
    not_promoted: 'red',
    promoted: 'blue',
    reallocated: 'green',
    still_unassigned: 'red',
  };

  function when(lo: number | undefined, hi: number): string {
    return lo ? t('common.between', { a: dateTime(lo), b: shortTime(hi) }) : dateTime(hi);
  }
</script>

<h1><Icon name="losses" size={20} /> {t('losses.title')}</h1>
<PageHelp id="losses" />
<WindowPicker />
{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
{#if !l}
  {#if !error}<p class="muted loading">{t('common.loading')}</p>{/if}
{:else}
  <div class="row">
    <label class="row"><input type="checkbox" checked={showDeleted} onchange={() => router.set({ deleted: showDeleted ? null : 1 })} /> {t('losses.showDeleted')}</label>
    <span class="spacer"></span>
    <ExportMenu {cluster} what="losses" params={{ a: win.a, b: win.b }} />
  </div>
  <div class="summary">
    {#each Object.entries(l.summary).filter(([k]) => k !== 'deleted_indices') as [k, v] (k)}
      <div class="card stat" class:zero={v === 0} class:bad={v > 0 && (k === 'not_promoted' || k === 'still_unassigned')} data-tone={tones[k]} use:tip={tk(`losses.sumHelp.${k}`)}>
        <div class="v"><span class="ico"><Icon name={`losses.${k}`} size={18} /></span>{num(v)}</div>
        <div class="muted">{tk(`losses.sum.${k}`)}</div>
      </div>
    {/each}
  </div>

  {#if l.truncated}<div class="banner warn">{t('losses.truncated')}</div>{/if}
  {#if (l.groups ?? []).length === 0}<p class="muted">{t('losses.none')}</p>{/if}
  {#each l.groups ?? [] as g (g.kind + g.name)}
    <section class="card group">
      <h2>
        <Icon name={g.kind === 'machine' ? 'machine' : 'node'} />
        {g.kind === 'machine'
          ? t('losses.group.machine', { name: g.name, n: (g.nodes ?? []).length, when: when(g.left_lo, g.left_hi) })
          : t('losses.group.node', { name: g.name, when: when(g.left_lo, g.left_hi) })}
        <span class="badge">{t('losses.copies', { n: g.copies.length + (g.more ?? 0) })}</span>
      </h2>
      <table class="data">
        <thead>
          <tr>
            <th colspan="5">{t('losses.before')} <Info text={t('losses.beforeHelp')} /></th>
            <th>{t('losses.after')} <Info text={t('losses.afterHelp')} /></th>
          </tr>
          <tr class="sub">
            <th>{t('col.node')}</th><th>{t('losses.zone')}</th><th>{t('losses.shardOf')}</th><th>{t('col.copyKind')}</th><th class="num">{t('col.size')}</th><th></th>
          </tr>
        </thead>
        <tbody>
          {#each g.copies as c, i (c.uuid + c.shard + c.primary + i)}
            <tr>
              <td><a href={href(`/c/${enc(cluster)}/node/${enc(c.node)}`, { at: c.ts_lo })}>{c.node}</a></td>
              <td class="muted">{c.zone ?? ''}</td>
              <td class="mono"><a href={href(`/c/${enc(cluster)}/index/${enc(c.uuid)}`, { at: win.b })}>{c.index}</a>[{c.shard}]</td>
              <td>{c.primary ? t('prirep.primary') : t('prirep.replica')}</td>
              <td class="num">{bytes(c.size_bytes)}</td>
              <td class:bad={c.after === 'unassigned'}>
                {#if c.promoted}<span class="badge">{t('after.promoted')}</span>{/if}
                {#if c.after === 'reallocated'}{t('after.reallocated', { node: c.after_node ?? '', date: dateTime(c.after_ts) })}
                {:else if c.after === 'unassigned'}{t('after.unassigned', { reason: c.after_reason ? `(${c.after_reason})` : '' })}{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if g.more}<p class="muted">{t('losses.more', { n: g.more })}</p>{/if}
    </section>
  {/each}

  {#if showDeleted}
    <section class="card group">
      <h2>{t('losses.deleted')} <span class="badge">{(l.deleted ?? []).length + (l.deleted_more ?? 0)}</span></h2>
      <table class="data">
        <tbody>
          {#each l.deleted ?? [] as x (x.uuid)}
            <tr><td>{dateTime(x.ts)}</td><td class="mono">{x.name}</td><td class="muted">{x.family}</td></tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}
{/if}

<style>
  .sub th {
    font-weight: 400;
    color: var(--muted);
    font-size: 0.82rem;
  }
  .summary {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
    gap: 0.6rem;
    margin: 0.6rem 0 0.9rem;
  }
  .stat .v {
    font-size: 1.35rem;
    font-weight: 600;
  }
  .stat.bad .v,
  td.bad {
    color: var(--red);
  }
  .group {
    margin-bottom: 0.9rem;
  }
  .group h2 {
    margin-top: 0;
  }
</style>
