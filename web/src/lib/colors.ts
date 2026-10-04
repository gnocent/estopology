// Tile colouring of the physical view (SPEC §8.2.2).
import type { NodeView } from './api';

export type ColorMode = 'status' | 'disk' | 'cpu' | 'heap' | 'shards' | 'version';

export const colorModes: ColorMode[] = ['status', 'disk', 'cpu', 'heap', 'shards', 'version'];

/** Green → yellow → red ramp for 0..1. */
export function ramp(f: number): string {
  const x = Math.min(1, Math.max(0, f));
  const hue = 130 - 130 * x; // 130 green → 0 red
  return `hsl(${hue} 60% 48%)`;
}

const palette = ['#3b82f6', '#8b5cf6', '#14b8a6', '#f59e0b', '#ec4899', '#84cc16', '#06b6d4', '#f97316'];

export function tileColor(n: NodeView, mode: ColorMode, ctx: { maxShards: number; versions: string[] }): string {
  if (n.missing || !n.present) return 'var(--red)';
  switch (mode) {
    case 'status':
      if (n.initializing > 0 || n.relocating_out > 0 || n.relocating_in > 0) return 'var(--blue)';
      if (n.above_high_watermark) return 'var(--orange)';
      return 'var(--green)';
    case 'disk':
      return n.disk_percent < 0 ? 'var(--grey)' : ramp((n.disk_percent - 50) / 45);
    case 'cpu':
      return n.metrics ? ramp(n.metrics.proc_cpu / 100) : 'var(--grey)';
    case 'heap':
      return n.metrics ? ramp((n.metrics.heap_pct - 40) / 55) : 'var(--grey)';
    case 'shards':
      return ramp(ctx.maxShards ? n.shards / ctx.maxShards : 0);
    case 'version': {
      const i = ctx.versions.indexOf(n.version);
      return i < 0 ? 'var(--grey)' : palette[i % palette.length];
    }
  }
}
