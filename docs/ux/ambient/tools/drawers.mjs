import {createRequire} from 'node:module';
const require = createRequire('/Users/thomasorourke/.claude/skills/gstack/');
const {chromium} = require('playwright-core');
const base = 'http://127.0.0.1:47800/?token=ambient-ui-test#ambient?svc=kind-mesh2/bookinfo/reviews&stab=configuration&drawer=';
const tasks = process.argv.slice(2);
const b = await chromium.launch({executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true});
const ctx = await b.newContext({viewport: {width: 1600, height: 1000}});
await ctx.addCookies([{name: 'flightbay-theme', value: 'light', domain: '127.0.0.1', path: '/'}]);
const p = await ctx.newPage();
const errs = []; p.on('pageerror', e => errs.push(String(e)));
for (const t of tasks) {
  errs.length = 0;
  await p.goto(base + t, {waitUntil: 'load'}); await p.reload(); await p.waitForTimeout(2500);
  const r = await p.evaluate(() => ({title: document.querySelector('.am2-drawer h2')?.textContent, form: document.querySelector('#am2-formcol')?.innerText.slice(0, 70).replace(/\n/g, ' | '), yaml: (document.querySelector('#am-live-yaml')?.textContent || '').split('\n').slice(0, 3).join(' / ').slice(0, 90), effects: document.querySelector('#am2-effects')?.innerText.slice(0, 80).replace(/\n/g, ' | '), err: document.querySelector('.am2-drawer [role=alert]')?.textContent}));
  console.log(t.padEnd(22), JSON.stringify(r), errs.length ? 'PAGEERR ' + errs.join(' ; ') : '');
}
await b.close();
