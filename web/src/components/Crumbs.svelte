<script lang="ts">
  // Where one is in the hierarchy, and the way back up: a clear "zoom out"
  // button to the level above, then the whole path, each level a link.
  import { groupHref } from '../lib/topo';
  import { t } from '../lib/i18n';
  import Icon from './Icon.svelte';

  let {
    cluster,
    clusterName,
    labels,
    current,
    at,
  }: { cluster: string; clusterName?: string; labels: string[]; current: string; at?: number } = $props();

  const root = $derived(clusterName || cluster);
  const parent = $derived(labels.length ? labels[labels.length - 1] : root);
</script>

<nav class="crumbs" aria-label={t('crumbs.label')}>
  <a class="up" href={groupHref(cluster, labels, { at })}>
    <Icon name="zoomout" size={18} />
    <span>{labels.length ? t('crumbs.up', { name: parent }) : t('crumbs.upCluster', { name: parent })}</span>
  </a>
  <span class="trail">
    <a href={groupHref(cluster, [], { at })}>{root}</a>
    {#each labels as lb, i (i)}
      <span class="sep">/</span><a href={groupHref(cluster, labels.slice(0, i + 1), { at })}>{lb}</a>
    {/each}
    <span class="sep">/</span><strong>{current}</strong>
  </span>
</nav>

<style>
  .crumbs {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.5rem 0.9rem;
    margin-bottom: 0.8rem;
  }
  .up {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    padding: 0.4rem 0.85rem 0.4rem 0.65rem;
    border: 1px solid var(--accent);
    border-radius: 999px;
    background: linear-gradient(135deg, var(--accent-soft), color-mix(in srgb, var(--accent-2) 18%, var(--accent-soft)));
    color: var(--accent);
    font-weight: 600;
    box-shadow: var(--shadow);
    transition: transform var(--fast) var(--ease);
  }
  .up:hover {
    text-decoration: none;
    transform: translateY(-1px);
  }
  .up:active {
    transform: scale(0.97);
  }
  .trail {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.35rem;
    color: var(--muted);
  }
  .sep {
    opacity: 0.6;
  }
  .trail strong {
    color: var(--text);
  }
</style>
