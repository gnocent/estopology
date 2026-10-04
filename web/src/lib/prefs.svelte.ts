// User preferences kept in the browser: language and theme (SPEC §8.1).

export type Lang = 'fr' | 'en';
export type Theme = 'light' | 'dark' | 'auto';

function load<T extends string>(key: string, allowed: readonly T[], def: T): T {
  try {
    const v = localStorage.getItem(key);
    if (v && (allowed as readonly string[]).includes(v)) return v as T;
  } catch {
    /* storage unavailable */
  }
  return def;
}

function save(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage unavailable */
  }
}

const browserLang: Lang = navigator.language?.toLowerCase().startsWith('en') ? 'en' : 'fr';

class Prefs {
  lang = $state<Lang>(load('estopo.lang', ['fr', 'en'] as const, browserLang));
  theme = $state<Theme>(load('estopo.theme', ['light', 'dark', 'auto'] as const, 'auto'));
  reducedMotion = $state(matchMedia('(prefers-reduced-motion: reduce)').matches);
  private systemDark = $state(matchMedia('(prefers-color-scheme: dark)').matches);
  dark = $derived(this.theme === 'dark' || (this.theme === 'auto' && this.systemDark));

  constructor() {
    matchMedia('(prefers-color-scheme: dark)').addEventListener('change', (e) => (this.systemDark = e.matches));
    matchMedia('(prefers-reduced-motion: reduce)').addEventListener('change', (e) => (this.reducedMotion = e.matches));
  }

  /** Called after a change made by the user (the session saves it in the profile). */
  onChange: (lang: Lang, theme: Theme) => void = () => {};

  setLang(l: Lang) {
    this.apply(l, this.theme);
    this.onChange(this.lang, this.theme);
  }

  cycleTheme() {
    this.apply(this.lang, this.theme === 'auto' ? 'light' : this.theme === 'light' ? 'dark' : 'auto');
    this.onChange(this.lang, this.theme);
  }

  /** Applies preferences (from the browser or the account profile). */
  apply(l: Lang, th: Theme) {
    this.lang = l;
    this.theme = th;
    save('estopo.lang', l);
    save('estopo.theme', th);
    document.documentElement.lang = l;
  }
}

export const prefs = new Prefs();
