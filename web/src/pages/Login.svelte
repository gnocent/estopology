<script lang="ts">
  import { ApiError } from '../lib/api';
  import { session } from '../lib/session.svelte';
  import { t } from '../lib/i18n';

  let name = $state('');
  let password = $state('');
  let error = $state('');
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      await session.login(name, password);
      password = '';
    } catch (err) {
      error =
        err instanceof ApiError && err.status === 429
          ? t('login.throttled')
          : err instanceof ApiError && err.status === 503
            ? t('login.unavailable')
            : t('login.invalid');
    } finally {
      busy = false;
    }
  }
</script>

<form class="card login" onsubmit={submit}>
  <h1>{t('login.title')}</h1>
  <label>
    <span>{t('login.user')}</span>
    <input type="text" autocomplete="username" bind:value={name} required />
  </label>
  <label>
    <span>{t('login.password')}</span>
    <input type="password" autocomplete="current-password" bind:value={password} required />
  </label>
  {#if error}<p class="bad" role="alert">{error}</p>{/if}
  <button type="submit" disabled={busy}>{t('login.submit')}</button>
</form>

<style>
  .login {
    max-width: 360px;
    margin: 12vh auto 0;
    display: flex;
    flex-direction: column;
    gap: 0.8rem;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .bad {
    color: var(--red);
    margin: 0;
  }
</style>
