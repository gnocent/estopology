<script lang="ts">
  import { adminApi, type ClusterConfig } from '../../lib/api';
  import { router } from '../../lib/router.svelte';
  import { t } from '../../lib/i18n';
  import ClusterForm from './ClusterForm.svelte';

  let clusters = $state<ClusterConfig[]>([]);
  let error = $state('');
  const selected = $derived(router.query.get('cluster') ?? '');

  async function load() {
    try {
      clusters = await adminApi.clusters();
      error = '';
    } catch (e) {
      error = (e as Error).message;
    }
  }
  $effect(() => {
    load();
  });

  // Global CA (SPEC §5.6): used by the clusters without their own.
  let caPath = $state('');
  let caMsg = $state('');
  let caErr = $state('');
  $effect(() => {
    adminApi.tls().then((d) => (caPath = d.ca_path ?? ''), () => {});
  });
  async function saveCA() {
    caMsg = caErr = '';
    try {
      const r = await adminApi.saveTLS(caPath);
      caMsg = r.ca_path ? t('admin.tls.saved', { n: r.certificates }) : t('common.saved');
    } catch (e) {
      caErr = (e as Error).message;
    }
  }

  const current = $derived(selected === '__new__' ? null : clusters.find((c) => c.id === selected));
</script>

{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
<div class="card ca row">
  <label class="row"><span>{t('admin.tls.ca')}</span><input type="text" class="mono path" bind:value={caPath} /></label>
  <button onclick={saveCA}>{t('common.save')}</button>
  {#if caMsg}<span class="ok">{caMsg}</span>{/if}
  {#if caErr}<span class="bad">{t('common.error', { msg: caErr })}</span>{/if}
</div>
<div class="layout">
  <nav class="card list">
    {#each clusters as c (c.id)}
      <button class:active={c.id === selected} onclick={() => router.set({ cluster: c.id })}>
        <span class="dot" class:green={c.enabled && !c.suspended} class:red={c.suspended} class:grey={!c.enabled}></span>
        {c.name} <span class="muted mono">{c.id}</span>
      </button>
    {/each}
    <button class:active={selected === '__new__'} onclick={() => router.set({ cluster: '__new__' })}>+ {t('admin.cluster.new')}</button>
  </nav>
  <div>
    {#if selected === '__new__'}
      {#key selected}<ClusterForm onsaved={(id) => (load(), router.set({ cluster: id }))} />{/key}
    {:else if current}
      {#key current.id}<ClusterForm cluster={current} onsaved={() => load()} onremoved={() => (load(), router.set({ cluster: null }))} />{/key}
    {/if}
  </div>
</div>

<style>
  .layout {
    display: grid;
    grid-template-columns: 280px minmax(0, 1fr);
    gap: 1rem;
    align-items: start;
  }
  .list {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding: 0.5rem;
  }
  .list button {
    text-align: left;
    border: none;
    display: flex;
    gap: 0.45rem;
    align-items: center;
  }
  .list button.active {
    background: var(--accent-soft);
  }
  .ca {
    margin-bottom: 1rem;
  }
  .ca .path {
    min-width: 26rem;
  }
  .ok {
    color: var(--green);
  }
  .bad {
    color: var(--red);
  }
</style>
