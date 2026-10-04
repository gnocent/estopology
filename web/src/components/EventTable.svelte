<script lang="ts">
  import { tip } from '../lib/tip';
  import Info from './Info.svelte';
  import type { EsEvent } from '../lib/api';
  import { href, enc } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';
  import { dateTime, shortTime, utc } from '../lib/format';
  import { eventTitle, eventSubject, eventDetail } from '../lib/events';

  let { cluster, events }: { cluster: string; events: EsEvent[] } = $props();

  function link(ev: EsEvent): string | null {
    switch (ev.subject_kind) {
      case 'node':
        return href(`/c/${enc(cluster)}/node/${enc(ev.subject_ref)}`, { at: ev.ts_hi });
      case 'index':
        return href(`/c/${enc(cluster)}/index/${enc(ev.subject_ref)}`, { at: ev.ts_hi });
      case 'shard':
        return href(`/c/${enc(cluster)}/index/${enc(ev.subject_ref.split('/')[0])}`, { at: ev.ts_hi });
      default:
        return null;
    }
  }

  function when(ev: EsEvent): string {
    if (!ev.ts_lo) return dateTime(ev.ts_hi);
    const sameDay = new Date(ev.ts_lo).toDateString() === new Date(ev.ts_hi).toDateString();
    return t('common.between', { a: dateTime(ev.ts_lo), b: sameDay ? shortTime(ev.ts_hi) : dateTime(ev.ts_hi) });
  }
</script>

<table class="data">
  <thead>
    <tr>
      <th>{t('events.col.when')} <Info text={t('events.whenHelp')} /></th>
      <th>{t('events.col.event')} <Info text={t('events.sevHelp')} /></th>
      <th>{t('events.col.subject')}</th>
      <th>{t('events.col.detail')}</th>
    </tr>
  </thead>
  <tbody>
    {#each events as ev, i (ev.ts_hi + ev.type + ev.subject_ref + i)}
      {@const l = link(ev)}
      <tr>
        <td class="when" use:tip={ev.ts_lo ? t('events.windowTip', { lo: utc(ev.ts_lo), hi: utc(ev.ts_hi) }) : utc(ev.ts_hi)}>
          {when(ev)}
          {#if ev.exact_ts}<span class="badge exact" use:tip={t('events.exactTip', { date: utc(ev.exact_ts) })}>{t('common.exact')} {dateTime(ev.exact_ts, true)}</span>{/if}
        </td>
        <td><span class="dot" style:background={`var(--sev-${ev.severity})`} use:tip={tk(`events.sev.${ev.severity}`)}></span> {eventTitle(ev)}{#if ev.type === 'node_new_generation'}<Info text={t('generation.help')} />{/if}</td>
        <td class="mono">{#if l}<a href={l}>{eventSubject(ev)}</a>{:else}{eventSubject(ev)}{/if}</td>
        <td class="detail">{eventDetail(ev)}</td>
      </tr>
    {:else}
      <tr><td colspan="4" class="muted">{t('events.none')}</td></tr>
    {/each}
  </tbody>
</table>

<style>
  .when {
    white-space: nowrap;
  }
  .exact {
    margin-left: 0.3rem;
  }
  .detail {
    font-size: 0.85rem;
    overflow-wrap: anywhere;
  }
</style>
