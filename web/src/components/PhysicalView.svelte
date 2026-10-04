<script lang="ts">
  // Physical view (SPEC §8.2.2): the hierarchy (DC → tier → machine → node)
  // as nested boxes, nodes as tiles; missing nodes stay where they were.
  // Every frame and every tile opens the page of what it shows.
  import type { Group, NodeView } from '../lib/api';
  import { tileColor, type ColorMode } from '../lib/colors';
  import { num } from '../lib/format';
  import { t, tk } from '../lib/i18n';
  import Info from './Info.svelte';
  import PhysicalGroup from './PhysicalGroup.svelte';

  let {
    cluster,
    at,
    root,
    base = [],
    nodes,
    mode,
    highlight,
    large = false,
    onselect,
  }: {
    cluster: string;
    at?: number;
    root: Group;
    /** Labels that lead to root (none for the whole cluster). */
    base?: string[];
    nodes: NodeView[];
    mode: ColorMode;
    highlight?: string;
    /** Tiles show the name and the figures of the node. */
    large?: boolean;
    onselect: (name: string, source: HTMLElement) => void;
  } = $props();

  const ctx = $derived({
    maxShards: Math.max(1, ...nodes.map((n) => n.shards)),
    versions: [...new Set(nodes.filter((n) => n.version).map((n) => n.version))].sort(),
  });

  function color(n: NodeView): string {
    return tileColor(n, mode, ctx);
  }

  const ramps = { disk: ['50 %', '95 %'], cpu: ['0 %', '100 %'], heap: ['40 %', '95 %'] } as const;
</script>

<div class="legend">
  {#if mode === 'status'}
    <span><span class="sw" style:background="var(--green)"></span>{t('legend.tile.ok')}</span>
    <span><span class="sw" style:background="var(--blue)"></span>{t('legend.tile.moving')}</span>
    <span><span class="sw" style:background="var(--orange)"></span>{t('legend.tile.watermark')}</span>
  {:else if mode === 'version'}
    {#each ctx.versions as v (v)}<span><span class="sw" style:background={color({ version: v, present: true } as NodeView)}></span>{v}</span>{/each}
  {:else}
    <span>{tk(`cluster.color.${mode}`)} :</span>
    <span><span class="sw ramp"></span>{mode === 'shards' ? t('legend.tile.shards', { n: num(ctx.maxShards) }) : `${ramps[mode][0]} → ${ramps[mode][1]}`}</span>
    <span><span class="sw" style:background="var(--grey)"></span>{t('legend.tile.nodata')}</span>
  {/if}
  <span><span class="sw missing"></span>{t('legend.tile.missing')}</span>
  <Info text={t('physical.help')} />
</div>
<div class="physical">
  {#each root.children ?? [] as g (g.kind + g.label)}
    <PhysicalGroup {cluster} {at} group={g} path={base} {nodes} {color} {highlight} {large} {onselect} depth={0} />
  {:else}
    <!-- A group without sub-groups (a machine): its nodes, in one frame. -->
    <PhysicalGroup {cluster} {at} group={root} path={base.slice(0, -1)} {nodes} {color} {highlight} {large} {onselect} depth={0} current />
  {/each}
</div>

<style>
  .ramp {
    width: 3rem !important;
    background: linear-gradient(90deg, hsl(130 60% 48%), hsl(65 60% 48%), hsl(0 60% 48%));
  }
  .missing {
    background: var(--surface);
    border: 1px dashed var(--red);
  }
  .physical {
    display: flex;
    gap: 0.75rem;
    align-items: flex-start;
    flex-wrap: wrap;
  }
</style>
