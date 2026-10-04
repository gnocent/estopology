import { test, expect, type Page } from '@playwright/test';

const base = 'http://127.0.0.1:18093';

// Every request of every page must stay on the estopo origin: the UI works
// without any external network access (SPEC §16.6, air-gapped production).
const foreign: string[] = [];
test.beforeEach(async ({ page }) => {
  page.on('request', (r) => {
    const u = new URL(r.url());
    if (u.origin !== base && u.protocol !== 'data:' && u.protocol !== 'blob:') foreign.push(r.url());
  });
});
test.afterAll(() => {
  expect(foreign, 'requests outside the estopo origin').toEqual([]);
});

async function waitClusters(page: Page) {
  // Both clusters have data once their first full capture is ingested.
  await expect(async () => {
    const r = await page.request.get('/api/v1/clusters');
    const cs = await r.json();
    expect(cs.filter((c: { counts?: unknown }) => c.counts).length).toBe(2);
  }).toPass({ timeout: 60_000 });
}

test('home lists the clusters by severity', async ({ page }) => {
  await waitClusters(page);
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Clusters' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Small' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Cold' })).toBeVisible();
  await expect(page.locator('svg.band').first()).toBeVisible();
});

test('cluster view: tiles, drill down and zoom out, logical view', async ({ page }) => {
  await waitClusters(page);
  const state = await (await page.request.get('/api/v1/clusters/small/state')).json();
  await page.goto('/c/small');
  const tiles = page.locator('button.tile');
  await expect(tiles).toHaveCount(state.nodes.length);

  // A tile opens the page of its node; "zoom out" goes back up one level.
  const name = await tiles.first().getAttribute('aria-label');
  await tiles.first().click();
  await expect(page).toHaveURL(/\/c\/small\/node\//);
  await expect(page.getByRole('heading', { level: 1 })).toContainText(name!);
  await page.locator('.crumbs .up').click();
  await expect(page).toHaveURL(/\/c\/small\/g\//);
  await expect(page.locator('button.tile.large').first()).toBeVisible();
  const url = page.url();
  await page.reload(); // the page of a group is a permalink
  expect(page.url()).toBe(url);
  await expect(page.locator('.summary .stat').first()).toBeVisible();
  while (/\/g\//.test(page.url())) {
    const before = page.url();
    await page.locator('.crumbs .up').click();
    await expect(page).not.toHaveURL(before);
  }
  await expect(page).toHaveURL(/\/c\/small(\?|$)/);

  // The name of a frame opens the page of that group alone.
  const top = state.hierarchy.children[0];
  await page.locator('a.label', { hasText: top.label }).first().click();
  await expect(page).toHaveURL(/\/c\/small\/g\/[^/?]+(\?|$)/);
  await expect(page.locator('button.tile')).toHaveCount(top.nodes.length);
  await page.goBack();

  await page.getByRole('button', { name: 'Vue logique' }).click();
  await expect(page).toHaveURL(/view=logical/);
  await expect(page.locator('canvas')).toBeVisible();
});

test('a date in the URL is a permalink', async ({ page }) => {
  await waitClusters(page);
  const at = Date.now() - 60_000;
  await page.goto(`/c/small?at=${at}`);
  await expect(page.locator('.timeband strong')).not.toHaveText('Maintenant');
  await page.getByRole('button', { name: 'Maintenant' }).last().click();
  await expect(page).not.toHaveURL(/at=/);
});

test('search goes to the node page', async ({ page }) => {
  await waitClusters(page);
  await page.goto('/');
  const box = page.getByRole('searchbox');
  await box.fill('small-hot-dc1-m01-1');
  await expect(page.getByRole('option').first()).toBeVisible();
  await box.press('Enter');
  await expect(page).toHaveURL(/\/c\/small\/node\/small-hot-dc1-m01-1/);
  await expect(page.getByRole('heading', { name: /small-hot-dc1-m01-1/ })).toBeVisible();
});

test('diff screen and XLSX export', async ({ page }) => {
  await waitClusters(page);
  await page.goto('/c/small/diff');
  await expect(page.getByRole('heading', { name: 'Différences entre A et B' })).toBeVisible();
  await expect(page.getByText('Copies déplacées')).toBeVisible();
  const href = await page.getByRole('link', { name: 'XLSX' }).getAttribute('href');
  expect(href).toContain('format=xlsx');
  expect(href).toContain('lang=fr');
  const r = await page.request.get(href!);
  expect(r.status()).toBe(200);
  expect(r.headers()['content-type']).toContain('spreadsheetml');
});

test('analysis screens render', async ({ page }) => {
  await waitClusters(page);
  for (const [path, heading] of [
    ['/c/small/losses', 'Pertes et disparitions'],
    ['/c/small/events', 'Journal d’événements'],
    ['/c/small/flows', 'Flux de déplacements'],
    ['/c/small/constraints', 'Contraintes d’allocation'],
  ]) {
    await page.goto(path);
    await expect(page.getByRole('heading', { name: heading })).toBeVisible();
  }
});

test('language and theme switch instantly', async ({ page }) => {
  await waitClusters(page);
  await page.goto('/c/small');
  await page.getByRole('button', { name: 'English' }).click();
  await expect(page.getByRole('link', { name: 'Topology' })).toBeVisible();
  const before = await page.evaluate(() => document.documentElement.dataset.theme);
  await page.locator('header button').first().click();
  await page.locator('header button').first().click();
  const after = await page.evaluate(() => document.documentElement.dataset.theme);
  expect(after).not.toBe(before);
  await page.reload();
  await expect(page.getByRole('link', { name: 'Topology' })).toBeVisible(); // remembered
});

test('index page: the shard path', async ({ page }) => {
  await waitClusters(page);
  const r = await page.request.get('/api/v1/clusters/small/indices?limit=1');
  const body = await r.json();
  const ix = (body.items ?? body)[0];
  await page.goto(`/c/small/index/${ix.uuid}`);
  await expect(page.getByRole('heading', { name: /Chemin des shards/ })).toBeVisible();
  // One lane per copy, one bar per interval.
  await expect(page.locator('.gantt .bar').first()).toBeVisible();
  await page.getByRole('button', { name: 'Détail des intervalles' }).click();
  await expect(page.getByRole('cell', { name: 'STARTED' }).first()).toBeVisible();
});

test('search "shard N de X" opens the path of that shard', async ({ page }) => {
  await waitClusters(page);
  const r = await page.request.get('/api/v1/clusters/small/indices?limit=1');
  const body = await r.json();
  const ix = (body.items ?? body)[0];
  await page.goto('/c/small');
  const box = page.getByRole('searchbox');
  await box.fill(`shard 0 de ${ix.name}`);
  await expect(page.getByRole('listbox').getByRole('option').first()).toContainText('shard');
  await box.press('Enter');
  await expect(page).toHaveURL(new RegExp(`/c/small/index/${ix.uuid}.*shard=0`));
  await expect(page.getByText('Shard 0 seulement.')).toBeVisible();
});

test('administration screens render', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByText('Authentification désactivée')).toBeVisible();
  await page.getByRole('link', { name: 'Administration' }).click();
  await expect(page.getByRole('heading', { name: 'Administration' })).toBeVisible();
  await expect(page.getByText('Small').first()).toBeVisible();
  for (const tab of ['Collecteurs', 'Rétention et stockage', 'Settings d’index', 'Authentification', 'Haute disponibilité', 'Système']) {
    await page.getByRole('button', { name: tab, exact: true }).click();
    await expect(page).toHaveURL(/tab=/);
  }
  // High availability: a primary without replication watches no standby yet.
  await page.getByRole('button', { name: 'Haute disponibilité', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Standby surveillée' })).toBeVisible();
  // The LDAP form appears with the LDAP mode (not saved here).
  await page.getByRole('button', { name: 'Authentification', exact: true }).click();
  await page.getByRole('combobox').first().selectOption('ldap');
  await expect(page.getByRole('heading', { name: 'Annuaire LDAP' })).toBeVisible();
  await page.getByRole('button', { name: 'Système', exact: true }).click();
  // The audit trail shows the cluster registrations of the test setup.
  await expect(page.getByText('cluster.save').first()).toBeVisible();
});

// Last test: it switches the instance to local authentication.
test('local authentication: login screen, then the application', async ({ page }) => {
  let r = await page.request.put('/api/v1/admin/users/alice', { data: { role: 'admin', password: 'a-long-enough-pw' } });
  expect(r.ok()).toBeTruthy();
  r = await page.request.put('/api/v1/admin/auth', { data: { mode: 'local' } });
  expect(r.ok()).toBeTruthy();
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Connexion' })).toBeVisible();
  expect((await page.request.get('/api/v1/clusters')).status()).toBe(401);
  await page.getByLabel('Utilisateur').fill('alice');
  await page.getByLabel('Mot de passe').fill('wrong-password');
  await page.getByRole('button', { name: 'Se connecter' }).click();
  await expect(page.getByRole('alert')).toHaveText('Identifiants invalides.');
  await page.getByLabel('Mot de passe').fill('a-long-enough-pw');
  await page.getByRole('button', { name: 'Se connecter' }).click();
  await expect(page.getByRole('heading', { name: 'Clusters' })).toBeVisible();
  await expect(page.getByText('alice')).toBeVisible();
  await page.getByRole('button', { name: 'Se déconnecter' }).click();
  await expect(page.getByRole('heading', { name: 'Connexion' })).toBeVisible();
});
