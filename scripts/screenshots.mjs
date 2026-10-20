// Takes the README screenshots: starts dbexplore on a temporary port against the demo database
// (make demo-db), drives the UI with Playwright and writes docs/screenshots/*.png.
// Run it with `make screenshots`. Set CHROME to a Chrome/Chromium binary to use instead of
// Playwright's bundled browser.
import { chromium } from 'playwright';
import { spawn } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const out = join(root, 'docs', 'screenshots');
const dsn = process.env.DEMO_DSN || 'postgres://explorer:explorer@127.0.0.1:55432/postgres?sslmode=disable';
const addr = '127.0.0.1:7899', base = `http://${addr}`;

// Start dbexplore with a throwaway config
const dir = mkdtempSync(join(tmpdir(), 'dbexplore-shots-'));
writeFileSync(join(dir, 'config.json'), JSON.stringify({
  listen: addr, password: 'screenshots',
  sources: [{ id: 'demo', name: 'Shop (replica)', dsn }, { id: 'reports', name: 'Reporting', dsn }],
}));
const server = spawn(join(root, 'dbexplore'), ['-config', join(dir, 'config.json')], { stdio: 'inherit' });
await new Promise(r => setTimeout(r, 1000));

const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
try {
  const page = await (await browser.newContext({ viewport: { width: 1280, height: 760 }, deviceScaleFactor: 2 })).newPage();
  const shot = async name => { await page.waitForTimeout(400); await page.screenshot({ path: join(out, `${name}.png`) }); console.log('wrote', name) };
  await page.goto(base);
  await page.fill('#pw', 'screenshots');
  await page.press('#pw', 'Enter');
  await page.waitForURL(base + '/');
  await page.click('.node[data-key="demo"]');
  await page.click('.node[data-key="demo/shop"]');

  // Table with a filter, a sort and an exact count
  await page.click('.node[data-key="demo/shop/order"]');
  await page.waitForSelector('table.grid tbody tr');
  await page.fill('[data-f="where"]', "status = 'shipped' and total > 1000");
  await page.fill('[data-f="order"]', 'placed_at desc');
  await page.press('[data-f="order"]', 'Enter');
  await page.waitForTimeout(400);
  await page.click('[data-a="count"]');
  await page.click('table.grid tbody tr:nth-child(3) td:nth-child(4)');
  await page.click('table.grid tbody tr:nth-child(6) td:nth-child(4)', { modifiers: ['Shift'] });
  await shot('table');

  // Row view of a customer, reached through the order's foreign key
  await page.click('table.grid tbody tr:nth-child(2) .fk');
  await page.waitForSelector('table.grid tbody tr');
  await page.dblclick('table.grid tbody tr:first-child td:nth-child(3)');
  await page.waitForSelector('#modal.on');
  await shot('row');
  await page.keyboard.press('Escape');

  // Cards view
  await page.click('.node[data-key="demo/shop/product"]');
  await page.waitForSelector('table.grid tbody tr');
  await page.click('[data-a="vcards"]');
  await shot('cards');

  // Query console
  await page.click('#newConsole');
  await page.fill('[data-f="sql"]', `select c.name, c.country, count(*) as orders, sum(o.total) as revenue
from shop.customer c
join shop."order" o on o.customer_id = c.id
where o.status <> 'cancelled'
group by c.id
order by revenue desc`);
  await page.keyboard.press('Control+Enter');
  await page.waitForSelector('table.grid tbody tr');
  await shot('console');

  // DDL
  await page.click('.tab:has-text("order")');
  await page.click('[data-a="ddl"]');
  await page.waitForSelector('#modal.on pre');
  await shot('ddl');
} finally {
  await browser.close();
  server.kill();
}
