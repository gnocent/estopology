// The A/B window read from the URL (same defaults as WindowPicker).
import { router } from './router.svelte';

// Default end of the window: one "now" shared by the queries and the
// picker, taken again on each change of screen and by refresh().
class Clock {
  bump = $state(0);
  now = $derived.by(() => {
    void router.path;
    void this.bump;
    return Date.now();
  });
  refresh() {
    this.bump++;
  }
}
export const clock = new Clock();

// To be called once, when the component is created: a and b only change
// when their value does (not on every change of the URL).
export function abWindow(defaultHours = () => 1): { readonly a: number; readonly b: number } {
  const b = $derived(router.num('b') ?? clock.now);
  const a = $derived(router.num('a') ?? b - defaultHours() * 3_600_000);
  return {
    get a() {
      return a;
    },
    get b() {
      return b;
    },
  };
}
