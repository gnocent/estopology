<script lang="ts">
  import Icon from './Icon.svelte';
  import { tip } from '../lib/tip';
  import { router, href, enc } from '../lib/router.svelte';
  import { prefs } from '../lib/prefs.svelte';
  import { t, tk } from '../lib/i18n';
  import Info from './Info.svelte';
  import Search from './Search.svelte';
  import PasswordDialog from './PasswordDialog.svelte';
  import { session } from '../lib/session.svelte';

  const cluster = $derived(router.route.params.cluster);
  const at = $derived(router.query.get('at'));
  const system = $derived(router.query.get('system')); // kept across the tabs

  const tabs = [
    ['cluster', '', 'nav.cluster'],
    ['diff', '/diff', 'nav.diff'],
    ['losses', '/losses', 'nav.losses'],
    ['events', '/events', 'nav.events'],
    ['flows', '/flows', 'nav.flows'],
    ['constraints', '/constraints', 'nav.constraints'],
  ] as const;

  let passwordOpen = $state(false);
  const themeLabel = $derived(t(prefs.theme === 'auto' ? 'theme.auto' : prefs.theme === 'light' ? 'theme.light' : 'theme.dark'));
</script>

<header>
  <a class="brand" href="/">
    <img src="/favicon.svg" alt="" width="20" height="20" />
    <span>{t('app.title')}</span>
  </a>
  {#if cluster}
    <nav aria-label="cluster">
      <a class="cluster-name" href={href(`/c/${enc(cluster)}`, { at, system })}>{cluster}</a>
      {#each tabs as [name, suffix, label] (name)}
        <a
          href={href(`/c/${enc(cluster)}${suffix}`, name === 'cluster' || name === 'constraints' ? { at, system } : { system })}
          class:active={router.route.name === name}
          use:tip={tk(`nav.help.${name}`)}><Icon {name} /> {t(label)}</a
        >
      {/each}
    </nav>
  {:else}
    <nav><a href="/" class:active={router.route.name === 'home'}><Icon name="home" /> {t('nav.home')}</a></nav>
  {/if}
  <div class="spacer"></div>
  {#if cluster}
    <label class="sys" use:tip={t('header.systemHelp')}>
      <input
        type="checkbox"
        checked={router.query.get('system') !== '1'}
        onchange={(e) => router.set({ system: (e.target as HTMLInputElement).checked ? null : '1' }, false)}
      />
      {t('common.hideSystem')}
    </label>
  {/if}
  <Search {cluster} />
  <Info text={t('search.help')} />
  <button use:tip={themeLabel} aria-label={themeLabel} onclick={() => prefs.cycleTheme()}>
    {prefs.theme === 'auto' ? '◐' : prefs.theme === 'light' ? '☀' : '☾'}
  </button>
  <button onclick={() => prefs.setLang(prefs.lang === 'fr' ? 'en' : 'fr')}>{t('lang.switch')}</button>
  {#if session.isAdmin}<a href="/admin" class="admin" class:active={router.route.name === 'admin'}><Icon name="admin" /> {t('nav.admin')}</a>{/if}
  {#if session.info?.mode !== 'none' && session.info?.user}
    <button class="user" use:tip={`${tk(`role.${session.info.role}`)}\n${t('user.changePassword')}`} onclick={() => (passwordOpen = true)}>{session.info.user}</button>
    <button onclick={() => session.logout()}>{t('user.logout')}</button>
  {/if}
</header>
{#if passwordOpen}<PasswordDialog onclose={() => (passwordOpen = false)} />{/if}

<style>
  header {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.5rem 1.25rem;
    background: linear-gradient(90deg, var(--glass), var(--tint) 55%, var(--tint-2)), var(--glass);
    backdrop-filter: blur(16px) saturate(1.6);
    border-bottom: 1px solid var(--border);
    box-shadow: var(--shadow);
    position: sticky;
    top: 0;
    z-index: 20;
    view-transition-name: site-header;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    font-weight: 600;
    color: var(--text);
  }
  .brand:hover {
    text-decoration: none;
  }
  .brand span {
    background: var(--grad);
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
    padding-right: 0.1em;
  }
  nav {
    display: flex;
    gap: 0.2rem;
    align-items: center;
    flex-wrap: nowrap;
  }
  nav a {
    padding: 0.3rem 0.5rem;
    white-space: nowrap;
    border-radius: var(--radius);
    color: var(--muted);
    position: relative;
    transition:
      color var(--fast) var(--ease),
      background-color var(--fast) var(--ease);
  }
  nav a:hover {
    text-decoration: none;
    color: var(--text);
    background: var(--layer);
  }
  nav a.active {
    background: linear-gradient(135deg, var(--accent-soft), color-mix(in srgb, var(--accent-2) 18%, var(--accent-soft)));
    color: var(--accent);
    font-weight: 500;
  }
  nav a.active::after {
    content: '';
    position: absolute;
    left: 0.5rem;
    right: 0.5rem;
    bottom: calc(-0.5rem - 1px);
    height: 2px;
    border-radius: 2px;
    background: var(--grad);
  }
  nav a.cluster-name {
    font-weight: 600;
    color: var(--text);
    margin-right: 0.4rem;
    padding: 0.2rem 0.65rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--layer);
  }
  .spacer {
    flex: 1;
  }
  .admin {
    color: var(--muted);
    white-space: nowrap;
    padding: 0.3rem 0.5rem;
    border-radius: var(--radius);
    transition: background-color var(--fast) var(--ease);
  }
  .admin:hover {
    text-decoration: none;
    color: var(--text);
    background: var(--surface-2);
  }
  .admin.active {
    color: var(--accent);
    background: var(--accent-soft);
    font-weight: 500;
  }
  .user {
    font-size: 0.85rem;
  }
  .sys {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.82rem;
    color: var(--muted);
    white-space: nowrap;
  }
</style>
