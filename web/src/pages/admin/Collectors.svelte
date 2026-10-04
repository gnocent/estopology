<script lang="ts">
  import { tip } from '../../lib/tip';
  import { adminApi, type CollectorStatus, type CaptureStatus } from '../../lib/api';
  import { t } from '../../lib/i18n';
  import { dateTime, bytes, utc } from '../../lib/format';

  let rows = $state<CollectorStatus[]>([]);
  let error = $state('');
  let open = $state<string | null>(null);

  async function load() {
    try {
      rows = await adminApi.collectors();
      error = '';
    } catch (e) {
      error = (e as Error).message;
    }
  }
  $effect(() => {
    load();
    const id = setInterval(load, 15_000);
    return () => clearInterval(id);
  });

  // Simultaneous full captures (SPEC §9), applied at restart.
  let parallelism = $state(3);
  let parMsg = $state('');
  $effect(() => {
    adminApi.collectorSettings().then((s) => (parallelism = s.parallelism), () => {});
  });
  async function saveParallelism() {
    parMsg = '';
    try {
      parallelism = (await adminApi.saveCollectorSettings(Number(parallelism))).parallelism;
      parMsg = t('admin.collector.savedRestart');
    } catch (e) {
      parMsg = t('common.error', { msg: (e as Error).message });
    }
  }

  function summary(c?: CaptureStatus): string {
    if (!c) return '—';
    return `${dateTime(c.start_ms, true)} · ${c.status} · ${(c.duration_ms / 1000).toFixed(1)} s`;
  }
</script>

{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
<div class="card row parallel">
  <label class="row"><span>{t('admin.collector.parallelism')}</span><input type="text" inputmode="numeric" size="3" bind:value={parallelism} /></label>
  <button onclick={saveParallelism}>{t('common.save')}</button>
  {#if parMsg}<span class="muted">{parMsg}</span>{/if}
</div>
<div class="card table">
  <table class="data">
    <thead>
      <tr>
        <th>{t('admin.col.cluster')}</th><th>{t('admin.col.lastProbe')}</th><th>{t('admin.col.lastFull')}</th>
        <th>{t('admin.col.next')}</th><th>{t('admin.col.pending')}</th><th>{t('admin.col.errors')}</th><th></th>
      </tr>
    </thead>
    <tbody>
      {#each rows as r (r.id)}
        <tr>
          <td><strong>{r.name}</strong> <span class="muted mono">{r.id}</span>{#if r.suspended}<div class="bad small">{r.suspend_reason}</div>{/if}</td>
          <td class:bad={r.last_probe?.status === 'failed'}>{summary(r.last_probe)}</td>
          <td class:bad={r.last_full && r.last_full.status !== 'ok'}>{summary(r.last_full)} <span class="muted">{r.last_full?.trigger ?? ''}</span></td>
          <td use:tip={utc(r.next_probe_ms)}>{dateTime(r.next_probe_ms, true)}</td>
          <td>{r.pending_trigger ?? ''}</td>
          <td class="small">{(r.recent_errors ?? []).slice(-2).join(' | ')}</td>
          <td><button onclick={() => (open = open === r.id ? null : r.id)}>{open === r.id ? '▲' : '▼'}</button></td>
        </tr>
        {#if open === r.id}
          <tr>
            <td colspan="7">
              <table class="data calls">
                <thead><tr><th>{t('admin.col.call')}</th><th>{t('admin.col.status')}</th><th class="num">{t('admin.col.duration')}</th><th class="num">{t('admin.col.size')}</th><th></th></tr></thead>
                <tbody>
                  {#each r.last_full?.calls ?? [] as c, i (c.name + i)}
                    <tr>
                      <td class="mono">{c.name}</td>
                      <td class:bad={c.status !== 200}>{c.status || c.error}</td>
                      <td class="num">{c.duration_ms} ms</td>
                      <td class="num">{bytes(c.bytes)}{c.gzip ? ` (gzip ${bytes(c.wire_bytes)})` : ''}</td>
                      <td class="muted small">{c.url}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </td>
          </tr>
        {/if}
      {/each}
    </tbody>
  </table>
</div>

<style>
  .parallel {
    margin-bottom: 1rem;
  }
  .table {
    padding: 0;
    overflow-x: auto;
  }
  .bad {
    color: var(--red);
  }
  .small {
    font-size: 0.82rem;
  }
  .calls {
    background: var(--surface-2);
  }
</style>
