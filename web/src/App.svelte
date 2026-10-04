<script lang="ts">
  import { router, groupLabels } from './lib/router.svelte';
  import { prefs } from './lib/prefs.svelte';
  import { session } from './lib/session.svelte';
  import { setShowSystem } from './lib/api';
  import { t } from './lib/i18n';
  import Header from './components/Header.svelte';
  import Home from './pages/Home.svelte';
  import Cluster from './pages/Cluster.svelte';
  import NodePage from './pages/NodePage.svelte';
  import GroupPage from './pages/GroupPage.svelte';
  import FamilyPage from './pages/FamilyPage.svelte';
  import IndexPage from './pages/IndexPage.svelte';
  import Diff from './pages/Diff.svelte';
  import Losses from './pages/Losses.svelte';
  import Events from './pages/Events.svelte';
  import Flows from './pages/Flows.svelte';
  import Constraints from './pages/Constraints.svelte';
  import Admin from './pages/admin/Admin.svelte';
  import Login from './pages/Login.svelte';

  session.load();
  setShowSystem(() => router.query.get('system') === '1');

  $effect(() => {
    document.documentElement.dataset.theme = prefs.dark ? 'dark' : 'light';
    document.documentElement.lang = prefs.lang;
  });

  const r = $derived(router.route);
</script>

{#if !session.loaded}
  <p class="muted boot">{t('common.loading')}</p>
{:else if session.needsLogin}
  {#if session.info?.insecure}<div class="nobanner">{t('auth.insecureBanner')}</div>{/if}
  <Login />
{:else}
  <Header />
  {#if session.info?.mode === 'none'}<div class="nobanner">{t('auth.noneBanner')}</div>{/if}
  {#if session.info?.insecure}<div class="nobanner">{t('auth.insecureBanner')}</div>{/if}
  {#if session.info?.instance === 'standby'}<div class="habanner">{t('ha.banner.standby')}</div>
  {:else if session.info?.instance === 'fenced'}<div class="habanner fenced">{t('ha.banner.fenced')}</div>{/if}
  <main>
    {#if r.name === 'home'}
      <Home />
    {:else if r.name === 'cluster'}
      <Cluster cluster={r.params.cluster} />
    {:else if r.name === 'group'}
      <GroupPage cluster={r.params.cluster} labels={groupLabels(router.path)} />
    {:else if r.name === 'node'}
      <NodePage cluster={r.params.cluster} name={r.params.name} />
    {:else if r.name === 'family'}
      <FamilyPage cluster={r.params.cluster} family={r.params.family} />
    {:else if r.name === 'index'}
      <IndexPage cluster={r.params.cluster} uuid={r.params.uuid} />
    {:else if r.name === 'diff'}
      <Diff cluster={r.params.cluster} />
    {:else if r.name === 'losses'}
      <Losses cluster={r.params.cluster} />
    {:else if r.name === 'events'}
      <Events cluster={r.params.cluster} />
    {:else if r.name === 'flows'}
      <Flows cluster={r.params.cluster} />
    {:else if r.name === 'constraints'}
      <Constraints cluster={r.params.cluster} />
    {:else if r.name === 'admin' && session.isAdmin}
      <Admin />
    {:else}
      <p class="muted">{t('notfound')}</p>
    {/if}
  </main>
{/if}

<style>
  main {
    padding: 1rem 1.25rem 3rem;
    max-width: 1800px;
    margin: 0 auto;
  }
  .nobanner {
    background: linear-gradient(90deg, var(--orange), var(--yellow));
    color: #fff;
    text-align: center;
    font-size: 0.85rem;
    padding: 0.2rem;
  }
  .habanner {
    background: linear-gradient(90deg, var(--accent-soft), var(--tint-2));
    text-align: center;
    font-size: 0.85rem;
    padding: 0.2rem;
  }
  .habanner.fenced {
    background: var(--red);
    color: #fff;
  }
  .boot {
    padding: 2rem;
  }
</style>
