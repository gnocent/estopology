<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import { tip } from '../lib/tip';
  import { api, type ClusterSummary } from '../lib/api';
  import { href, enc } from '../lib/router.svelte';
  import { t } from '../lib/i18n';
  import { ago, num, dateTime, utc } from '../lib/format';
  import { eventTitle, eventSubject } from '../lib/events';
  import Severity from '../components/Severity.svelte';
  import HealthBand from '../components/HealthBand.svelte';
  import HealthLegend from '../components/HealthLegend.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';

  let clusters = $state<ClusterSummary[] | null>(null);
  let error = $state('');
  let now = $state(Date.now());

  async function load() {
    try {
      clusters = await api.clusters();
      error = '';
      now = Date.now();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  $effect(() => {
    load();
    const id = setInterval(load, 60_000);
    return () => clearInterval(id);
  });
</script>

<h1><Icon name="home" size={20} /> {t('home.title')}</h1>
<PageHelp id="home" />
{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
{#if clusters === null}
  <p class="muted loading">{t('common.loading')}</p>
{:else if clusters.length === 0}
  <p class="muted">{t('home.empty')}</p>
{:else}
  <div class="card table">
    <table class="data">
      <thead>
        <tr>
          <th>{t('home.col.cluster')}</th>
          <th>{t('home.col.state')} <Info text={t('severity.help')} /></th>
          <th class="band-col">{t('home.col.band')} <Info text={t('band.help')} /></th>
          <th class="num">{t('home.col.nodes')} <Info text={t('home.nodesHelp')} /></th>
          <th class="num">{t('home.col.indices')}</th>
          <th class="num">{t('home.col.shards')} <Info text={t('home.shardsHelp')} /></th>
          <th class="num">{t('home.col.unassigned')} <Info text={t('home.unassignedHelp')} /></th>
          <th>{t('home.col.versions')}</th>
          <th>{t('home.col.events')} <Info text={t('home.eventsHelp')} /></th>
        </tr>
      </thead>
      <tbody>
        {#each clusters as c (c.id)}
          <tr>
            <td>
              <a href={href(`/c/${enc(c.id)}`)} class="name">{c.name}</a>
              <div class="muted small">
                {#if c.removed}{t('home.removed')}
                {:else if c.suspended}<span use:tip={c.suspend_reason}>⚠ {t('home.suspended')}</span>
                {:else if c.last_capture_ts}<span use:tip={utc(c.last_capture_ts)}>{t('home.lastCapture', { ago: ago(c.last_capture_ts, now, (k, n) => t(k, { n: n ?? 0 })) })}</span>
                {:else}{t('home.noData')}{/if}
              </div>
            </td>
            <td><Severity a={c.assessment} /></td>
            <td class="band-col">{#if c.timeline.length}<HealthBand buckets={c.timeline} />{/if}</td>
            <td class="num">
              {#if c.counts}<span use:tip={t('home.nodesTip', { present: c.counts.nodes_present, total: c.counts.nodes, missing: c.missing_nodes.length })}
                  >{num(c.counts.nodes_present)}{#if c.missing_nodes.length}<span class="missing"> / {num(c.counts.nodes)}</span>{/if}</span
                >{:else}—{/if}
            </td>
            <td class="num">{num(c.counts?.indices)}</td>
            <td class="num">{num(c.counts?.shards)}</td>
            <td class="num" class:bad={(c.counts?.unassigned ?? 0) > 0}>{num(c.counts?.unassigned)}</td>
            <td>{#each c.versions as v (v)}<span class="badge">{v}</span> {/each}</td>
            <td class="events">
              {#each c.recent_events.slice(0, 3) as ev, i (ev.ts_hi + ev.type + ev.subject_ref + i)}
                <div class="ev">
                  <span class="dot" style:background={`var(--sev-${ev.severity})`}></span>
                  <span use:tip={utc(ev.ts_hi)}>{dateTime(ev.ts_hi)}</span>
                  <span>{eventTitle(ev)}</span>
                  <span class="mono muted">{eventSubject(ev)}</span>
                </div>
              {/each}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <HealthLegend />
{/if}

<style>
  .table {
    padding: 0;
    overflow-x: auto;
  }
  .name {
    font-weight: 600;
    font-size: 1.02rem;
  }
  .small {
    font-size: 0.82rem;
  }
  .band-col {
    width: 22%;
    min-width: 180px;
  }
  .missing {
    color: var(--red);
  }
  td.bad {
    color: var(--red);
    font-weight: 600;
  }
  .events {
    font-size: 0.85rem;
  }
  .ev {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    white-space: nowrap;
  }
</style>
