// Session of the UI (SPEC §10): mode, user, role, CSRF token; the language
// and theme follow the account (SPEC §8.1).
import { sessionApi, setCSRF, setUnauthorizedHandler, type SessionInfo } from './api';
import { prefs } from './prefs.svelte';

class Session {
  info = $state<SessionInfo | null>(null);
  loaded = $derived(this.info !== null);
  needsLogin = $derived(this.info !== null && !this.info.authenticated);
  isAdmin = $derived(this.info?.role === 'admin');

  constructor() {
    setUnauthorizedHandler(() => {
      if (this.info) this.info = { ...this.info, authenticated: false };
    });
    prefs.onChange = (lang, theme) => {
      // Accounts only; failures (e.g. a read-only standby) keep the browser copy.
      if (this.info?.authenticated && this.info.mode !== 'none') sessionApi.savePrefs(lang, theme).catch(() => {});
    };
  }

  async load() {
    try {
      this.set(await sessionApi.get());
    } catch {
      this.info = { mode: 'local', authenticated: false };
    }
  }

  set(i: SessionInfo) {
    this.info = i;
    setCSRF(i.csrf ?? '');
    if (i.prefs) prefs.apply(i.prefs.lang, i.prefs.theme);
  }

  async login(name: string, password: string) {
    this.set(await sessionApi.login(name, password));
    await this.load(); // brings the account preferences
  }

  async logout() {
    try {
      await sessionApi.logout();
    } finally {
      await this.load();
    }
  }
}

export const session = new Session();
