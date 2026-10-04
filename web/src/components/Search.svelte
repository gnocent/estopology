<script lang="ts">
  import { api, type Hit } from '../lib/api';
  import { router, href, enc } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';

  let { cluster }: { cluster?: string } = $props();

  let q = $state('');
  let hits = $state<Hit[]>([]);
  let open = $state(false);
  let active = $state(0);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let ctrl: AbortController | undefined;

  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(run, 120);
  }

  async function run() {
    ctrl?.abort();
    if (q.trim().length < 2) {
      hits = [];
      return;
    }
    ctrl = new AbortController();
    const sh = parseShard(q.trim());
    try {
      if (sh) {
        // "shard N de X à T": the index X, then its shard N (SPEC §8.2.9).
        const found = await api.search(sh.index, cluster, ctrl.signal);
        hits = found
          .filter((h) => h.kind === 'index' && h.value === sh.index)
          .map((h) => ({ ...h, kind: 'shard' as const, value: `${h.value}[${sh.shard}]`, label: h.label, shard: sh.shard, at: sh.at }));
        active = 0;
        open = true;
        return;
      }
      hits = await api.search(q, cluster, ctrl.signal);
      active = 0;
      open = true;
    } catch (e) {
      if ((e as Error).name !== 'AbortError') hits = [];
    }
  }

  // Accepted forms: "shard 3 de logs-x à 2026-09-20 12:00", "shard 3 of logs-x
  // at …", "logs-x[3]", "logs-x[3] @ 2026-09-20 12:00" (local time).
  function parseShard(s: string): { index: string; shard: number; at?: number } | null {
    const date = (d?: string) => {
      if (!d) return undefined;
      const ms = Date.parse(d.trim().replace(' ', 'T'));
      return Number.isNaN(ms) ? undefined : ms;
    };
    let m = s.match(/^shard\s+(\d+)\s+(?:de|of)\s+(\S+)(?:\s+(?:à|a|at)\s+(.+))?$/i);
    if (m) return { index: m[2], shard: Number(m[1]), at: date(m[3]) };
    m = s.match(/^([^\s[]+)\[(\d+)\](?:\s*(?:@|à|at)\s*(.+))?$/i);
    if (m) return { index: m[1], shard: Number(m[2]), at: date(m[3]) };
    return null;
  }

  function target(h: Hit): string {
    const c = `/c/${enc(h.cluster)}`;
    const at = router.query.get('at');
    switch (h.kind) {
      case 'shard':
        return href(`${c}/index/${enc(h.label ?? '')}`, { at: h.at ?? at, shard: h.shard });
      case 'node':
        return href(`${c}/node/${enc(h.value)}`, { at });
      case 'ip':
        return href(`${c}/node/${enc(h.label ?? '')}`, { at });
      case 'machine':
        return href(c, { at, machine: h.value });
      case 'family':
        return href(`${c}/family/${enc(h.value)}`, { at });
      case 'index':
        return href(`${c}/index/${enc(h.label ?? '')}`, { at });
      default:
        return href(`${c}/index/${enc(h.value)}`, { at });
    }
  }

  function choose(h: Hit) {
    open = false;
    q = '';
    hits = [];
    router.go(target(h));
  }

  function key(e: KeyboardEvent) {
    if (!open || hits.length === 0) return;
    if (e.key === 'ArrowDown') {
      active = (active + 1) % hits.length;
      e.preventDefault();
    } else if (e.key === 'ArrowUp') {
      active = (active - 1 + hits.length) % hits.length;
      e.preventDefault();
    } else if (e.key === 'Enter') {
      choose(hits[active]);
      e.preventDefault();
    } else if (e.key === 'Escape') {
      open = false;
    }
  }
</script>

<div class="search">
  <input
    type="search"
    placeholder={t('nav.search')}
    aria-label={t('nav.search')}
    bind:value={q}
    oninput={schedule}
    onkeydown={key}
    onfocus={() => (open = hits.length > 0)}
    onblur={() => setTimeout(() => (open = false), 150)}
  />
  {#if open}
    <ul role="listbox">
      {#each hits as h, i (h.cluster + h.kind + h.value + (h.label ?? ''))}
        <li role="option" aria-selected={i === active}>
          <button class:active={i === active} onmousedown={() => choose(h)}>
            <span class="badge">{tk(`search.kind.${h.kind}`)}</span>
            <span class="value mono">{h.value}</span>
            {#if h.label && h.kind !== 'index' && h.kind !== 'shard'}<span class="muted">{h.label}</span>{/if}
            {#if !cluster}<span class="muted cl">{h.cluster}</span>{/if}
          </button>
        </li>
      {:else}
        <li class="muted none">{t('search.none')}</li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .search {
    position: relative;
  }
  input {
    width: 22rem;
    max-width: 22vw;
  }
  ul {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    width: 32rem;
    max-height: 60vh;
    overflow: auto;
    margin: 0;
    padding: 0.25rem;
    list-style: none;
    background: color-mix(in srgb, var(--surface) 82%, transparent);
    backdrop-filter: blur(16px) saturate(1.5);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-3);
    animation: rise var(--fast) var(--ease);
  }
  li button {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    width: 100%;
    text-align: left;
    border: none;
    background: none;
    box-shadow: none;
    padding: 0.35rem 0.5rem;
  }
  li button:hover {
    background: var(--surface-2);
  }
  li button.active {
    background: var(--accent-soft);
  }
  .value {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cl {
    margin-left: auto;
  }
  .none {
    padding: 0.4rem 0.5rem;
  }
</style>
