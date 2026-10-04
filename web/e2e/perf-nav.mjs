// Cost of the screen transitions: time from a click to the new screen being
// interactive, with the transitions and with "reduce motion" (none).
// Usage: node e2e/perf-nav.mjs http://127.0.0.1:18081 <cluster>
import { chromium } from '@playwright/test';

const [base = 'http://127.0.0.1:18081', cluster = 'c'] = process.argv.slice(2);
const browser = await chromium.launch();

async function run(reducedMotion) {
  const ctx = await browser.newContext({ viewport: { width: 1500, height: 950 }, reducedMotion });
  const page = await ctx.newPage();
  await page.goto(`${base}/c/${cluster}`);
  await page.locator('button.tile').first().waitFor();
  const tiles = await page.locator('button.tile').count();
  const down = [];
  const up = [];
  for (let i = 0; i < 8; i++) {
    let t0 = Date.now();
    await page.locator('button.tile').nth(i * 3).click();
    await page.locator('main h1').filter({ hasText: /-/ }).waitFor();
    await page.locator('figure').first().waitFor(); // node metrics drawn
    down.push(Date.now() - t0);
    t0 = Date.now();
    await page.goBack();
    await page.locator('button.tile').first().waitFor();
    await page.locator('button.tile').first().click({ trial: true }); // interactive again
    up.push(Date.now() - t0);
  }
  await ctx.close();
  const med = (a) => [...a].sort((x, y) => x - y)[Math.floor(a.length / 2)];
  return { tiles, down: med(down), up: med(up), downAll: down, upAll: up };
}

const off = await run('reduce');
const on = await run('no-preference');
console.log(JSON.stringify({ cluster, withoutTransitions: off, withTransitions: on }, null, 1));
await browser.close();
