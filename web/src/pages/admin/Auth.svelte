<script lang="ts">
  import { adminApi, type UserInfo, type LDAPConfig, type LDAPTest } from '../../lib/api';
  import { session } from '../../lib/session.svelte';
  import { t, tk } from '../../lib/i18n';
  import { dateTime } from '../../lib/format';

  let mode = $state('none');
  let hours = $state(12);
  let users = $state<UserInfo[]>([]);
  let message = $state('');
  let error = $state('');

  // LDAP (one value per line for the lists).
  let lUrls = $state('');
  let lStartTLS = $state(false);
  let lCA = $state('');
  let lServerName = $state('');
  let lBindDN = $state('');
  let lBindPw = $state('');
  let lUserBase = $state('');
  let lUserFilter = $state('');
  let lGroupBase = $state('');
  let lGroupFilter = $state('');
  let lAdmins = $state('');
  let lReaders = $state('');
  let tUser = $state('');
  let tPassword = $state('');
  let test = $state<LDAPTest | null>(null);

  const lines = (s: string) => s.split('\n').map((l) => l.trim()).filter(Boolean);
  function ldap(): LDAPConfig {
    return {
      urls: lines(lUrls),
      start_tls: lStartTLS,
      ca_file: lCA || undefined,
      server_name: lServerName || undefined,
      bind_dn: lBindDN,
      bind_password_file: lBindPw,
      user_base: lUserBase,
      user_filter: lUserFilter || undefined,
      group_base: lGroupBase,
      group_filter: lGroupFilter || undefined,
      admin_groups: lines(lAdmins),
      reader_groups: lines(lReaders),
    };
  }

  let uName = $state('');
  let uRole = $state<'reader' | 'admin'>('reader');
  let uPassword = $state('');
  let uDisabled = $state(false);

  async function load() {
    try {
      const a = await adminApi.auth();
      mode = a.mode;
      hours = a.session_hours;
      const l = a.ldap;
      if (l) {
        lUrls = l.urls.join('\n');
        lStartTLS = !!l.start_tls;
        lCA = l.ca_file ?? '';
        lServerName = l.server_name ?? '';
        lBindDN = l.bind_dn;
        lBindPw = l.bind_password_file;
        lUserBase = l.user_base;
        lUserFilter = l.user_filter ?? '';
        lGroupBase = l.group_base;
        lGroupFilter = l.group_filter ?? '';
        lAdmins = (l.admin_groups ?? []).join('\n');
        lReaders = (l.reader_groups ?? []).join('\n');
      }
      users = await adminApi.users();
    } catch (e) {
      error = (e as Error).message;
    }
  }
  $effect(() => {
    load();
  });

  async function run(f: () => Promise<unknown>) {
    message = error = '';
    try {
      await f();
      message = t('common.saved');
      await load();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  function edit(u: UserInfo) {
    uName = u.name;
    uRole = u.role;
    uDisabled = u.disabled;
    uPassword = '';
  }
</script>

<div class="card">
  <div class="row">
    <label class="row"
      ><span>{t('admin.auth.mode')}</span>
      <select bind:value={mode}>
        {#each ['none', 'local', 'ldap'] as m (m)}<option value={m}>{tk(`admin.auth.mode.${m}`)}</option>{/each}
      </select>
    </label>
    <label class="row"><span>{t('admin.auth.sessionHours')}</span><input type="text" inputmode="numeric" size="4" bind:value={hours} /></label>
    <button onclick={() => run(async () => (await adminApi.saveAuth({ mode, session_hours: Number(hours), ldap: lines(lUrls).length ? ldap() : undefined }), await session.load()))}>{t('common.save')}</button>
  </div>
  {#if message}<p class="ok">{message}</p>{/if}
  {#if error}<p class="bad">{t('common.error', { msg: error })}</p>{/if}
</div>

{#if mode === 'ldap'}
  <h2>{t('admin.ldap.title')}</h2>
  <div class="card">
    <p class="muted small">{t('admin.ldap.help')}</p>
    <div class="grid">
      <label class="wide"><span>{t('admin.ldap.urls')}</span><textarea rows="2" bind:value={lUrls}></textarea></label>
      <label class="check"><input type="checkbox" bind:checked={lStartTLS} /> {t('admin.ldap.startTLS')}</label>
      <label><span>{t('admin.ldap.ca')}</span><input type="text" bind:value={lCA} /></label>
      <label><span>{t('admin.ldap.serverName')}</span><input type="text" bind:value={lServerName} /></label>
      <label><span>{t('admin.ldap.bindDN')}</span><input type="text" bind:value={lBindDN} /></label>
      <label><span>{t('admin.ldap.bindPassword')}</span><input type="text" bind:value={lBindPw} /></label>
      <label><span>{t('admin.ldap.userBase')}</span><input type="text" bind:value={lUserBase} /></label>
      <label><span>{t('admin.ldap.userFilter')}</span><input type="text" class="mono" placeholder="(&(objectClass=inetOrgPerson)(uid={'{user}'}))" bind:value={lUserFilter} /></label>
      <label><span>{t('admin.ldap.groupBase')}</span><input type="text" bind:value={lGroupBase} /></label>
      <label><span>{t('admin.ldap.groupFilter')}</span><input type="text" class="mono" placeholder={t('admin.ldap.groupFilterDefault')} bind:value={lGroupFilter} /></label>
      <label class="wide"><span>{t('admin.ldap.adminGroups')}</span><textarea rows="2" bind:value={lAdmins}></textarea></label>
      <label class="wide"><span>{t('admin.ldap.readerGroups')}</span><textarea rows="2" bind:value={lReaders}></textarea></label>
    </div>
    <div class="row form">
      <input type="text" placeholder={t('login.user')} autocomplete="off" bind:value={tUser} />
      <input type="password" placeholder={t('login.password')} autocomplete="new-password" bind:value={tPassword} />
      <button
        onclick={async () => {
          test = null;
          error = '';
          try {
            test = await adminApi.ldapTest(ldap(), tUser, tPassword);
          } catch (e) {
            error = (e as Error).message;
          }
          tPassword = '';
        }}>{t('admin.ldap.test')}</button
      >
    </div>
    {#if test}
      <table class="data steps">
        <tbody>
          {#each test.steps as s (s.step)}
            <tr><td class:ok={s.ok} class:bad={!s.ok}>{s.ok ? '✓' : '✗'}</td><td>{tk(`admin.ldap.step.${s.step}`)}</td><td class="mono small">{s.detail ?? ''}</td></tr>
          {/each}
        </tbody>
      </table>
      {#if test.result?.role}<p class="ok">{t('admin.ldap.testOk', { role: tk(`role.${test.result.role}`) })}</p>
      {:else if test.ok}<p class="ok">{t('admin.ldap.connectionOk')}</p>
      {:else}<p class="bad">{test.error}</p>{/if}
    {/if}
  </div>
{/if}

<h2>{t('admin.users.title')}</h2>
<div class="card">
  <table class="data">
    <thead><tr><th>{t('admin.users.name')}</th><th>{t('admin.users.role')}</th><th>{t('admin.users.source')}</th><th>{t('col.created')}</th><th>{t('admin.users.disabled')}</th><th></th></tr></thead>
    <tbody>
      {#each users as u (u.name)}
        <tr>
          <td>{u.name}</td><td>{tk(`role.${u.role}`)}</td><td>{u.source}</td><td>{dateTime(u.created_ms)}</td>
          <td>{u.disabled ? t('common.yes') : ''}</td>
          <td class="row">
            <button onclick={() => edit(u)}>✎</button>
            {#if u.name !== session.info?.user}
              <button onclick={() => confirm(t('common.confirm')) && run(() => adminApi.deleteUser(u.name))}>{t('common.delete')}</button>
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
  <div class="row form">
    <input type="text" placeholder={t('admin.users.name')} bind:value={uName} />
    <select bind:value={uRole}>
      <option value="reader">{t('role.reader')}</option>
      <option value="admin">{t('role.admin')}</option>
    </select>
    <input type="password" placeholder={t('admin.users.password')} autocomplete="new-password" bind:value={uPassword} />
    <label class="row"><input type="checkbox" bind:checked={uDisabled} /> {t('admin.users.disabled')}</label>
    <button disabled={!uName} onclick={() => run(() => adminApi.saveUser(uName, { role: uRole, password: uPassword || undefined, disabled: uDisabled }))}>{t('common.save')}</button>
  </div>
</div>

<style>
  .ok {
    color: var(--green);
  }
  .bad {
    color: var(--red);
  }
  .form {
    margin-top: 0.8rem;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 0.6rem 1rem;
  }
  .grid label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.88rem;
  }
  .grid label.check {
    flex-direction: row;
    align-items: center;
    gap: 0.4rem;
  }
  .wide {
    grid-column: 1 / -1;
  }
  textarea {
    width: 100%;
    font: inherit;
    background: var(--surface);
    color: inherit;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.3rem 0.5rem;
  }
  .small {
    font-size: 0.82rem;
  }
  .steps {
    margin-top: 0.8rem;
  }
</style>
