<script lang="ts">
  import { adminApi } from '../../lib/api';
  import { t } from '../../lib/i18n';
  import { bytes, dateTime, num } from '../../lib/format';

  type Sys = Awaited<ReturnType<typeof adminApi.system>>;
  type Audit = Awaited<ReturnType<typeof adminApi.audit>>;
  let sys = $state<Sys | null>(null);
  let audit = $state<Audit>([]);
  let metrics = $state(false);
  let structures = $state(64);
  let cacheMsg = $state('');

  $effect(() => {
    adminApi.system().then((s) => (sys = s), () => {});
    adminApi.audit().then((a) => (audit = a), () => {});
    adminApi.metrics().then((m) => (metrics = m.enabled), () => {});
    adminApi.cache().then((c) => (structures = c.structures), () => {});
  });

  async function saveCache() {
    cacheMsg = '';
    try {
      structures = (await adminApi.saveCache(Number(structures))).structures;
      cacheMsg = t('admin.collector.savedRestart');
    } catch (e) {
      cacheMsg = t('common.error', { msg: (e as Error).message });
    }
  }

  function duration(s: number): string {
    const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
    return `${d ? d + ' j ' : ''}${h} h ${m} min`;
  }
</script>

{#if sys}
  <div class="card">
    <dl>
      <dt>{t('admin.system.version')}</dt><dd>{sys.version} <span class="muted">({sys.commit}, {sys.go_version}, {sys.role})</span></dd>
      <dt>{t('admin.system.uptime')}</dt><dd>{duration(sys.uptime_s)}</dd>
      <dt>{t('admin.system.memory')}</dt><dd>{bytes(sys.heap_bytes)} / {bytes(sys.sys_bytes)}</dd>
      <dt>{t('admin.system.goroutines')}</dt><dd>{num(sys.goroutines)}</dd>
      <dt>{t('admin.system.dataDir')}</dt><dd class="mono">{sys.data_dir}</dd>
      <dt>{t('admin.auth.mode')}</dt><dd>{sys.auth_mode}</dd>
      <dt>{t('admin.cache.title')}</dt>
      <dd>
        <label class="row"><input type="text" inputmode="numeric" size="5" bind:value={structures} /> <button onclick={saveCache}>{t('common.save')}</button> <span class="muted">{cacheMsg}</span></label>
        <span class="muted small">{t('admin.cache.help')}</span>
      </dd>
      <dt>{t('admin.metrics.title')}</dt>
      <dd>
        <label class="row"><input type="checkbox" checked={metrics} onchange={async (e) => (metrics = (await adminApi.saveMetrics((e.target as HTMLInputElement).checked)).enabled)} /> {t('admin.metrics.enable')}</label>
        <span class="muted small">{t('admin.metrics.help')}</span>
      </dd>
    </dl>
  </div>
{/if}
<h2>{t('admin.audit.title')}</h2>
<div class="card table">
  <table class="data">
    <thead><tr><th>{t('events.col.when')}</th><th>{t('admin.audit.actor')}</th><th>{t('admin.audit.action')}</th><th>{t('admin.audit.target')}</th><th>{t('admin.audit.details')}</th></tr></thead>
    <tbody>
      {#each audit as a, i (a.ts + a.action + i)}
        <tr><td>{dateTime(a.ts, true)}</td><td>{a.actor}</td><td class="mono">{a.action}</td><td>{a.target ?? ''}</td><td class="muted">{a.details ?? ''}</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.3rem 1rem;
    margin: 0;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
  }
  .small {
    font-size: 0.82rem;
  }
  .table {
    padding: 0;
    overflow-x: auto;
  }
</style>
