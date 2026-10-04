<script lang="ts">
  import { tip } from '../lib/tip';
  import type { Assessment } from '../lib/api';
  import { tk } from '../lib/i18n';

  let { a, compact = false }: { a?: Assessment; compact?: boolean } = $props();

  const level = $derived(a?.level ?? -1);
  const color = $derived(level === 0 ? 'var(--green)' : level === -1 ? 'var(--grey)' : `var(--sev-${level})`);
  const text = $derived((a?.concerns ?? []).map((c) => tk(`concern.${c.code}`, { n: c.count })).join('\n'));
</script>

<span class="sev" style:--c={color} use:tip={text}>
  <span class="dot" style:background={color}></span>
  {#if !compact}<span>{tk(`level.${level}`)}</span>{/if}
</span>

<style>
  .sev {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-weight: 500;
    white-space: nowrap;
  }
</style>
