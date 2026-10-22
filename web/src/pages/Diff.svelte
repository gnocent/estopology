<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import { api, api2, type DiffResponse, type EsEvent, type NodeDiff, type IndexDiff, type ShardDiff, type SettingDiff, type RemoteDiff, type IndexSettingChange } from '../lib/api';
  import { router, href, enc } from '../lib/router.svelte';
  import { t, tk } from '../lib/i18n';
  import { dateTime, num } from '../lib/format';
  import { abWindow } from '../lib/window.svelte';
  import WindowPicker from '../components/WindowPicker.svelte';
  import EventTable from '../components/EventTable.svelte';
  import ExportMenu from '../components/ExportMenu.svelte';
  import PageHelp from '../components/PageHelp.svelte';
  import Info from '../components/Info.svelte';
  import { tip } from '../lib/tip';

  let { cluster }: { cluster: string } = $props();

  const win = abWindow();
  const tab = $derived(router.query.get('tab') ?? 'path');

  let d = $state<DiffResponse | null>(null);
  let error = $state('');
  let path = $state<EsEvent[]>([]);
  let cursor = $state<{ ts?: number; key?: string }>({});

  $effect(() => {
    const ctrl = new AbortController();
    d = null;
    api2.diff(cluster, win.a, win.b, ctrl.signal).then(
      (x) => ((d = x), (error = '')),
      (e) => (e as Error).name !== 'AbortError' && (error = (e as Error).message),
    );
    return () => ctrl.abort();
  });

  const pathTypes = 'shard_relocated,shard_lost,shard_promoted,shard_assigned,shard_unassigned,node_left,node_joined,machine_lost';
  // Only the answer to the latest request is kept.
  let pathSeq = 0;
  let pathError = $state('');
  async function loadPath(more = false) {
    const n = ++pathSeq;
    if (!more) cursor = {};
    try {
      const r = await api.events(cluster, {
        from: win.a,
        to: win.b,
        types: pathTypes,
        limit: 300,
        before_ts: more ? cursor.ts : undefined,
        before_key: more ? cursor.key : undefined,
      });
      if (n !== pathSeq) return;
      path = more ? [...path, ...r.events] : r.events;
      cursor = { ts: r.next_ts, key: r.next_key };
      pathError = '';
    } catch (e) {
      if (n === pathSeq) pathError = (e as Error).message;
    }
  }
  $effect(() => {
    if (tab === 'path') loadPath();
  });

  // Extra pages of a truncated section, dropped when the window changes.
  let extra = $state<Record<string, unknown[]>>({});
  let extraSeq = 0;
  async function more(section: string, loaded: number) {
    const n = extraSeq;
    try {
      const p = await api2.diffSection<unknown>(cluster, win.a, win.b, section, loaded, 500);
      if (n !== extraSeq) return;
      extra = { ...extra, [section]: [...(extra[section] ?? []), ...p.items] };
    } catch (e) {
      if (n === extraSeq) error = (e as Error).message;
    }
  }
  $effect(() => {
    void cluster;
    void win.a;
    void win.b;
    extraSeq++;
    extra = {};
  });

  const s = $derived(d?.diff.summary);
  // Colour of a counter: what appeared is green, what went away red or
  // orange, what changed blue, settings violet, remotes cyan.
  const tones: Record<string, string> = {
    nodes_added: 'green',
    indices_added: 'green',
    shards_new: 'green',
    shards_promoted: 'blue',
    nodes_removed: 'red',
    shards_lost: 'red',
    indices_removed: 'orange',
    nodes_changed: 'blue',
    indices_changed: 'blue',
    shards_moved: 'blue',
    settings_changed: 'violet',
    cluster_settings_changed: 'violet',
    remotes_changed: 'cyan',
  };
  const nodes = $derived([...(d?.diff.nodes ?? []), ...((extra.nodes as NodeDiff[]) ?? [])]);
  const indices = $derived([...(d?.diff.indices ?? []), ...((extra.indices as IndexDiff[]) ?? [])]);
  const shards = $derived([...(d?.diff.shards ?? []), ...((extra.shards as ShardDiff[]) ?? [])]);
  const settings = $derived(d?.diff.cluster_settings ?? ([] as SettingDiff[]));
  const remotes = $derived(d?.diff.remotes ?? ([] as RemoteDiff[]));
  // Index settings: loaded when the tab opens (one line per index and key).
  let idxSettings = $state<IndexSettingChange[]>([]);
  $effect(() => {
    if (tab !== 'settings') return;
    const ctrl = new AbortController();
    api2.diffSection<IndexSettingChange>(cluster, win.a, win.b, 'settings', 0, 500, ctrl.signal).then(
      (p) => (idxSettings = p.items ?? []),
      () => {},
    );
    return () => ctrl.abort();
  });
  const allIdxSettings = $derived([...idxSettings, ...((extra.settings as IndexSettingChange[]) ?? [])]);
  const tabs = ['path', 'nodes', 'indices', 'shards', 'settings', 'cluster_settings', 'remotes'] as const;
</script>

<h1><Icon name="diff" size={20} /> {t('diff.title')}</h1>
<PageHelp id="diff" />
<WindowPicker />
{#if error}<div class="banner warn">{t('common.error', { msg: error })}</div>{/if}
{#if !d || !s}
  {#if !error}<p class="muted loading">{t('common.loading')}</p>{/if}
{:else}
  <div class="row">
    <p class="muted small">{t('diff.captures', { a: dateTime(d.diff.capture_a), b: dateTime(d.diff.capture_b) })} <Info text={t('diff.capturesHelp')} /></p>
    <span class="spacer"></span>
    <ExportMenu {cluster} what="diff" params={{ a: win.a, b: win.b }} />
  </div>
  <div class="summary" style:--n={Object.keys(s).length} style:--half={Math.ceil(Object.keys(s).length / 2)}>
    {#each Object.entries(s) as [k, v] (k)}
      <div class="card stat" class:zero={v === 0} data-tone={tones[k]} use:tip={tk(`sumHelp.${k}`)}>
        <div class="v"><span class="ico"><Icon name={`sum.${k}`} size={18} /></span>{num(v)}</div>
        <div class="muted">{tk(`sum.${k}`)}</div>
      </div>
    {/each}
  </div>

  <div class="tabs row">
    {#each tabs as tb (tb)}
      <button aria-pressed={tab === tb} onclick={() => router.set({ tab: tb === 'path' ? null : tb })}><Icon name={`diff.${tb}`} /> {tk(`diff.tab.${tb}`)}</button>
    {/each}
  </div>

  <div class="card">
    <p class="muted tabhelp">{tk(`diff.help.${tab}`)}</p>
    {#if tab === 'path'}
      {#if pathError}<div class="banner warn">{t('common.error', { msg: pathError })}</div>{/if}
      <EventTable {cluster} events={path} />
      {#if cursor.ts}<button class="more" onclick={() => loadPath(true)}>{t('events.more')}</button>{/if}
    {:else if tab === 'nodes'}
      <table class="data">
        <thead><tr><th>{t('col.node')}</th><th>{t('col.change')}</th><th>{t('node.machine')}</th><th>{t('col.detail')}</th></tr></thead>
        <tbody>
          {#each nodes as n (n.name + n.es_id + n.change)}
            <tr>
              <td><a href={href(`/c/${enc(cluster)}/node/${enc(n.name)}`, { at: win.b })}>{n.name}</a></td>
              <td><span class="badge ch-{n.change}">{tk(`change.${n.change}`)}</span></td>
              <td class="muted">{n.machine}</td>
              <td class="small">{(n.changes ?? []).map((c) => `${c.key}: ${c.from || '∅'} → ${c.to || '∅'}`).join(' ; ')}</td>
            </tr>
          {:else}<tr><td class="muted">{t('diff.none')}</td></tr>{/each}
        </tbody>
      </table>
      {#if d.totals.Nodes > nodes.length}<button class="more" onclick={() => more('nodes', nodes.length)}>{t('common.more', { n: d.totals.Nodes - nodes.length })}</button>{/if}
    {:else if tab === 'indices'}
      <table class="data">
        <thead><tr><th>{t('col.name')}</th><th>{t('col.change')}</th><th>{t('col.family')} <Info text={t('family.help')} /></th><th>{t('col.detail')}</th></tr></thead>
        <tbody>
          {#each indices as x (x.uuid + x.change)}
            <tr>
              <td class="mono"><a href={href(`/c/${enc(cluster)}/index/${enc(x.uuid)}`, { at: win.b })}>{x.name}</a></td>
              <td><span class="badge ch-{x.change}">{tk(`change.${x.change}`)}</span></td>
              <td class="muted">{x.family}</td>
              <td class="small">{(x.changes ?? []).map((c) => `${c.key}: ${c.from || '∅'} → ${c.to || '∅'}`).join(' ; ')}</td>
            </tr>
          {:else}<tr><td class="muted">{t('diff.none')}</td></tr>{/each}
        </tbody>
      </table>
      {#if d.totals.Indices > indices.length}<button class="more" onclick={() => more('indices', indices.length)}>{t('common.more', { n: d.totals.Indices - indices.length })}</button>{/if}
    {:else if tab === 'shards'}
      <table class="data">
        <thead><tr><th>{t('col.name')}</th><th>{t('col.shard')}</th><th>{t('col.copyKind')}</th><th>{t('col.change')}</th><th>{t('col.before')}</th><th>{t('col.after')}</th></tr></thead>
        <tbody>
          {#each shards as x, i (x.uuid + x.shard + x.primary + x.change + i)}
            <tr>
              <td class="mono"><a href={href(`/c/${enc(cluster)}/index/${enc(x.uuid)}`, { at: win.b })}>{x.index}</a></td>
              <td>{x.shard}</td>
              <td>{x.primary ? t('prirep.primary') : t('prirep.replica')}</td>
              <td><span class="badge ch-{x.change}">{tk(`change.${x.change}`)}</span></td>
              <td>{(x.from ?? []).join(', ')}</td>
              <td>{(x.to ?? []).join(', ')}</td>
            </tr>
          {:else}<tr><td class="muted">{t('diff.none')}</td></tr>{/each}
        </tbody>
      </table>
      {#if d.totals.Shards > shards.length}<button class="more" onclick={() => more('shards', shards.length)}>{t('common.more', { n: d.totals.Shards - shards.length })}</button>{/if}
    {:else if tab === 'settings'}
      <table class="data">
        <thead><tr><th>{t('col.name')}</th><th>{t('col.key')}</th><th>{t('col.valueA')}</th><th>{t('col.valueB')}</th></tr></thead>
        <tbody>
          {#each allIdxSettings as c, i (c.uuid + c.key + i)}
            <tr>
              <td class="mono"><a href={href(`/c/${enc(cluster)}/index/${enc(c.uuid)}`, { at: win.b })}>{c.index}</a></td>
              <td class="mono">{c.key}</td><td>{c.from || '∅'}</td><td>{c.to || '∅'}</td>
            </tr>
          {:else}<tr><td class="muted">{t('diff.none')}</td></tr>{/each}
        </tbody>
      </table>
      {#if d.totals.Settings > allIdxSettings.length}<button class="more" onclick={() => more('settings', allIdxSettings.length)}>{t('common.more', { n: d.totals.Settings - allIdxSettings.length })}</button>{/if}
    {:else if tab === 'cluster_settings'}
      <table class="data">
        <thead><tr><th>{t('col.key')}</th><th>{t('col.valueA')}</th><th>{t('col.valueB')}</th></tr></thead>
        <tbody>
          {#each settings as c (c.key)}
            <tr><td class="mono">{c.key}</td><td>{c.from || '∅'}</td><td>{c.to || '∅'}</td></tr>
          {:else}<tr><td class="muted">{t('diff.none')}</td></tr>{/each}
        </tbody>
      </table>
    {:else}
      <table class="data">
        <tbody>
          {#each remotes as r (r.alias)}
            <tr><td>{r.alias}</td><td>{r.from || '∅'}</td><td>→</td><td>{r.to || '∅'}</td></tr>
          {:else}<tr><td class="muted">{t('diff.none')}</td></tr>{/each}
        </tbody>
      </table>
    {/if}
  </div>
{/if}

<style>
  .tabhelp {
    margin: 0 0 0.6rem;
    font-size: 0.9rem;
  }
  .summary {
    display: grid;
    /* Every counter on one line on a wide screen, two lines below, then more. */
    grid-template-columns: repeat(var(--n, 12), minmax(0, 1fr));
    gap: 0.5rem;
    margin-bottom: 0.9rem;
  }
  .summary .stat {
    padding: 0.65rem 0.6rem 0.55rem;
    font-size: 0.85rem;
    line-height: 1.25;
  }
  @media (max-width: 1500px) {
    .summary {
      grid-template-columns: repeat(var(--half, 6), minmax(0, 1fr));
    }
  }
  @media (max-width: 900px) {
    .summary {
      grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    }
  }
  .stat .v {
    font-size: 1.35rem;
    font-weight: 600;
  }
  .stat.zero {
    opacity: 0.55;
  }
  .tabs {
    gap: 0.3rem;
    margin-bottom: 0.6rem;
  }
  .small {
    font-size: 0.85rem;
  }
  .more {
    margin-top: 0.6rem;
  }
  .ch-added,
  .ch-new {
    border-color: var(--green);
  }
  .ch-removed,
  .ch-lost {
    border-color: var(--red);
  }
  .ch-moved,
  .ch-changed,
  .ch-promoted {
    border-color: var(--blue);
  }
</style>
