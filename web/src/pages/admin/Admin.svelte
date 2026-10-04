<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import { router } from '../../lib/router.svelte';
  import { tk, t } from '../../lib/i18n';
  import Clusters from './Clusters.svelte';
  import Collectors from './Collectors.svelte';
  import Storage from './Storage.svelte';
  import Settings from './Settings.svelte';
  import Auth from './Auth.svelte';
  import System from './System.svelte';
  import HA from './HA.svelte';
  import PageHelp from '../../components/PageHelp.svelte';

  const tabs = ['clusters', 'collectors', 'storage', 'settings', 'auth', 'ha', 'system'] as const;
  const tab = $derived(router.query.get('tab') ?? 'clusters');
</script>

<h1><Icon name="admin" size={20} /> {t('admin.title')}</h1>
<div class="row tabs">
  {#each tabs as tb (tb)}
    <button aria-pressed={tab === tb} onclick={() => router.go(`/admin?tab=${tb}`)}><Icon name={`admin.${tb}`} /> {tk(`admin.tab.${tb}`)}</button>
  {/each}
</div>
<PageHelp id={`admin.${tab}`} />
{#if tab === 'clusters'}
  <Clusters />
{:else if tab === 'collectors'}
  <Collectors />
{:else if tab === 'storage'}
  <Storage />
{:else if tab === 'settings'}
  <Settings />
{:else if tab === 'auth'}
  <Auth />
{:else if tab === 'ha'}
  <HA />
{:else}
  <System />
{/if}

<style>
  .tabs {
    gap: 0.3rem;
    margin-bottom: 1rem;
  }
</style>
