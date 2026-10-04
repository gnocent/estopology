<script lang="ts">
  // Health band: worst health per bucket, failed captures hatched grey,
  // event density below (SPEC §8.2.1, §8.2.2).
  import type { Bucket } from '../lib/api';
  import { dateTime, shortTime } from '../lib/format';
  import { t } from '../lib/i18n';
  import { tip } from '../lib/tip';

  let { buckets, height = 18, density = true }: { buckets: Bucket[]; height?: number; density?: boolean } = $props();

  const w = $derived(Math.max(buckets.length, 1));
  const maxEvents = $derived(Math.max(1, ...buckets.map((b) => b.events)));

  function color(b: Bucket): string {
    switch (b.status) {
      case 'green':
        return 'var(--green)';
      case 'yellow':
        return 'var(--yellow)';
      case 'red':
        return 'var(--red)';
      default:
        return 'var(--surface-2)';
    }
  }

  // Tooltip of a slice: period, worst health, failed capture, events.
  function label(b: Bucket): string {
    const lines = [`${dateTime(b.from)} → ${shortTime(b.to)}`, t('band.health', { status: b.status ? t(`health.${b.status}`) : t('health.unknown') })];
    if (b.unknown) lines.push(t('band.failed'));
    lines.push(b.events ? t('band.events', { n: b.events, s: b.severe }) : t('band.noEvents'));
    return lines.join('\n');
  }
</script>

<svg
  class="band"
  viewBox={`0 0 ${w} ${density ? height + 8 : height}`}
  preserveAspectRatio="none"
  role="img"
  style:height={`${density ? height + 8 : height}px`}
>
  <defs>
    <linearGradient id="gloss" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#fff" stop-opacity="0.28" />
      <stop offset="0.55" stop-color="#fff" stop-opacity="0" />
      <stop offset="1" stop-color="#000" stop-opacity="0.1" />
    </linearGradient>
    <pattern id="hatch" width="2" height="4" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
      <rect width="1" height="4" fill="var(--grey)" />
    </pattern>
  </defs>
  {#each buckets as b, i (b.from)}
    <rect x={i} y="0" width="1.02" {height} fill={color(b)} use:tip={label(b)} />
    {#if b.unknown}<rect x={i} y="0" width="1.02" {height} fill="url(#hatch)" pointer-events="none" />{/if}
    {#if density && b.events > 0}
      <rect
        x={i + 0.1}
        y={height + 8 - Math.max(1.5, (b.events / maxEvents) * 7)}
        width="0.8"
        height={Math.max(1.5, (b.events / maxEvents) * 7)}
        fill={b.severe > 0 ? 'var(--red)' : 'var(--muted)'}
      />
    {/if}
  {/each}
  <rect x="0" y="0" width={w} {height} fill="url(#gloss)" pointer-events="none" />
</svg>

<style>
  .band {
    width: 100%;
    display: block;
    border-radius: 5px;
    overflow: hidden;
  }
</style>
