<script lang="ts">
  import { adminApi, type Storage } from '../../lib/api';
  import { t } from '../../lib/i18n';
  import { bytes, dateTime, num } from '../../lib/format';

  let s = $state<Storage | null>(null);
  let days = $state(30);
  let maxGB = $state(0);
  let message = $state('');
  let error = $state('');

  async function load() {
    try {
      s = await adminApi.storage();
      days = s.retention.max_age_days;
      maxGB = Math.round(s.retention.max_bytes / 2 ** 30);
    } catch (e) {
      error = (e as Error).message;
    }
  }
  $effect(() => {
    load();
  });

  async function save() {
    message = error = '';
    try {
      await adminApi.saveRetention({ max_age_days: Number(days), max_bytes: Number(maxGB) * 2 ** 30 });
      message = t('common.saved');
      await load();
    } catch (e) {
      error = (e as Error).message;
    }
  }
</script>

<div class="card">
  <div class="row">
    <label class="row"><span>{t('admin.retention.days')}</span><input type="text" inputmode="numeric" bind:value={days} size="5" /></label>
    <label class="row"><span>{t('admin.retention.maxBytes')}</span><input type="text" inputmode="numeric" bind:value={maxGB} size="6" /></label>
    <button onclick={save}>{t('common.save')}</button>
    {#if message}<span class="ok">{message}</span>{/if}
    {#if error}<span class="bad">{error}</span>{/if}
  </div>
</div>

{#if s}
  {#if (s.alerts ?? []).length}
    <h2>{t('admin.alerts')}</h2>
    {#each s.alerts ?? [] as a (a.id)}<div class="banner warn">{dateTime(a.ts)} — {a.message}</div>{/each}
  {/if}
  <h2>{t('admin.storage.total')} : {bytes(s.total_bytes)} <span class="muted">({t('admin.storage.meta')} {bytes(s.meta_bytes)})</span></h2>
  <div class="card table">
    <table class="data">
      <thead>
        <tr>
          <th>{t('admin.col.cluster')}</th><th class="num">{t('admin.storage.partitions')}</th><th class="num">{t('admin.col.size')}</th>
          <th class="num">{t('admin.storage.bytesPerDay')}</th><th class="num">{t('admin.storage.proj30')}</th><th class="num">{t('admin.storage.proj1y')}</th>
        </tr>
      </thead>
      <tbody>
        {#each s.clusters as c (c.cluster)}
          <tr>
            <td>{c.cluster}</td>
            <td class="num">{num(c.partitions)}</td>
            <td class="num">{bytes(c.bytes)}</td>
            <td class="num">{bytes(c.bytes_per_day)}</td>
            <td class="num">{bytes(c.projection_30d)}</td>
            <td class="num">{bytes(c.projection_1y)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <h2>{t('admin.storage.partitions')}</h2>
  <div class="card table">
    <table class="data">
      <tbody>
        {#each (s.partitions ?? []).slice().reverse() as p (p.Cluster + p.Week)}
          <tr>
            <td>{p.Cluster}</td><td class="mono">{p.Week}</td><td>{dateTime(p.FromMs)} → {dateTime(p.ToMs)}</td>
            <td class="num">{bytes(p.SizeBytes)}</td><td>{p.Sealed ? '🔒' : ''}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<style>
  .table {
    padding: 0;
    overflow-x: auto;
  }
  .ok {
    color: var(--green);
  }
  .bad {
    color: var(--red);
  }
</style>
