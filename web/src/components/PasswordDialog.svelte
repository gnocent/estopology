<script lang="ts">
  // Change of one's own password (local accounts). The other sessions of the
  // account are closed by the server; this one stays open.
  import { sessionApi, ApiError } from '../lib/api';
  import { t } from '../lib/i18n';

  let { onclose }: { onclose: () => void } = $props();

  let old = $state('');
  let next = $state('');
  let busy = $state(false);
  let error = $state('');
  let done = $state(false);

  async function submit(e: Event) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      await sessionApi.changePassword(old, next);
      done = true;
      old = next = '';
    } catch (err) {
      error = err instanceof ApiError && err.status === 429 ? t('login.throttled') : (err as Error).message;
    } finally {
      busy = false;
    }
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose()} />

<div class="veil" role="presentation" onclick={(e) => e.target === e.currentTarget && onclose()}>
  <form class="card dialog" onsubmit={submit} aria-label={t('user.changePassword')}>
    <h2>{t('user.changePassword')}</h2>
    {#if done}
      <p>{t('user.passwordChanged')}</p>
      <div class="row actions"><button type="button" onclick={onclose}>{t('common.close')}</button></div>
    {:else}
      <label>
        <span class="muted">{t('user.oldPassword')}</span>
        <input type="password" autocomplete="current-password" bind:value={old} required />
      </label>
      <label>
        <span class="muted">{t('user.newPassword')}</span>
        <input type="password" autocomplete="new-password" minlength="12" bind:value={next} required />
      </label>
      {#if error}<p class="bad">{error}</p>{/if}
      <div class="row actions">
        <button type="button" onclick={onclose}>{t('common.close')}</button>
        <button type="submit" aria-pressed="true" disabled={busy || next.length < 12 || !old}>{t('common.save')}</button>
      </div>
    {/if}
  </form>
</div>

<style>
  .veil {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: grid;
    place-items: center;
    background: rgb(0 0 0 / 45%);
  }
  .dialog {
    width: min(26rem, calc(100vw - 2rem));
    background: var(--surface);
    box-shadow: var(--shadow-3);
    display: flex;
    flex-direction: column;
    gap: 0.7rem;
  }
  h2 {
    margin: 0;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
  .actions {
    justify-content: flex-end;
  }
  .bad {
    color: var(--red);
    margin: 0;
  }
</style>
