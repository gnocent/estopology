<script lang="ts">
  import { tip } from '../lib/tip';
  // Horizontal bars of validity intervals over [from, to] (presence,
  // generations, attribute periods): a small Gantt chart.
  import { dateTime } from '../lib/format';

  let {
    rows,
    from,
    to,
    at,
  }: {
    rows: { label: string; items: { from: number; to?: number; title?: string; color?: string }[] }[];
    from: number;
    to: number;
    at?: number;
  } = $props();

  const span = $derived(Math.max(1, to - from));
  const left = (ms: number) => `${Math.min(100, Math.max(0, ((ms - from) / span) * 100))}%`;
  const width = (a: number, b?: number) => {
    const s = Math.max(a, from);
    const e = Math.min(b || to, to);
    return `${Math.max(0.3, ((e - s) / span) * 100)}%`;
  };
</script>

<div class="gantt">
  {#each rows as r (r.label)}
    <div class="row-g">
      <span class="lbl mono" use:tip={r.label}>{r.label}</span>
      <div class="lane">
        {#each r.items as it, i (i)}
          <div
            class="bar"
            style:left={left(it.from)}
            style:width={width(it.from, it.to)}
            style:background={it.color ?? 'var(--green)'}
            use:tip={it.title ?? `${dateTime(it.from)} → ${it.to ? dateTime(it.to) : '…'}`}
          ></div>
        {/each}
        {#if at}<div class="at" style:left={left(at)}></div>{/if}
      </div>
    </div>
  {/each}
  <div class="row-g axis">
    <span></span>
    <div class="ends"><span>{dateTime(from)}</span><span>{dateTime(to)}</span></div>
  </div>
</div>

<style>
  .ends {
    display: flex;
    justify-content: space-between;
    font-size: 0.75rem;
    color: var(--muted);
  }
  .gantt {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .row-g {
    display: grid;
    grid-template-columns: min(14rem, 45%) 1fr;
    gap: 0.5rem;
    align-items: center;
  }
  .lbl {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.85rem;
  }
  .lane {
    position: relative;
    height: 16px;
    background: var(--surface-2);
    border-radius: 5px;
  }
  .bar {
    position: absolute;
    top: 2px;
    bottom: 2px;
    border-radius: 3px;
    box-shadow: inset 0 1px 0 rgb(255 255 255 / 25%);
    transition: transform var(--fast) var(--ease);
  }
  .bar:hover {
    transform: scaleY(1.35);
  }
  .at {
    position: absolute;
    top: -2px;
    bottom: -2px;
    width: 2px;
    background: var(--accent);
  }
</style>
