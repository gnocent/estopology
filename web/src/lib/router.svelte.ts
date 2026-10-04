// Minimal router on the History API. Every screen state that matters
// (cluster, dates, view, filters) lives in the URL: permalinks (SPEC §8.1).

import { tick } from 'svelte';

export interface Route {
  name: string;
  params: Record<string, string>;
}

const patterns: [string, RegExp, string[]][] = [
  ['home', /^\/$/, []],
  ['cluster', /^\/c\/([^/]+)\/?$/, ['cluster']],
  ['group', /^\/c\/([^/]+)\/g\/(.+)$/, ['cluster', 'rest']],
  ['node', /^\/c\/([^/]+)\/node\/([^/]+)$/, ['cluster', 'name']],
  ['family', /^\/c\/([^/]+)\/family\/([^/]+)$/, ['cluster', 'family']],
  ['index', /^\/c\/([^/]+)\/index\/([^/]+)$/, ['cluster', 'uuid']],
  ['diff', /^\/c\/([^/]+)\/diff$/, ['cluster']],
  ['losses', /^\/c\/([^/]+)\/losses$/, ['cluster']],
  ['events', /^\/c\/([^/]+)\/events$/, ['cluster']],
  ['flows', /^\/c\/([^/]+)\/flows$/, ['cluster']],
  ['constraints', /^\/c\/([^/]+)\/constraints$/, ['cluster']],
  ['admin', /^\/admin\/?$/, []],
];

function match(path: string): Route {
  for (const [name, re, keys] of patterns) {
    const m = re.exec(path);
    if (m) {
      const params: Record<string, string> = {};
      try {
        keys.forEach((k, i) => (params[k] = decodeURIComponent(m[i + 1])));
      } catch {
        return { name: 'notfound', params: {} }; // malformed percent-encoding
      }
      return { name, params };
    }
  }
  return { name: 'notfound', params: {} };
}

// Depth of a screen in the hierarchy: home → cluster → groups (zone, tier,
// machine: one level per label) → node; cluster → family → index.
const depths: Record<string, number> = { home: 0, admin: 0, node: 20, family: 20, index: 21 };
function depth(path: string): number {
  const name = match(path).name;
  if (name === 'group') return path.split('/').length - 3;
  return depths[name] ?? 1;
}

/** Labels of the group shown by a path /c/<cluster>/g/<label>/<label>… */
export function groupLabels(path: string): string[] {
  try {
    return path.split('/').slice(4).filter(Boolean).map(decodeURIComponent);
  } catch {
    return [];
  }
}

class Router {
  path = $state(location.pathname);
  search = $state(location.search);
  route = $derived(match(this.path));
  query = $derived(new URLSearchParams(this.search));

  constructor() {
    addEventListener('popstate', () => this.transition(location.pathname, () => this.sync()));
    // Same-origin links navigate without reloading the page.
    document.addEventListener('click', (e) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const a = (e.target as Element).closest?.('a');
      if (!a || a.target || a.hasAttribute('download') || !a.href) return;
      const url = new URL(a.href);
      if (url.origin !== location.origin || url.pathname.startsWith('/api/')) return;
      e.preventDefault();
      this.go(url.pathname + url.search, false, a.closest('main') ? a : undefined);
    });
  }

  private sync() {
    this.path = location.pathname;
    this.search = location.search;
  }

  /** Navigates; source is the link that was clicked, when there is one. */
  go(href: string, replace = false, source?: HTMLElement) {
    this.transition(new URL(href, location.origin).pathname, () => {
      if (replace) history.replaceState(null, '', href);
      else history.pushState(null, '', href);
      this.sync();
      if (!replace) scrollTo(0, 0);
    }, source);
  }

  // Changing screen says where one goes (SPEC §8.1): going down the hierarchy
  // zooms in, going up zooms out, and the clicked link becomes the title of
  // the screen it opens. Done by the browser (View Transitions); a change of
  // date or filter keeps the same path and is never animated here.
  private transition(toPath: string, apply: () => void, source?: HTMLElement) {
    const doc = document as Document & { startViewTransition?: (cb: () => Promise<void>) => { finished: Promise<void> } };
    const delta = depth(toPath) - depth(this.path);
    if (toPath === this.path || !doc.startViewTransition || matchMedia('(prefers-reduced-motion: reduce)').matches) {
      apply();
      return;
    }
    const root = document.documentElement;
    root.dataset.nav = delta > 0 ? 'down' : delta < 0 ? 'up' : 'same';
    const hero = delta > 0 ? source : undefined;
    if (hero) hero.style.viewTransitionName = 'hero';
    let title: HTMLElement | null = null;
    const tr = doc.startViewTransition(async () => {
      if (hero) hero.style.viewTransitionName = '';
      apply();
      await tick();
      title = hero ? document.querySelector<HTMLElement>('main h1') : null;
      if (title) title.style.viewTransitionName = 'hero';
    });
    tr.finished
      .catch(() => {})
      .then(() => {
        if (title) title.style.viewTransitionName = '';
        delete root.dataset.nav;
      });
  }

  /** Updates query parameters of the current URL (null removes one). */
  set(params: Record<string, string | number | null | undefined>, replace = true) {
    const q = new URLSearchParams(this.search);
    for (const [k, v] of Object.entries(params)) {
      if (v === null || v === undefined || v === '') q.delete(k);
      else q.set(k, String(v));
    }
    const s = q.toString();
    this.go(this.path + (s ? `?${s}` : ''), replace);
  }

  num(name: string): number | undefined {
    const v = this.query.get(name);
    if (v === null || v === '') return undefined;
    const n = Number(v);
    return Number.isFinite(n) ? n : undefined;
  }
}

export const router = new Router();

/** Builds an application URL. */
export function href(path: string, params: Record<string, string | number | undefined | null> = {}): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') q.set(k, String(v));
  }
  const s = q.toString();
  return path + (s ? `?${s}` : '');
}

export const enc = encodeURIComponent;
