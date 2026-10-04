<script lang="ts">
  import Icon from './Icon.svelte';
  import { tip } from '../lib/tip';
  import { t } from '../lib/i18n';
  import type { Group, NodeView } from '../lib/api';
  import PhysicalGroup from './PhysicalGroup.svelte';
  import { flip } from 'svelte/animate';
  import { fade, scale } from 'svelte/transition';
  import { prefs } from '../lib/prefs.svelte';
  import { groupHref, groupTip, nodeTip } from '../lib/topo';
  import { num, pct } from '../lib/format';

  // Transitions between two dates (SPEC §8.1): nodes appear and disappear,
  // tiles slide to their new place; off when the user prefers reduced motion.
  const ms = $derived(prefs.reducedMotion ? 0 : 250);

  let {
    cluster,
    at,
    group,
    path,
    nodes,
    color,
    highlight,
    large = false,
    current = false,
    onselect,
    depth,
  }: {
    cluster: string;
    at?: number;
    group: Group;
    /** Labels of the groups above this one. */
    path: string[];
    nodes: NodeView[];
    color: (n: NodeView) => string;
    highlight?: string;
    large?: boolean;
    /** The group is the page being shown: its name is not a link. */
    current?: boolean;
    onselect: (name: string, source: HTMLElement) => void;
    depth: number;
  } = $props();

  const mine = $derived([...path, group.label]);

  const leaf = $derived(!group.children || group.children.length === 0);
  const members = $derived((group.nodes ?? []).map((i) => nodes[i]).filter(Boolean));
  const missing = $derived(members.filter((n) => n.missing).length);
</script>

<section
  class="group depth-{Math.min(depth, 3)} kind-{group.kind}"
  class:highlight={highlight && group.kind === 'machine' && group.label === highlight}
  data-tier={group.kind === 'tier' ? group.label : undefined}
>
  <header>
    <span class="muted ico"><Icon name={`group.${group.kind}`} size={depth === 0 ? 16 : 14} /></span>
    {#if group.kind === 'attr' || group.kind === 'tier'}<span class="muted kind">{group.kind === 'attr' ? group.attr : t('group.tier')}</span>{/if}
    {#if current}<span class="label">{group.label}</span>
    {:else}<a class="label" href={groupHref(cluster, mine, { at })} use:tip={groupTip(group, members)}>{group.label}</a>{/if}
    <span class="muted count" use:tip={t('group.count', { n: members.length, missing })}
      >{members.length}{#if group.kind !== 'machine'}&nbsp;{t('group.nodes')}{/if}{#if missing}<span class="miss"> −{missing}</span>{/if}</span
    >
  </header>
  {#if leaf}
    <div class="tiles" class:large>
      {#each members as n (n.name + n.es_id)}
        <button
          class="tile"
          class:large
          class:missing={n.missing}
          style:--c={color(n)}
          use:tip={nodeTip(n)}
          aria-label={n.name}
          onclick={(e) => onselect(n.name, e.currentTarget)}
          animate:flip={{ duration: ms }}
          in:scale={{ duration: ms, start: 0.3 }}
          out:fade={{ duration: ms }}
        >
          {#if large}
            <span class="name mono">{n.name}</span>
            <span class="figs">
              {#if n.missing || !n.present}{t('cluster.missing')}
              {:else}{num(n.shards)} {t('unit.shards')}{#if n.metrics} · {t('node.disk')} {pct(n.disk_percent)} · CPU {pct(n.metrics.proc_cpu)} · {t('node.heap')} {pct(n.metrics.heap_pct)}{/if}{/if}
            </span>
          {/if}
        </button>
      {/each}
    </div>
  {:else}
    <div class="children" class:rows={depth === 0}>
      {#each group.children ?? [] as g (g.kind + g.label)}
        <PhysicalGroup {cluster} {at} group={g} path={mine} {nodes} {color} {highlight} {large} {onselect} depth={depth + 1} />
      {/each}
    </div>
  {/if}
</section>

<style>
  .group {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.4rem 0.5rem 0.5rem;
    background: var(--layer);
    transition: border-color var(--fast) var(--ease);
  }
  /* Tier frames carry the colour of their temperature. */
  .kind-tier {
    --t: var(--muted);
    background: linear-gradient(120deg, color-mix(in srgb, var(--t) 15%, transparent), color-mix(in srgb, var(--t) 3%, transparent) 70%);
    border-color: color-mix(in srgb, var(--t) 28%, var(--border));
  }
  .kind-tier[data-tier='hot'] {
    --t: var(--orange);
  }
  .kind-tier[data-tier='warm'] {
    --t: var(--yellow);
  }
  .kind-tier[data-tier='cold'] {
    --t: var(--blue);
  }
  .kind-tier[data-tier='frozen'] {
    --t: var(--accent-2);
  }
  .kind-tier[data-tier='content'] {
    --t: var(--green);
  }
  .kind-tier[data-tier='master'] {
    --t: var(--violet);
  }
  .kind-tier > header .ico {
    color: var(--t);
  }
  .kind-machine {
    background: var(--glass);
  }
  .depth-0 {
    border-radius: var(--radius-lg);
    padding: 0.6rem 0.7rem 0.7rem;
    background:
      radial-gradient(130% 150% at 0% 0%, var(--tint), transparent 58%),
      linear-gradient(160deg, var(--glass-hi), var(--glass));
    box-shadow:
      inset 0 1px 0 var(--hairline),
      var(--shadow);
  }
  .kind-machine:hover {
    border-color: var(--border-strong);
  }
  .ico {
    color: var(--muted);
    margin-right: 0;
  }
  .depth-0 > header .ico {
    color: var(--accent);
  }
  .highlight {
    outline: 2px solid var(--accent);
  }
  header {
    display: flex;
    gap: 0.4rem;
    align-items: baseline;
    font-size: 0.8rem;
    margin-bottom: 0.3rem;
  }
  .depth-0 > header {
    font-size: 0.95rem;
    font-weight: 600;
  }
  a.label {
    color: var(--text);
    border-radius: 4px;
    padding: 0 0.2rem;
    margin: 0 -0.2rem;
    transition: background-color var(--fast) var(--ease);
  }
  a.label:hover {
    text-decoration: none;
    color: var(--accent);
    background: var(--accent-soft);
  }
  .label {
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 16rem;
  }
  .kind {
    font-size: 0.78rem;
  }
  .miss {
    color: var(--red);
    font-weight: 600;
  }
  .children {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: flex-start;
  }
  .children.rows {
    flex-direction: column;
    align-items: stretch;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(4, 18px);
    gap: 3px;
  }
  .tile {
    width: 18px;
    height: 18px;
    padding: 0;
    background: var(--c);
    border: 1px solid transparent;
    border-radius: 4px;
    box-shadow:
      inset 0 1px 0 rgb(255 255 255 / 28%),
      inset 0 -1px 0 rgb(0 0 0 / 14%);
    transition:
      background-color 0.4s ease,
      transform var(--fast) var(--ease);
  }
  .tile:not(.missing):not(.large) {
    background-image: linear-gradient(150deg, rgb(255 255 255 / 30%), transparent 60%);
  }
  /* Large tiles (page of a group): the name and the figures of the node. */
  .tiles.large {
    grid-template-columns: repeat(auto-fill, minmax(17rem, 1fr));
    gap: 0.45rem;
    min-width: min(36rem, 80vw);
  }
  .tile.large {
    width: auto;
    height: auto;
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    text-align: left;
    padding: 0.45rem 0.6rem 0.45rem 0.7rem;
    border: 1px solid var(--border);
    border-left: 6px solid var(--c);
    border-radius: var(--radius);
    background: linear-gradient(160deg, var(--glass-hi), var(--glass));
    box-shadow: var(--shadow);
  }
  .tile.large:hover {
    transform: translateY(-2px);
    border-color: var(--accent);
    border-left-color: var(--c);
  }
  .tile.large.missing {
    border: 1px dashed var(--red);
    border-left: 6px dashed var(--red);
    color: var(--red);
  }
  .tile .name {
    font-weight: 600;
  }
  .tile .figs {
    font-size: 0.82rem;
    color: var(--muted);
  }
  .tile:hover {
    transform: scale(1.3);
    position: relative;
    z-index: 1;
    border-color: var(--surface);
  }
  .tile.missing {
    background: transparent !important;
    border: 2px dashed var(--red);
    box-shadow: none;
  }
</style>
