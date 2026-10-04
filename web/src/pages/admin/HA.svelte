<script lang="ts">
  import { adminApi, type HAStatus } from '../../lib/api';
  import { session } from '../../lib/session.svelte';
  import { t, tk } from '../../lib/i18n';
  import { dateTime, num } from '../../lib/format';

  let st = $state<HAStatus | null>(null);
  let error = $state('');
  let message = $state('');
  let peer = $state('');
  let demoteURL = $state('');

  async function load() {
    try {
      st = await adminApi.ha();
      peer = st.peer ?? '';
    } catch (e) {
      error = (e as Error).message;
    }
  }
  $effect(() => {
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  });

  async function act(action: 'promote' | 'demote' | 'rebuild' | 'resume' | 'peer', url?: string, confirmKey?: string) {
    if (confirmKey && !confirm(tk(confirmKey))) return;
    message = error = '';
    try {
      st = await adminApi.haAction(action, url);
      message = t('common.saved');
      await session.load(); // the banner follows the role
    } catch (e) {
      error = (e as Error).message;
    }
  }

  function duration(ms: number): string {
    const s = Math.round(ms / 1000);
    if (s < 120) return `${s} s`;
    const m = Math.round(s / 60);
    return m < 120 ? `${m} min` : `${Math.round(m / 60)} h`;
  }
</script>

{#if st}
  <div class="card">
    <dl>
      <dt>{t('admin.ha.role')}</dt>
      <dd><strong>{tk(`ha.role.${st.role}`)}</strong>{#if st.promoted_ms} <span class="muted">({t('admin.ha.promotedAt', { date: dateTime(st.promoted_ms) })})</span>{/if}</dd>
      {#if st.standby_of}<dt>{t('admin.ha.standbyOf')}</dt><dd class="mono">{st.standby_of}</dd>{/if}
      <dt>{t('admin.ha.token')}</dt><dd>{st.token_configured ? t('common.yes') : t('admin.ha.noToken')}</dd>
      {#if st.fenced_reason}<dt>{t('admin.ha.fenced')}</dt><dd class="bad">{st.fenced_reason}</dd>{/if}
    </dl>
  </div>

  {#if st.role === 'standby' && st.replication}
    {@const r = st.replication}
    <h2>{t('admin.ha.replication')}</h2>
    <div class="card">
      {#if r.blocked}<div class="banner warn"><strong>{t('admin.ha.blocked')}</strong> {r.blocked}<br />{t('admin.ha.blockedHelp')}</div>{/if}
      <dl>
        <dt>{t('admin.ha.lastSync')}</dt><dd>{r.last_sync_ms ? dateTime(r.last_sync_ms, true) : '—'}</dd>
        <dt>{t('admin.ha.lag')}</dt><dd>{duration(r.lag_ms)}</dd>
        {#if r.rebuilding}<dt></dt><dd>{t('admin.ha.rebuilding')}</dd>{/if}
        {#if r.last_error}<dt>{t('admin.ha.lastError')}</dt><dd class="bad">{r.last_error} <span class="muted">({dateTime(r.last_error_ms ?? 0, true)})</span></dd>{/if}
      </dl>
      <table class="data">
        <thead><tr><th>{t('admin.col.cluster')}</th><th>{t('admin.ha.cursor')}</th><th>{t('admin.ha.primaryNewest')}</th><th class="num">{t('admin.ha.records')}</th><th class="num">{t('admin.ha.rebuilds')}</th></tr></thead>
        <tbody>
          {#each r.clusters as c (c.cluster)}
            <tr><td>{c.cluster}</td><td>{c.cursor_ms ? dateTime(c.cursor_ms, true) : '—'}</td><td>{c.primary_ms ? dateTime(c.primary_ms, true) : '—'}</td><td class="num">{num(c.records)}</td><td class="num">{num(c.rebuilds)}</td></tr>
          {/each}
        </tbody>
      </table>
      <div class="row actions">
        <button class="danger" onclick={() => act('promote', undefined, 'admin.ha.promoteConfirm')}>{t('admin.ha.promote')}</button>
        <button onclick={() => act('rebuild', undefined, 'admin.ha.rebuildConfirm')}>{t('admin.ha.rebuild')}</button>
      </div>
      <p class="muted small">{t('admin.ha.promoteHelp')}</p>
    </div>
  {:else}
    <h2>{t('admin.ha.peer')}</h2>
    <div class="card">
      <p class="muted small">{t('admin.ha.peerHelp')}</p>
      <div class="row">
        <input type="text" class="mono wide" placeholder="https://estopo-standby.example.org:8443" bind:value={peer} />
        <button onclick={() => act('peer', peer)}>{t('common.save')}</button>
      </div>
      {#if st.peer_check}
        <p class="small">
          {#if st.peer_check.last_error}<span class="bad">{t('admin.ha.peerError', { error: st.peer_check.last_error })}</span>
          {:else if st.peer_check.peer_role}{t('admin.ha.peerSeen', { role: tk(`ha.role.${st.peer_check.peer_role}`), date: dateTime(st.peer_check.last_ms ?? 0, true) })}{/if}
        </p>
      {/if}
      <div class="row actions">
        {#if st.role === 'fenced'}<button onclick={() => act('resume', undefined, 'admin.ha.resumeConfirm')}>{t('admin.ha.resume')}</button>{/if}
        <input type="text" class="mono wide" placeholder={st.peer || t('admin.ha.demoteURL')} bind:value={demoteURL} />
        <button class="danger" onclick={() => act('demote', demoteURL || undefined, 'admin.ha.demoteConfirm')}>{t('admin.ha.demote')}</button>
      </div>
      <p class="muted small">{t('admin.ha.demoteHelp')}</p>
    </div>
  {/if}
{/if}
{#if message}<p class="ok">{message}</p>{/if}
{#if error}<p class="bad">{t('common.error', { msg: error })}</p>{/if}

<style>
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.3rem 1rem;
    margin: 0 0 0.8rem;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
  }
  .actions {
    margin-top: 0.8rem;
  }
  .wide {
    min-width: 28rem;
  }
  .danger {
    border-color: var(--red);
  }
  .ok {
    color: var(--green);
  }
  .bad {
    color: var(--red);
  }
  .small {
    font-size: 0.82rem;
  }
</style>
