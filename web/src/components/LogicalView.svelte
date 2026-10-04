<script lang="ts">
  import Icon from './Icon.svelte';
  // Logical view (SPEC §8.2.2): copies per family × node, drawn on a canvas
  // (hundreds of nodes × tens of families). With one column per node, the
  // columns follow the hierarchy (zone, tier, machine): gaps and coloured
  // bands above the grid show which nodes go together. The columns can also
  // be the groups themselves: the copies of their nodes are then added up,
  // and the cells are wide enough to carry the figure.
  import type { Matrix } from '../lib/api';
  import { t } from '../lib/i18n';
  import { prefs } from '../lib/prefs.svelte';
  import Info from './Info.svelte';
  import { tip as hint } from '../lib/tip';

  export interface Column {
    label: string;
    /** What the column is: "machine", "tier", the awareness attribute… */
    kind: string;
    /** Positions in m.nodes of the nodes it adds up. */
    nodes: number[];
    /** The groups above it, drawn as bands over the columns. */
    above?: Placed['path'];
  }

  /** Where a node sits: its position in m.nodes and the groups above it. */
  export interface Placed {
    node: number;
    path: { label: string; kind: string; name: string }[];
  }

  let {
    m,
    columns,
    layout,
    rowKind,
    rowIcon,
    rowHref,
    onfamily,
    onnode,
    oncolumn,
  }: {
    m: Matrix;
    /** Groups shown as columns; none = one column per node. */
    columns?: Column[];
    /** One column per node: the nodes in the order of the hierarchy. */
    layout?: Placed[];
    /** What a row is: "Family" or "Index". */
    rowKind: string;
    /** Icon of a row name: "family" or "index". */
    rowIcon: string;
    /** Page a row name opens. */
    rowHref: (name: string) => string;
    onfamily: (f: string) => void;
    onnode: (n: string) => void;
    oncolumn?: (i: number) => void;
  } = $props();

  // What is drawn: one column per node, or per group with the sums.
  const view = $derived.by(() => {
    if (columns) {
      const sum = (rows: number[][]) => rows.map((row) => columns.map((c) => c.nodes.reduce((a, i) => a + (row[i] ?? 0), 0)));
      return { labels: columns.map((c) => c.label), kind: columns[0]?.kind ?? '', counts: sum(m.counts), primaries: sum(m.primaries), grouped: true, placed: columns.map((c) => ({ node: -1, path: c.above ?? [] })) };
    }
    if (layout?.length) {
      // Nodes the hierarchy does not know (none in practice) go last.
      const seen = new Set(layout.map((p) => p.node));
      const placed = [...layout, ...m.nodes.map((_, i) => ({ node: i, path: [] })).filter((p) => !seen.has(p.node))];
      const pick = (rows: number[][]) => rows.map((row) => placed.map((p) => row[p.node] ?? 0));
      return { labels: placed.map((p) => m.nodes[p.node]), kind: t('col.node'), counts: pick(m.counts), primaries: pick(m.primaries), grouped: false, placed };
    }
    return { labels: m.nodes, kind: t('col.node'), counts: m.counts, primaries: m.primaries, grouped: false, placed: [] as Placed[] };
  });

  const rowH = $derived(view.grouped ? 22 : 18);
  // The names of the rows are real links, in a column that stays in place
  // when the grid scrolls sideways; the canvas only draws the grid.
  const labelW = 0;
  const namesW = 230;
  let hoverRow = $state(-1);
  const rows = $derived(m.families.length);
  const cols = $derived(view.labels.length);
  const cellW = $derived(!view.grouped ? 12 : cols <= 14 ? 78 : cols <= 40 ? 44 : 22);
  const depth = $derived(view.placed.reduce((a, p) => Math.max(a, p.path.length), 0));
  const bandH = 15;

  // Left edge of each column. Between two nodes that do not share a group,
  // a gap: the higher the level that changes, the wider.
  const xs = $derived.by(() => {
    const out: number[] = [];
    let x = 0;
    for (let c = 0; c < cols; c++) {
      if (c > 0 && depth > 0) {
        const a = view.placed[c - 1].path;
        const b = view.placed[c].path;
        let lvl = 0;
        while (lvl < depth && a[lvl]?.label === b[lvl]?.label) lvl++;
        if (lvl < depth) x += [14, 8, 3][Math.min(2, lvl)];
      }
      out.push(x);
      x += cellW;
    }
    return out;
  });
  const gridW = $derived(cols ? xs[cols - 1] + cellW : 0);

  // Bands above the grid: for each level, the runs of columns of one group.
  const bands = $derived.by(() => {
    const out: { level: number; from: number; to: number; label: string; kind: string; n: number }[] = [];
    for (let lvl = 0; lvl < depth; lvl++) {
      let start = 0;
      let n = 0;
      for (let c = 1; c <= cols; c++) {
        const same =
          c < cols && view.placed[c].path.slice(0, lvl + 1).every((g, i) => g.label === view.placed[start].path[i]?.label) && view.placed[c].path.length > lvl;
        if (same) continue;
        const g = view.placed[start].path[lvl];
        if (g) out.push({ level: lvl, from: start, to: c - 1, label: g.label, kind: g.kind, n: n++ });
        start = c;
      }
    }
    return out;
  });

  // Grouped columns are named above the grid, written upwards.
  const bandsH = $derived(depth > 0 ? depth * (bandH + 2) + 4 : 0);
  const headH = $derived(view.grouped ? bandsH + Math.min(170, 14 + 6.2 * Math.max(...view.labels.map((l) => l.length), 4)) : bandsH || 8);
  // Scale of the colours: the named families only. The "others" row adds up
  // many families and would flatten every other row.
  const maxCount = $derived(Math.max(1, ...view.counts.filter((_, r) => m.families[r]).flat()));
  let canvas: HTMLCanvasElement;
  let tip = $state<{ x: number; y: number; text: string } | null>(null);

  // Transition between two dates (SPEC §8.1): each cell moves from its last
  // drawn value to the new one in ~300 ms; never blocking, off when the
  // user prefers reduced motion.
  const key = (f: string, n: string) => `${f}\u0000${n}`;
  let shown = new Map<string, number>(); // values on screen
  let frame = 0;

  $effect(() => {
    const dark = prefs.dark; // redraw on theme change
    const v = view;
    void xs;
    void bands;
    const target = new Map<string, number>();
    m.families.forEach((f, r) => v.labels.forEach((n, c) => target.set(key(f, n), v.counts[r][c])));
    const from = shown;
    cancelAnimationFrame(frame);
    if (prefs.reducedMotion || from.size === 0) {
      draw(dark, (f, n, x) => x);
      shown = target;
      return;
    }
    const start = performance.now();
    const step = (now: number) => {
      const p = Math.min(1, (now - start) / 300);
      const e = 1 - (1 - p) ** 3; // ease-out
      draw(dark, (f, n, x) => (from.get(key(f, n)) ?? 0) + (x - (from.get(key(f, n)) ?? 0)) * e);
      if (p < 1) frame = requestAnimationFrame(step);
    };
    shown = target;
    frame = requestAnimationFrame(step);
    return () => cancelAnimationFrame(frame);
  });

  // Colour of a band: tiers take the colour of their temperature, the other
  // levels alternate two tints so that neighbours stand apart.
  const tierVar: Record<string, string> = { hot: '--orange', warm: '--yellow', cold: '--blue', frozen: '--accent-2', content: '--green', master: '--violet' };
  function bandColor(css: CSSStyleDeclaration, b: { kind: string; label: string; n: number; level: number }): string {
    const v = b.kind === 'tier' ? (tierVar[b.label] ?? '--muted') : b.level === 0 ? (b.n % 2 ? '--accent-2' : '--accent') : '--muted';
    return css.getPropertyValue(v).trim();
  }

  function draw(dark: boolean, value: (family: string, col: string, v: number) => number) {
    const dpr = devicePixelRatio || 1;
    const w = labelW + gridW + 90;
    const h = headH + rows * rowH + 4;
    canvas.width = w * dpr;
    canvas.height = h * dpr;
    canvas.style.width = `${w}px`;
    canvas.style.height = `${h}px`;
    const g = canvas.getContext('2d')!;
    g.scale(dpr, dpr);
    const css = getComputedStyle(document.documentElement);
    const text = css.getPropertyValue('--text').trim();
    const muted = css.getPropertyValue('--muted').trim();
    const empty = css.getPropertyValue('--surface-2').trim();
    const red = css.getPropertyValue('--red').trim();
    const font = css.getPropertyValue('--font').trim() || 'system-ui, sans-serif';
    g.font = `11px ${font}`;
    g.textBaseline = 'middle';
    if (view.grouped) {
      g.fillStyle = text;
      for (let c = 0; c < cols; c++) {
        g.save();
        g.translate(labelW + xs[c] + cellW / 2, headH - 6);
        g.rotate(-Math.PI / 2);
        const l = view.labels[c];
        g.fillText(l.length > 26 ? l.slice(0, 25) + '…' : l, 0, 0);
        g.restore();
      }
    }
    for (const b of bands) {
      const x = labelW + xs[b.from];
      const bw = xs[b.to] + cellW - 1 - xs[b.from];
      const y = b.level * (bandH + 2);
      const last = b.kind === 'machine';
      g.globalAlpha = b.kind === 'tier' ? 0.85 : last ? (b.n % 2 ? 0.28 : 0.5) : 0.8;
      g.fillStyle = bandColor(css, b);
      g.beginPath();
      g.roundRect(x, y, bw, bandH, 3);
      g.fill();
      g.globalAlpha = 1;
      // The name, when the band is wide enough to hold it.
      g.fillStyle = b.kind === 'tier' || !last ? '#fff' : text;
      g.textAlign = 'center';
      if (g.measureText(b.label).width + 6 <= bw) g.fillText(b.label, x + bw / 2, y + bandH / 2 + 0.5);
    }
    for (let r = 0; r < rows; r++) {
      const y = headH + r * rowH;
      for (let c = 0; c < cols; c++) {
        const n = value(m.families[r], view.labels[c], view.counts[r][c]);
        const f = Math.min(1, Math.sqrt(n / maxCount));
        g.fillStyle = n < 0.5 ? empty : dark ? `hsl(215 80% ${20 + 50 * f}%)` : `hsl(215 80% ${88 - 55 * f}%)`;
        g.fillRect(labelW + xs[c], y, cellW - 1, rowH - 1);
        if (cellW >= 40 && n >= 0.5) {
          g.fillStyle = (dark ? f < 0.75 : f > 0.5) ? '#fff' : '#101624';
          g.textAlign = 'center';
          g.fillText(`${Math.round(n)}`, labelW + xs[c] + (cellW - 1) / 2, y + rowH / 2);
        }
      }
      const un = m.unassigned[r];
      if (un > 0) {
        g.fillStyle = red;
        g.textAlign = 'left';
        g.fillText(`${un}`, labelW + gridW + 8, y + rowH / 2);
      }
    }
  }

  // Row and column under the pointer; row -1 = the bands above the grid.
  function hit(e: MouseEvent): { r: number; c: number } | null {
    const rect = canvas.getBoundingClientRect();
    const x = e.clientX - rect.left - labelW;
    const y = e.clientY - rect.top - headH;
    const r = Math.floor(y / rowH);
    let c = -1;
    for (let i = 0; i < cols; i++) {
      if (x >= xs[i] && x < xs[i] + cellW) {
        c = i;
        break;
      }
    }
    if (r >= rows || (r < 0 && (depth === 0 || c < 0))) return null;
    return { r: Math.max(r, -1), c };
  }

  const where = (c: number) => (view.placed[c]?.path ?? []).map((g) => `${g.name} ${g.label}`).join(' · ');

  function move(e: MouseEvent) {
    const h = hit(e);
    hoverRow = h ? h.r : -1;
    if (!h) {
      tip = null;
      return;
    }
    let text: string;
    if (h.r < 0) text = `${view.kind} ${view.labels[h.c]}\n${where(h.c)}`;
    else {
      const fam = m.families[h.r] || t('common.others');
      text =
        h.c < 0
          ? t('matrix.tip.unassigned', { rowKind, family: fam, n: m.unassigned[h.r] })
          : t('matrix.tip.col', { rowKind, family: fam, kind: view.kind, name: view.labels[h.c], n: view.counts[h.r][h.c], p: view.primaries[h.r][h.c] }) +
            (depth > 0 ? `\n${where(h.c)}` : '');
    }
    tip = { x: e.offsetX + 12, y: e.offsetY + 12, text };
  }

  function click(e: MouseEvent) {
    const h = hit(e);
    if (!h || h.r < 0) return;
    if (h.c >= 0 && view.counts[h.r][h.c] > 0) {
      if (view.grouped) oncolumn?.(h.c);
      else onnode(view.labels[h.c]);
    } else if (m.families[h.r]) onfamily(m.families[h.r]);
  }
</script>

<div class="logical">
  <h2><Icon name="home" /> {t('cluster.matrix.title')} <Info text={t('matrix.help')} /></h2>
  <div class="legend">
    <span><span class="sw empty"></span>{t('matrix.legend.none')}</span>
    <span><span class="sw scale"></span>{t('matrix.legend.scale', { n: maxCount })}</span>
    <span><span class="un">12</span> {t('matrix.legend.unassigned')}</span>
    <span>{t('matrix.legend.axesCols', { rowKind, rows, cols, kind: view.kind })}</span>
    {#if depth > 0}<span>{t('matrix.legend.bands')} <Info text={t('matrix.legend.bandsHelp')} /></span>{/if}
  </div>
  <div class="wrap">
    <div class="inner">
      <div class="names" style:width={`${namesW}px`} style:padding-top={`${headH}px`}>
        {#each m.families as f, r (r)}
          {#if f}
            <a class="name" class:active={hoverRow === r} style:height={`${rowH}px`} href={rowHref(f)} use:hint={t('matrix.openRow', { rowKind, name: f })}>
              <Icon name={rowIcon} size={13} /><span class="txt">{f}</span><span class="go">›</span>
            </a>
          {:else}
            <span class="name others" class:active={hoverRow === r} style:height={`${rowH}px`} use:hint={t('matrix.othersHelp')}><span class="txt">{t('common.others')}</span></span>
          {/if}
        {/each}
      </div>
      <div class="grid">
        <canvas
          bind:this={canvas}
          onmousemove={move}
          onmouseleave={() => {
            tip = null;
            hoverRow = -1;
          }}
          onclick={click}
        ></canvas>
        {#if tip}<div class="tip" style:left={`${tip.x}px`} style:top={`${tip.y}px`}>{tip.text}</div>{/if}
      </div>
    </div>
  </div>
</div>

<style>
  .wrap {
    overflow: auto;
  }
  .inner {
    display: flex;
    align-items: flex-start;
    width: max-content;
  }
  .grid {
    position: relative;
  }
  /* Names of the rows: links, kept in view when the grid scrolls sideways. */
  .names {
    position: sticky;
    left: 0;
    z-index: 2;
    flex: none;
    display: flex;
    flex-direction: column;
    padding-right: 0.5rem;
    background: color-mix(in srgb, var(--bg) 82%, transparent);
    backdrop-filter: blur(8px);
  }
  .name {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0 0.45rem;
    border-radius: 5px;
    font-size: 0.82rem;
    color: var(--accent);
    white-space: nowrap;
    transition: background-color var(--fast) var(--ease);
  }
  .name :global(svg) {
    color: var(--muted);
  }
  .name .txt {
    overflow: hidden;
    text-overflow: ellipsis;
    flex: 1;
  }
  .name .go {
    opacity: 0;
    font-weight: 600;
    transition:
      opacity var(--fast) var(--ease),
      transform var(--fast) var(--ease);
  }
  a.name:hover,
  .name.active {
    text-decoration: none;
    background: var(--accent-soft);
  }
  a.name:hover .go {
    opacity: 1;
    transform: translateX(2px);
  }
  a.name:hover :global(svg) {
    color: var(--accent);
  }
  .name.others {
    color: var(--muted);
    font-style: italic;
  }
  canvas {
    cursor: pointer;
    display: block;
  }
  .tip {
    position: absolute;
    pointer-events: none;
    background: var(--surface);
    border: 1px solid var(--border);
    box-shadow: var(--shadow);
    padding: 0.4rem 0.6rem;
    border-radius: 4px;
    font-size: 0.9rem;
    white-space: pre;
    z-index: 5;
  }
  .empty {
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  .scale {
    width: 3rem !important;
    background: linear-gradient(90deg, hsl(215 80% 80%), hsl(215 80% 35%));
  }
  .un {
    color: var(--red);
    font-weight: 600;
  }
</style>
