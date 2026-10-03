#!/usr/bin/env node
// Drives the web shell (features/web-shell.md): the signed-in Shell on
// desktop and phone, its loading and error states, the login screen, the
// Settings and Admin routes, and the SPA fallback. Loading and error states
// are produced with page.route in the browser only (service workers
// blocked so it sees every fetch); the cluster is untouched.
// Usage: node web-shell.mjs [evidence-dir]
import { BASE, checker, nav, open, seen, sleep } from './lib.mjs'

const dir = process.argv[2] ?? 'web-shell'
const { check, exit } = checker()
const visible = (loc) => loc.isVisible()

{
  // The PWA's service worker would carry the page's fetches past page.route.
  const r = await open({ dir, name: 'desktop', device: 'desktop', contextOptions: { serviceWorkers: 'block' } })
  const { page } = r
  await page.goto(BASE)
  check('desktop: Shell with New chat, Chats nav, empty pane, Settings link',
    (await seen(page.getByRole('button', { name: 'New chat' }))) &&
      (await visible(nav(page))) &&
      (await visible(page.getByText('Select a chat or start a new one'))) &&
      (await visible(page.getByRole('link', { name: 'Settings' }))))
  await r.shot('home')

  await page.goto(`${BASE}/settings`)
  check('route /settings: Account', await seen(page.getByRole('heading', { name: 'Account' })))
  await page.goto(`${BASE}/admin/users`)
  check('route /admin/users: Users', await seen(page.getByRole('heading', { name: 'Users', level: 1 })))
  await r.shot('admin-users')

  await page.route('**/api/sessions', async (route) => {
    await sleep(1500)
    await route.continue()
  })
  await page.goto(BASE)
  check('loading: list shows Loading… first', await seen(page.getByText('Loading…'), 1000))
  check('loading: then the list', await seen(nav(page)))
  await page.unroute('**/api/sessions')

  await page.route('**/api/sessions', (route) => route.fulfill({ status: 500, body: '{"error":"x"}' }))
  await page.goto(BASE)
  check('error: "Could not load chats."', await seen(page.getByText('Could not load chats.')))
  await r.shot('chats-error')
  await page.unroute('**/api/sessions')

  await page.route('**/api/me', (route) => route.abort())
  await page.goto(BASE)
  check('error: "Could not reach the server."', await seen(page.getByText('Could not reach the server.')))
  await r.shot('server-error')
  await page.unroute('**/api/me')

  const fallback = await page.request.get(`${BASE}/some/deep/path`)
  check('SPA fallback: deep path serves index.html', fallback.status() === 200 && (await fallback.text()).includes('<div id="root">'))
  await r.close()
}

{
  const r = await open({ dir, name: 'pixel7', device: 'pixel7' })
  const { page } = r
  await page.goto(BASE)
  check('phone: / is the list, no empty pane', (await seen(nav(page))) && !(await visible(page.getByText('Select a chat or start a new one'))))
  await r.shot('home')
  await r.close()
}

{
  const r = await open({ dir, name: 'signed-out', device: 'desktop', signedIn: false })
  const { page } = r
  await page.goto(BASE)
  check('signed out: login screen', (await seen(page.getByRole('heading', { name: 'Jelly-fish' }))) &&
    (await visible(page.getByLabel('Email'))) && (await visible(page.getByRole('button', { name: 'Email me a code' }))))
  await r.shot('login')
  console.log('evidence:', await r.close())
}

exit()
