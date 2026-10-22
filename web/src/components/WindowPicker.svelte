<script lang="ts">
  // A/B window of the analysis screens, kept in the URL (?a=&b=).
  import { router } from '../lib/router.svelte';
  import { abWindow, clock } from '../lib/window.svelte';
  import { t } from '../lib/i18n';
  import Info from './Info.svelte';

  let { defaultHours = 1 }: { defaultHours?: number } = $props();

  const win = abWindow(() => defaultHours);

  function local(ms: number): string {
    return new Date(ms - new Date(ms).getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
  }
  function parse(v: string): number | null {
    const d = new Date(v).getTime();
    return Number.isFinite(d) ? d : null;
  }
  function preset(hours: number) {
    // A fresh "now", the end of the window: a is always before b.
    clock.refresh();
    router.set({ a: hours === defaultHours ? null : clock.now - hours * 3_600_000, b: null });
  }
  // The preset in use, when the period ends now.
  const current = $derived(router.num('b') === undefined ? Math.round((win.b - win.a) / 3_600_000) : 0);
  const presets = [1, 6, 24, 24 * 7];
</script>

<div class="row picker">
  <strong>{t('win.period')}</strong>
  <label class="row">
    <span class="muted">{t('win.a')}</span>
    <input type="datetime-local" value={local(win.a)} onchange={(e) => router.set({ a: parse((e.target as HTMLInputElement).value) })} />
  </label>
  <label class="row">
    <span class="muted">{t('win.b')}</span>
    <input type="datetime-local" value={local(win.b)} onchange={(e) => router.set({ b: parse((e.target as HTMLInputElement).value) })} />
  </label>
  {#each presets as h (h)}
    <button aria-pressed={current === h} onclick={() => preset(h)}>{h === 1 ? t('win.lastHour') : h < 48 ? t('win.last', { h }) : t('win.days', { d: h / 24 })}</button>
  {/each}
  <Info text={t('win.help')} />
</div>

<style>
  .picker {
    margin-bottom: 0.9rem;
    gap: 0.5rem;
  }
</style>
