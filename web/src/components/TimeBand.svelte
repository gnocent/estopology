<script lang="ts">
  import { tip } from '../lib/tip';
  import HealthLegend from './HealthLegend.svelte';
  import Info from './Info.svelte';
  // Time band of the cluster screen (SPEC §8.2.2): health + event density
  // over a window, a cursor at T. Dragging moves the cursor; the date is
  // committed on release (the view is re-rendered once).
  import { api, type Bucket } from '../lib/api';
  import { dateTime, utc, shortDate } from '../lib/format';
  import { t } from '../lib/i18n';
  import HealthBand from './HealthBand.svelte';

  let {
    cluster,
    at,
    onchange,
    days = 7,
  }: { cluster: string; at: number | undefined; onchange: (at: number | undefined) => void; days?: number } = $props();

  // Now, taken again when T changes and at each action (to the minute: the
  // timeline is not fetched again for nothing).
  let bump = $state(0);
  const now = $derived.by(() => {
    void at;
    void bump;
    return Math.floor(Date.now() / 60_000) * 60_000;
  });
  const cursor = $derived(at ?? now);
  // Window: `days` days ending one day after T (never after now).
  const to = $derived(Math.min(now, cursor + 86_400_000));
  const from = $derived(to - days * 86_400_000);

  let buckets = $state<Bucket[]>([]);
  let drag = $state<number | null>(null);
  let el: HTMLDivElement;

  $effect(() => {
    const ctrl = new AbortController();
    api.timeline(cluster, from, to, 168, ctrl.signal).then((b) => (buckets = b), () => {});
    return () => ctrl.abort();
  });

  const shown = $derived(drag ?? cursor);
  const pos = $derived(((shown - from) / (to - from)) * 100);

  function dateAt(clientX: number): number {
    const r = el.getBoundingClientRect();
    const f = Math.min(1, Math.max(0, (clientX - r.left) / r.width));
    return Math.round(from + f * (to - from));
  }

  // A date at or after now (give or take `slack`) is "now".
  function commit(d: number | undefined, slack = 0) {
    bump++;
    onchange(d === undefined || d >= Date.now() - slack ? undefined : d);
  }

  function down(e: PointerEvent) {
    el.setPointerCapture(e.pointerId);
    bump++;
    drag = dateAt(e.clientX);
  }
  function move(e: PointerEvent) {
    if (drag !== null) drag = dateAt(e.clientX);
  }
  function up() {
    if (drag === null) return;
    const d = drag;
    drag = null;
    commit(d, 60_000);
  }

  function step(ms: number) {
    commit((at ?? Date.now()) + ms);
  }

  const ticks = $derived(Array.from({ length: days + 1 }, (_, i) => from + i * 86_400_000));

  function input(e: Event) {
    const v = (e.target as HTMLInputElement).value;
    if (!v) return;
    const d = new Date(v).getTime();
    if (Number.isFinite(d)) commit(d);
  }

  function localValue(ms: number): string {
    const d = new Date(ms - new Date(ms).getTimezoneOffset() * 60_000);
    return d.toISOString().slice(0, 16);
  }
</script>

<div class="timeband card">
  <div class="row controls">
    <span class="muted">{t('band.viewAt')}</span>
    <strong use:tip={utc(shown)}>{at === undefined && drag === null ? t('common.now') : dateTime(shown, true)}</strong>
    <span class="spacer"></span>
    <button onclick={() => step(-3_600_000)} use:tip={t('band.back1h')}>« 1 h</button>
    <button onclick={() => step(-300_000)} use:tip={t('band.back5m')}>‹ 5 min</button>
    <button onclick={() => step(300_000)} use:tip={t('band.fwd5m')}>5 min ›</button>
    <button onclick={() => step(3_600_000)} use:tip={t('band.fwd1h')}>1 h »</button>
    <input type="datetime-local" value={localValue(cursor)} onchange={input} aria-label="date" />
    <button onclick={() => commit(undefined)} aria-pressed={at === undefined}>{t('common.now')}</button>
  </div>
  <div
    class="track"
    bind:this={el}
    role="slider"
    tabindex="0"
    aria-valuemin={from}
    aria-valuemax={to}
    aria-valuenow={shown}
    aria-label={t('band.drag')}
    onpointerdown={down}
    onpointermove={move}
    onpointerup={up}
    onpointercancel={() => (drag = null)}
    onkeydown={(e) => {
      if (e.key === 'ArrowLeft') step(-300_000);
      if (e.key === 'ArrowRight') step(300_000);
    }}
  >
    <HealthBand {buckets} height={16} />
    <div class="cursor" style:left={`${pos}%`}></div>
  </div>
  <div class="ticks">
    {#each ticks as tk (tk)}
      <span style:left={`${((tk - from) / (to - from)) * 100}%`}>{shortDate(tk)}</span>
    {/each}
  </div>
  <div class="below">
    <HealthLegend />
    <Info text={t('band.help')} />
  </div>
</div>

<style>
  .timeband {
    padding: 0.6rem 0.9rem 0.4rem;
    margin-bottom: 0.9rem;
  }
  .below {
    display: flex;
    align-items: center;
    margin-top: 1.3rem;
  }
  .controls {
    margin-bottom: 0.45rem;
    gap: 0.4rem;
  }
  .track {
    position: relative;
    cursor: ew-resize;
    touch-action: none;
    user-select: none;
  }
  .cursor {
    position: absolute;
    top: -3px;
    bottom: -3px;
    width: 2px;
    margin-left: -1px;
    background: var(--text);
    box-shadow: 0 0 0 1px var(--surface);
    pointer-events: none;
  }
  .cursor::before {
    content: '';
    position: absolute;
    top: -7px;
    left: -5px;
    width: 12px;
    height: 12px;
    border-radius: 50%;
    background: var(--accent);
    border: 2px solid var(--surface);
    box-shadow: var(--shadow);
  }
  .ticks {
    position: relative;
    height: 0.8rem;
    font-size: 0.75rem;
    color: var(--muted);
  }
  .ticks span {
    position: absolute;
    transform: translateX(-50%);
    top: 0.2rem;
  }
</style>
