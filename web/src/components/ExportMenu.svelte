<script lang="ts">
  import { exportURL } from '../lib/api';
  import { prefs } from '../lib/prefs.svelte';
  import { t } from '../lib/i18n';

  let { cluster, what, params }: { cluster: string; what: string; params: Record<string, string | number | undefined> } = $props();
</script>

<span class="row exports">
  <span class="muted">{t('export.label')}</span>
  {#each ['xlsx', 'csv', 'json'] as f (f)}
    <a class="badge" href={exportURL(cluster, what, { ...params, format: f, lang: prefs.lang })} download>{f.toUpperCase()}</a>
  {/each}
</span>

<style>
  .exports {
    gap: 0.35rem;
  }
</style>
