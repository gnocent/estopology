<script lang="ts">
  import { adminApi } from '../../lib/api';
  import { t } from '../../lib/i18n';

  let text = $state('');
  let message = $state('');
  let error = $state('');

  $effect(() => {
    adminApi.whitelist().then((w) => (text = w.join('\n')), (e) => (error = (e as Error).message));
  });

  async function save() {
    message = error = '';
    try {
      const w = await adminApi.saveWhitelist(text.split('\n').map((s) => s.trim()).filter(Boolean));
      text = w.join('\n');
      message = t('common.saved');
    } catch (e) {
      error = (e as Error).message;
    }
  }
</script>

<div class="card">
  <p class="muted">{t('admin.whitelist.help')}</p>
  <textarea class="mono" rows="18" bind:value={text}></textarea>
  <div class="row">
    <button onclick={save}>{t('common.save')}</button>
    {#if message}<span class="ok">{message}</span>{/if}
    {#if error}<span class="bad">{error}</span>{/if}
  </div>
</div>

<style>
  textarea {
    width: 100%;
    background: var(--surface);
    color: inherit;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.4rem;
    margin-bottom: 0.6rem;
  }
  .ok {
    color: var(--green);
  }
  .bad {
    color: var(--red);
  }
</style>
