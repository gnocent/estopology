<script lang="ts">
  // Small line chart for node metrics (SPEC §8.2.3): scale and period shown,
  // value read under the pointer.
  import { dateTime } from '../lib/format';
  import { t } from '../lib/i18n';

  let {
    points,
    max,
    label,
    unit = '',
    at,
    height = 64,
    format,
    zoom = false,
  }: {
    points: { ts: number; v: number }[];
    max?: number;
    label: string;
    unit?: string;
    at?: number;
    height?: number;
    /** How a value is written (default: the number and the unit). */
    format?: (v: number) => string;
    /** Scale from the lowest value instead of zero: small variations of a large figure become visible. */
    zoom?: boolean;
  } = $props();

  const w = 320;
  const h = 64;
  const valid = $derived(points.filter((p) => p.v >= 0));
  const t0 = $derived(valid.length ? valid[0].ts : 0);
  const t1 = $derived(valid.length ? valid[valid.length - 1].ts : 1);
  const peak = $derived(max ?? Math.max(1, ...valid.map((p) => p.v)));
  const lo = $derived(valid.length ? Math.min(...valid.map((p) => p.v)) : 0);
  const hi = $derived(valid.length ? Math.max(...valid.map((p) => p.v)) : 0);
  const x = (ts: number) => ((ts - t0) / Math.max(1, t1 - t0)) * w;
  // Bottom and top of the scale.
  const zoomed = $derived(zoom && valid.length > 0 && hi > lo);
  const base = $derived(zoomed ? Math.max(0, lo - (hi - lo) * 0.15) : 0);
  const top = $derived(zoomed ? hi + (hi - lo) * 0.15 : peak);
  const tick = (v: number) => fmt(top - base >= 20 ? Math.round(v) : v);
  const y = (v: number) => h - 2 - ((v - base) / (top - base)) * (h - 4);
  const path = $derived(valid.map((p, i) => `${i ? 'L' : 'M'}${x(p.ts).toFixed(1)},${y(p.v).toFixed(1)}`).join(''));
  const area = $derived(valid.length ? `${path}L${x(t1).toFixed(1)},${h}L${x(t0).toFixed(1)},${h}Z` : '');
  const last = $derived(valid.length ? valid[valid.length - 1].v : undefined);
  const fmt = (v: number) => (format ? format(v) : `${(Math.round(v * 10) / 10).toLocaleString()}${unit}`);

  let hover = $state<{ ts: number; v: number } | null>(null);
  function move(e: MouseEvent) {
    if (!valid.length) return;
    const r = (e.currentTarget as SVGElement).getBoundingClientRect();
    const ts = t0 + ((e.clientX - r.left) / r.width) * (t1 - t0);
    let best = valid[0];
    for (const p of valid) if (Math.abs(p.ts - ts) < Math.abs(best.ts - ts)) best = p;
    hover = best;
  }
</script>

<figure>
  <figcaption>
    <span class="name">{label}</span>
    {#if hover}
      <span>{dateTime(hover.ts)} : <strong>{fmt(hover.v)}</strong></span>
    {:else if last !== undefined}
      <span class="muted">{t('spark.last')} <strong>{fmt(last)}</strong> · {t('spark.range', { min: fmt(lo), max: fmt(hi) })}</span>
    {:else}
      <span class="muted">{t('spark.none')}</span>
    {/if}
  </figcaption>
  <div class="plot">
    <span class="axis top">{tick(top)}</span>
    {#if height >= 120}<span class="axis mid">{tick((top + base) / 2)}</span>{/if}
    <span class="axis bottom">{tick(base)}</span>
    <svg
      viewBox={`0 0 ${w} ${h}`}
      preserveAspectRatio="none"
      role="img"
      aria-label={label}
      onmousemove={move}
      onmouseleave={() => (hover = null)}
      style:height={`${height}px`}
    >
      {#if height >= 120}
        {#each [0.25, 0.5, 0.75] as f (f)}
          <line x1="0" x2={w} y1={y(base + (top - base) * f)} y2={y(base + (top - base) * f)} stroke="var(--border-strong)" stroke-width="1" stroke-dasharray="3 4" vector-effect="non-scaling-stroke" />
        {/each}
      {/if}
      <defs>
        <linearGradient id="spark-fill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stop-color="var(--accent)" stop-opacity="0.32" />
          <stop offset="1" stop-color="var(--accent)" stop-opacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill="url(#spark-fill)" />
      <path d={path} fill="none" stroke="var(--accent)" stroke-width="1.6" stroke-linejoin="round" vector-effect="non-scaling-stroke" />
      {#if at && at >= t0 && at <= t1}
        <line x1={x(at)} x2={x(at)} y1="0" y2={h} stroke="var(--text)" stroke-width="1" vector-effect="non-scaling-stroke" />
      {/if}
      {#if hover}
        <line x1={x(hover.ts)} x2={x(hover.ts)} y1="0" y2={h} stroke="var(--muted)" stroke-dasharray="2 2" vector-effect="non-scaling-stroke" />
      {/if}
    </svg>
  </div>
  {#if valid.length}<div class="period muted"><span>{dateTime(t0)}</span><span>{dateTime(t1)}</span></div>{/if}
</figure>

<style>
  figure {
    margin: 0;
  }
  figcaption {
    display: flex;
    justify-content: space-between;
    gap: 0.6rem;
    font-size: 0.88rem;
    margin-bottom: 0.15rem;
  }
  .name {
    font-weight: 500;
  }
  .plot {
    position: relative;
  }
  .axis {
    position: absolute;
    left: 4px;
    font-size: 0.72rem;
    color: var(--muted);
    pointer-events: none;
  }
  .top {
    top: 1px;
  }
  .mid {
    top: calc(50% - 0.5em);
  }
  .bottom {
    bottom: 2px;
  }
  svg {
    width: 100%;
    height: 64px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    display: block;
    cursor: crosshair;
  }
  .period {
    display: flex;
    justify-content: space-between;
    font-size: 0.75rem;
  }
</style>
