// The A/B window read from the URL (same defaults as WindowPicker).
import { router } from './router.svelte';

const loadedAt = Date.now();

export function abWindow(defaultHours = 1): { a: number; b: number } {
  const b = router.num('b') ?? loadedAt;
  const a = router.num('a') ?? b - defaultHours * 3_600_000;
  return { a, b };
}
