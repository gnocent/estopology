<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';
  import { api, type EsEvent, type Meta } from '../lib/api';
  import { router } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';
  import WindowPicker from '../components/WindowPicker.svelte';
  import EventTable from '../components/EventTable.svelte';
  import ExportMenu from '../components/ExportMenu.svelte';
  import { abWindow } from '../lib/window.svelte';

  let { cluster }: { cluster: string } = $props();
  const win = abWindow();
  const q = $derived(router.query);
  const filters = $derived({
    types: q.get('types') ?? '',
    severity: q.get('severity') ?? '',
    node: q.get('node') ?? '',
    machine: q.get('machine') ?? '',
    family: q.get('family') ?? '',
  });

  let meta = $state<Meta | null>(null);
  let events = $state<EsEvent[]>([]);
  let cursor = $state<{ ts?: number; key?: string }>({});
  let error = $state('');

  $effect(() => {
    api.meta().then((m) => (meta = m), () => {});
  });

  // Only the answer to the latest request is kept.
  let seq = 0;
  async function load(more = false) {
    const n = ++seq;
    if (!more) cursor = {};
    try {
      const r = await api.events(cluster, {
        from: win.a,
        to: win.b,
        ...filters,
        limit: 200,
        before_ts: more ? cursor.ts : undefined,
        before_key: more ? cursor.key : undefined,
      });
      if (n !== seq) return;
      events = more ? [...events, ...r.events] : r.events;
      cursor = { ts: r.next_ts, key: r.next_key };
      error = '';
    } catch (e) {
      if (n === seq) error = (e as Error).message;
    }
  }

  $effect(() => {
    void filters;
    void win.a;
    void win.b;
    load();
  });

  const selectedTypes = $derived(new Set(filters.types ? filters.types.split(',') : []));
  function toggleType(ty: string) {
    const s = new Set(selectedTypes);
    if (s.has(ty)) s.delete(ty);
    else s.add(ty);
    router.set({ types: [...s].join(',') || null });
  }
  let text = $state<Record<string, string>>({});
</script>

<h1><Icon name="events" size={20} /> {t('events.title')}</h1>
<PageHelp id="events" />
<WindowPicker />
<div class="card filters">
  <div class="row">
    <label class="row"
      ><span class="muted">{t('events.severity')}</span> <Info text={t('events.sevHelp')} />
      <select value={filters.severity} onchange={(e) => router.set({ severity: (e.target as HTMLSelectElement).value || null })}>
        <option value="">{t('events.allSeverities')}</option>
        {#each [1, 2, 3] as s (s)}<option value={String(s)}>≤ {tk(`severity.${s}`)}</option>{/each}
      </select>
    </label>
    {#each ['node', 'machine', 'family'] as f (f)}
      <label class="row"
        ><span class="muted">{tk(`events.${f}`)}</span>{#if f === 'family'}<Info text={t('family.help')} />{/if}
        <input
          type="text"
          value={filters[f as 'node' | 'machine' | 'family']}
          oninput={(e) => (text[f] = (e.target as HTMLInputElement).value)}
          onchange={() => router.set({ [f]: text[f] || null })}
        />
      </label>
    {/each}
    <span class="spacer"></span>
    <ExportMenu {cluster} what="events" params={{ from: win.a, to: win.b, ...filters }} />
  </div>
  <p class="muted typehelp">{t('events.typesHelp')}</p>
  <div class="types">
    {#each meta?.event_types ?? [] as ty (ty)}
      <button aria-pressed={selectedTypes.has(ty)} onclick={() => toggleType(ty)}>{tk(`event.${ty}`, { from: '', to: '', n: '' }, ty)}</button>
    {/each}
  </div>
</div>
{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
<div class="card">
  <EventTable {cluster} {events} />
  {#if cursor.ts}<button class="more" onclick={() => load(true)}>{t('events.more')}</button>{/if}
</div>

<style>
  .typehelp {
    margin: 0.6rem 0 0.3rem;
    font-size: 0.88rem;
  }
  .filters {
    margin-bottom: 0.9rem;
  }
  .types {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    margin-top: 0.6rem;
  }
  .types button {
    font-size: 0.8rem;
    padding: 0.15rem 0.5rem;
  }
  .more {
    margin-top: 0.6rem;
  }
</style>
