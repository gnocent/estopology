<script lang="ts">
  // Explanation of a screen: what it shows and how to read it. Collapsible;
  // the choice is remembered per screen in the browser.
  import { t, tk } from '../lib/i18n';

  let { id }: { id: string } = $props();

  function stored(): boolean {
    try {
      return localStorage.getItem(`estopo.help.${id}`) !== 'closed';
    } catch {
      return true;
    }
  }
  let open = $state(stored());
  $effect(() => {
    open = stored();
  });
  function toggle(e: Event) {
    open = (e.currentTarget as HTMLDetailsElement).open;
    try {
      localStorage.setItem(`estopo.help.${id}`, open ? 'open' : 'closed');
    } catch {
      /* storage unavailable */
    }
  }
  const lines = $derived(tk(`help.${id}`).split('\n'));
</script>

<details class="help" {open} ontoggle={toggle}>
  <summary>{t('help.title')}</summary>
  <div class="body">
    {#each lines as l, i (i)}
      {#if l.startsWith('- ')}<p class="item">{l.slice(2)}</p>{:else}<p>{l}</p>{/if}
    {/each}
  </div>
</details>

<style>
  .help {
    margin: 0 0 1rem;
    border: 1px solid var(--border);
    border-left: 3px solid var(--accent);
    border-radius: var(--radius);
    background: linear-gradient(100deg, color-mix(in srgb, var(--accent) 16%, transparent), var(--tint-2) 50%, transparent), var(--glass);
    box-shadow: var(--shadow);
    padding: 0.5rem 0.8rem;
    font-size: 0.92rem;
    line-height: 1.45;
  }
  summary {
    cursor: pointer;
    color: var(--accent);
    font-weight: 500;
  }
  .body {
    margin-top: 0.4rem;
    max-width: 70rem;
  }
  p {
    margin: 0.25rem 0;
  }
  .item {
    padding-left: 1.1rem;
    position: relative;
  }
  .item::before {
    content: '•';
    position: absolute;
    left: 0.3rem;
    color: var(--muted);
  }
</style>
