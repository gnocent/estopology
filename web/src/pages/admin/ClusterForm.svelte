<script lang="ts">
  import { untrack } from 'svelte';
  import { adminApi, type ClusterConfig, type ConnectionTest, type Ack, type EsConfig, type Schedule } from '../../lib/api';
  import { t } from '../../lib/i18n';
  import { dateTime } from '../../lib/format';

  let {
    cluster,
    onsaved,
    onremoved,
  }: { cluster?: ClusterConfig; onsaved: (id: string) => void; onremoved?: () => void } = $props();

  // The form is re-created for each cluster ({#key} in Clusters.svelte): it
  // deliberately starts from the initial value.
  const src = untrack(() => cluster);
  const isNew = !src;
  let id = $state(src?.id ?? '');
  let name = $state(src?.name ?? '');
  let urls = $state((src?.es.URLs ?? []).join('\n'));
  let authType = $state<EsConfig['Auth']>(src?.es.Auth ?? 'cert');
  let certFile = $state(src?.es.CertFile ?? '');
  let keyFile = $state(src?.es.KeyFile ?? '');
  let passFile = $state(src?.es.KeyPassphraseFile ?? '');
  let apiKeyFile = $state(src?.es.APIKeyFile ?? '');
  let username = $state(src?.es.Username ?? '');
  let passwordFile = $state(src?.es.PasswordFile ?? '');
  let caPath = $state(src?.es.CAPath ?? '');
  let systemCAs = $state(src?.es.UseSystemCAs ?? false);
  let timeoutS = $state(src?.es.Timeout ? src.es.Timeout / 1e9 : 60);
  let enabled = $state(src?.enabled ?? true);
  let machineAttr = $state(src?.machine_attr ?? '');
  let reference = $state((src?.node_reference ?? []).join('\n'));

  // Schedule (SPEC §5.3), in minutes / percent; empty = default.
  const MIN = 60e9;
  const sched = src?.schedule;
  let probeMin = $state(sched ? String(sched.ProbeEvery / MIN) : '');
  let minMin = $state(sched ? String(sched.MinInterval / MIN) : '');
  let maxMin = $state(sched ? String(sched.MaxInterval / MIN) : '');
  let jitterPct = $state(sched ? String(Math.round(sched.Jitter * 100)) : '');
  let threshold = $state(sched ? String(sched.ShardsThreshold) : '');
  function schedule(): Schedule | null {
    if (![probeMin, minMin, maxMin, jitterPct, threshold].some((v) => String(v).trim() !== '')) return null;
    const n = (v: string, def: number) => (String(v).trim() === '' ? def : Number(v));
    return {
      ProbeEvery: Math.round(n(probeMin, 5) * MIN),
      MinInterval: Math.round(n(minMin, 10) * MIN),
      MaxInterval: Math.round(n(maxMin, 60) * MIN),
      Jitter: n(jitterPct, 10) / 100,
      ShardsThreshold: n(threshold, 0),
    };
  }

  // Hierarchy (SPEC §7.4): "dc, rack, tier, machine, node"; tier, machine and
  // node are built-in levels, any other word is a node attribute.
  let hierarchyText = $state((src?.hierarchy ?? []).map((l) => (l.kind === 'attr' ? l.attr : l.kind)).join(', '));
  function hierarchy() {
    const words = hierarchyText.split(',').map((w) => w.trim()).filter(Boolean);
    if (!words.length) return null;
    return words.map((w) => (['tier', 'machine', 'node'].includes(w) ? { kind: w } : { kind: 'attr', attr: w }));
  }

  let message = $state('');
  let error = $state('');
  let test = $state<ConnectionTest | null>(null);
  let force = $state(false);

  function es(): EsConfig {
    return {
      URLs: urls.split('\n').map((u) => u.trim()).filter(Boolean),
      Auth: authType,
      CertFile: certFile || undefined,
      KeyFile: keyFile || undefined,
      KeyPassphraseFile: passFile || undefined,
      APIKeyFile: apiKeyFile || undefined,
      Username: username || undefined,
      PasswordFile: passwordFile || undefined,
      CAPath: caPath || undefined,
      UseSystemCAs: systemCAs,
      Timeout: Math.round(timeoutS * 1e9),
    };
  }

  async function run(f: () => Promise<unknown>, ok = t('common.saved')) {
    message = '';
    error = '';
    try {
      await f();
      message = ok;
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function save() {
    await run(async () => {
      const c = await adminApi.saveCluster({
        id,
        name,
        es: es(),
        enabled,
        machine_attr: machineAttr,
        node_reference: reference.split('\n').map((s) => s.trim()).filter(Boolean),
        schedule: schedule(),
        hierarchy: hierarchy(),
      });
      onsaved(c.id);
    });
  }

  async function doTest() {
    test = null;
    await run(async () => (test = await adminApi.test(id || 'new', es())), '');
  }

  // Family rules and acknowledgements (existing clusters only).
  let rules = $state('');
  let preview = $state<{ family: string; indices: number; examples: string[] }[]>([]);
  let acks = $state<Ack[]>([]);
  let ackNode = $state('');
  let ackComment = $state('');
  let ackFrom = $state(''); // datetime-local, empty = now
  $effect(() => {
    if (isNew) return;
    adminApi.familyRules(id).then((r) => (rules = r.join('\n')), () => {});
    adminApi.acks(id).then((a) => (acks = a), () => {});
  });
  const ruleList = $derived(rules.split('\n').map((s) => s.trim()).filter(Boolean));
</script>

<div class="card form">
  {#if src?.suspended}<div class="banner warn">{t('admin.cluster.suspended', { reason: src.suspend_reason ?? '' })}</div>{/if}
  {#if src?.bound_uuid}<p class="muted small">{t('admin.cluster.bound', { uuid: src.bound_uuid })}</p>{/if}
  <div class="grid">
    <label><span>{t('admin.cluster.id')}</span><input type="text" bind:value={id} disabled={!isNew} pattern="[a-z0-9][a-z0-9_-]*" required /></label>
    <label><span>{t('admin.cluster.name')}</span><input type="text" bind:value={name} /></label>
    <label class="wide"><span>{t('admin.cluster.urls')}</span><textarea rows="2" bind:value={urls}></textarea></label>
    <label>
      <span>{t('admin.cluster.auth')}</span>
      <select bind:value={authType}>
        <option value="cert">{t('admin.cluster.auth.cert')}</option>
        <option value="apikey">{t('admin.cluster.auth.apikey')}</option>
        <option value="basic">{t('admin.cluster.auth.basic')}</option>
        <option value="none">{t('admin.cluster.auth.none')}</option>
      </select>
    </label>
    <label><span>{t('admin.cluster.timeout')}</span><input type="text" inputmode="numeric" bind:value={timeoutS} /></label>
    {#if authType === 'cert'}
      <label><span>{t('admin.cluster.cert')}</span><input type="text" bind:value={certFile} /></label>
      <label><span>{t('admin.cluster.key')}</span><input type="text" bind:value={keyFile} /></label>
      <label><span>{t('admin.cluster.passphrase')}</span><input type="text" bind:value={passFile} /></label>
    {:else if authType === 'apikey'}
      <label><span>{t('admin.cluster.apikey')}</span><input type="text" bind:value={apiKeyFile} /></label>
    {:else if authType === 'basic'}
      <label><span>{t('admin.cluster.username')}</span><input type="text" bind:value={username} /></label>
      <label><span>{t('admin.cluster.password')}</span><input type="text" bind:value={passwordFile} /></label>
    {/if}
    <label><span>{t('admin.cluster.ca')}</span><input type="text" bind:value={caPath} /></label>
    <label class="check"><input type="checkbox" bind:checked={systemCAs} /> {t('admin.cluster.systemCAs')}</label>
    <label><span>{t('admin.cluster.machineAttr')}</span><input type="text" bind:value={machineAttr} /></label>
    <label class="check"><input type="checkbox" bind:checked={enabled} /> {t('admin.cluster.enabled')}</label>
    <label class="wide"><span>{t('admin.cluster.reference')}</span><textarea rows="2" bind:value={reference}></textarea></label>
    <label class="wide"><span>{t('admin.cluster.hierarchy')}</span><input type="text" placeholder={t('admin.cluster.hierarchyDefault')} bind:value={hierarchyText} /></label>
  </div>
  <h3>{t('admin.schedule.title')}</h3>
  <div class="grid sched">
    <label><span>{t('admin.schedule.probe')}</span><input type="text" inputmode="decimal" placeholder="5" bind:value={probeMin} /></label>
    <label><span>{t('admin.schedule.min')}</span><input type="text" inputmode="decimal" placeholder="10" bind:value={minMin} /></label>
    <label><span>{t('admin.schedule.max')}</span><input type="text" inputmode="decimal" placeholder="60" bind:value={maxMin} /></label>
    <label><span>{t('admin.schedule.jitter')}</span><input type="text" inputmode="numeric" placeholder="10" bind:value={jitterPct} /></label>
    <label><span>{t('admin.schedule.threshold')}</span><input type="text" inputmode="numeric" placeholder="0" bind:value={threshold} /></label>
  </div>
  <p class="muted small">{t('admin.schedule.help')}</p>
  <div class="row actions">
    <button onclick={save}>{t('common.save')}</button>
    <button onclick={doTest}>{t('admin.cluster.test')}</button>
    {#if !isNew}
      <button onclick={() => run(() => adminApi.capture(id, force), '✓')}>{t('admin.cluster.capture')}</button>
      <label class="check"><input type="checkbox" bind:checked={force} /> {t('admin.cluster.captureForce')}</label>
      {#if src?.suspended}
        <button onclick={() => run(() => adminApi.resume(id, test?.cluster_uuid ?? ''))}>{t('admin.cluster.resume')}</button>
        <button
          onclick={() => {
            const nid = prompt(t('admin.cluster.reattachPrompt'), `${id}-2`);
            if (nid) run(async () => onsaved((await adminApi.reattach(id, nid, `${name} (${nid})`)).id));
          }}>{t('admin.cluster.reattach')}</button
        >
      {/if}
      <span class="spacer"></span>
      <button class="danger" onclick={() => confirm(t('admin.cluster.removeConfirm', { id })) && run(async () => (await adminApi.removeCluster(id), onremoved?.()))}>{t('admin.cluster.remove')}</button>
      <button class="danger" onclick={() => confirm(t('admin.cluster.purgeConfirm', { id })) && run(() => adminApi.purge(id))}>{t('admin.cluster.purge')}</button>
    {/if}
  </div>
  {#if message}<p class="ok">{message}</p>{/if}
  {#if error}<p class="bad">{t('common.error', { msg: error })}</p>{/if}
  {#if test}
    <div class="banner" class:warn={!test.ok}>
      {#if test.error}{t('admin.test.fail', { error: test.error })}
      {:else}{t('admin.test.ok', { name: test.cluster_name ?? '', version: test.version ?? '', nodes: test.nodes ?? 0, health: test.health ?? '' })}{/if}
      {#if test.missing_privileges?.length}<div>{t('admin.test.missing', { list: test.missing_privileges.join(', ') })}</div>{/if}
      {#if test.uuid_changed}<div>{t('admin.test.uuidChanged', { bound: test.bound_uuid ?? '', seen: test.cluster_uuid ?? '' })}</div>{/if}
    </div>
  {/if}
</div>

{#if !isNew}
  <div class="card form">
    <h2>{t('admin.families.title')}</h2>
    <p class="muted small">{t('admin.families.help')}</p>
    <textarea class="mono" rows="3" bind:value={rules}></textarea>
    <div class="row actions">
      <button onclick={() => run(async () => (preview = (await adminApi.familyPreview(id, ruleList)).items), '')}>{t('admin.families.preview')}</button>
      <button onclick={() => run(() => adminApi.saveFamilyRules(id, ruleList))}>{t('common.save')}</button>
    </div>
    {#if preview.length}
      <table class="data">
        <tbody>
          {#each preview as p (p.family)}
            <tr><td class="mono">{p.family}</td><td class="num">{p.indices}</td><td class="muted mono small">{p.examples.join(', ')}</td></tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </div>

  <div class="card form">
    <h2>{t('admin.acks.title')}</h2>
    <div class="row">
      <input type="text" placeholder={t('admin.acks.node')} bind:value={ackNode} />
      <input type="text" placeholder={t('admin.acks.comment')} bind:value={ackComment} />
      <label class="check">{t('admin.acks.from')} <input type="datetime-local" bind:value={ackFrom} /></label>
      <button
        disabled={!ackNode}
        onclick={() =>
          run(async () => {
            await adminApi.addAck(id, { node: ackNode, comment: ackComment, from_ms: ackFrom ? new Date(ackFrom).getTime() : undefined });
            acks = await adminApi.acks(id);
            ackNode = ackComment = ackFrom = '';
          })}>{t('admin.acks.add')}</button
      >
    </div>
    <table class="data">
      <tbody>
        {#each acks as a (a.id)}
          <tr class:revoked={a.revoked_ms}>
            <td>{a.node}</td>
            <td>{t('admin.acks.from')} {dateTime(a.from_ms)}</td>
            <td class="muted">{a.author}{a.comment ? ` — ${a.comment}` : ''}</td>
            <td>
              {#if a.revoked_ms}<span class="muted">{t('admin.acks.revoked', { by: a.revoked_by ?? '' })}</span>
              {:else}<button onclick={() => run(async () => (await adminApi.revokeAck(id, a.id), (acks = await adminApi.acks(id))))}>{t('admin.acks.revoke')}</button>{/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<style>
  .form {
    margin-bottom: 1rem;
  }
  .form h2 {
    margin-top: 0;
  }
  h3 {
    margin: 1rem 0 0.4rem;
    font-size: 0.95rem;
  }
  .grid.sched {
    grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 0.6rem 1rem;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.88rem;
  }
  label.check {
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
  .actions {
    margin-top: 0.8rem;
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
  tr.revoked {
    opacity: 0.55;
  }
</style>
