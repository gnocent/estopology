// Dates in the browser's local time, with UTC in tooltips (SPEC §8.1);
// sizes and counts.
import { prefs } from './prefs.svelte';

function locale(): string {
  return prefs.lang === 'fr' ? 'fr-FR' : 'en-GB';
}

export function dateTime(ms: number | undefined | null, withSeconds = false): string {
  if (!ms) return '—';
  return new Date(ms).toLocaleString(locale(), {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    ...(withSeconds ? { second: '2-digit' } : {}),
  });
}

export function shortTime(ms: number): string {
  return new Date(ms).toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit' });
}

export function shortDate(ms: number): string {
  return new Date(ms).toLocaleDateString(locale(), { month: '2-digit', day: '2-digit' });
}

/** UTC rendering for tooltips. */
export function utc(ms: number | undefined | null): string {
  if (!ms) return '';
  return new Date(ms).toISOString().replace('T', ' ').replace(/\.\d+Z$/, ' UTC');
}

/** "between 14:05 and 14:10" style window of an event. */
export function window(lo: number | undefined, hi: number, between: (a: string, b: string) => string): string {
  if (!lo) return dateTime(hi);
  const sameDay = new Date(lo).toDateString() === new Date(hi).toDateString();
  return between(sameDay ? dateTime(lo) : dateTime(lo), sameDay ? shortTime(hi) : dateTime(hi));
}

export function bytes(n: number | undefined | null): string {
  if (n === undefined || n === null || n < 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toLocaleString(locale(), { maximumFractionDigits: v < 10 && i > 0 ? 1 : 0 })} ${units[i]}`;
}

export function num(n: number | undefined | null): string {
  if (n === undefined || n === null || n < 0) return '—';
  return n.toLocaleString(locale());
}

export function pct(n: number | undefined | null): string {
  if (n === undefined || n === null || n < 0) return '—';
  return `${n.toLocaleString(locale(), { maximumFractionDigits: 0 })} %`;
}

export function ago(ms: number, now: number, t: (k: 'time.now' | 'time.minutes' | 'time.hours' | 'time.days', n?: number) => string): string {
  const d = Math.max(0, now - ms);
  if (d < 60_000) return t('time.now');
  if (d < 3_600_000) return t('time.minutes', Math.floor(d / 60_000));
  if (d < 86_400_000) return t('time.hours', Math.floor(d / 3_600_000));
  return t('time.days', Math.floor(d / 86_400_000));
}
