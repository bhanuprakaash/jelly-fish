#!/usr/bin/env node
// Drives Connectors in Settings (features/connectors.md): add a public MCP
// server (probe, per-tool switches, save), refuse bad addresses and a taken
// edited slug, number a repeated name's slug, toggle a tool on the row, and remove with the confirm step.
// Usage: [JF_MCP_URL=https://…/mcp] node connectors.mjs <desktop|pixel7> [evidence-dir]
import { BASE, checker, open, seen, gone, until } from './lib.mjs'

const device = process.argv[2] ?? 'desktop'
const dir = process.argv[3] ?? 'connectors'
const phone = device === 'pixel7'
const MCP_URL = process.env.JF_MCP_URL ?? 'https://mcp.deepwiki.com/mcp'
const { check, exit } = checker(`[${device}] `)
const tap = (loc) => (phone ? loc.tap() : loc.click())

const r = await open({ dir, name: device, device })
const { page } = r
const api = (path, method = 'GET') =>
  page.evaluate(([path, method]) => fetch(path, { method }).then(async (res) => ({ status: res.status, body: res.ok && res.status !== 204 ? await res.json() : null })), [path, method])
const connectors = async () => (await api('/api/connectors')).body
const bySlug = async (slug) => (await connectors()).find((c) => c.slug === slug)

await page.goto(`${BASE}/settings`)
await page.getByRole('heading', { name: 'Connectors' }).waitFor()
// A rerun starts clean: drop connectors this drive left behind.
for (const slug of ['deepwiki', 'deepwiki2', 'keycheck']) {
  const c = await bySlug(slug)
  if (c) await api(`/api/connectors/${c.id}`, 'DELETE')
}
await page.reload()
await page.getByRole('heading', { name: 'Connectors' }).waitFor()
const section = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Connectors' }) })
const card = (text) => section.locator('div.rounded-card').filter({ hasText: text })
const box = async (loc) => (await loc.boundingBox()) ?? { width: 0, height: 0 }

const name = page.getByRole('textbox', { name: 'Name', exact: true })
const slug = page.getByRole('textbox', { name: 'Slug' })
const url = page.getByRole('textbox', { name: 'URL' })
const probe = section.getByRole('button', { name: 'Probe' })
const save = section.getByRole('button', { name: 'Save' })
const alert = section.getByRole('alert')

const had = (await connectors()).length
if (had === 0) check('empty state: "No connectors yet."', await seen(section.getByText('No connectors yet.')))

// Name derives the slug live; no Save before a probe.
await name.fill('Deep Wiki')
check('slug follows the name: deepwiki', (await slug.inputValue()) === 'deepwiki')
check('no Save button before a probe', (await save.count()) === 0)
check('Probe is disabled without a URL', await probe.isDisabled())
check('touch targets: Probe and the inputs are at least 44px tall', (await box(probe)).height >= 44 && (await box(name)).height >= 44)

// A private address is refused with the api's message, and no Save appears.
await url.fill('https://127.0.0.1/mcp')
await tap(probe)
check('private address: "This address can\'t be used"', await seen(alert.filter({ hasText: "This address can't be used" })))
check('private address: no Save button', (await save.count()) === 0)
await r.shot('refused')

// Probe the real server.
await url.fill(MCP_URL)
check('editing the URL clears the message', await gone(alert, 5000))
await tap(probe)
const found = section.getByText(/^\d+ tools? found$/)
check('probe: "N tools found"', await seen(found, 30_000), await found.textContent().catch(() => ''))
const n = Number((await found.textContent())?.match(/\d+/)?.[0] ?? 0)
const toolSwitches = section.locator('form').getByRole('switch').filter({ hasNotText: 'Uses an API key' })
check(`probe: ${n} switches, all on`, n > 0 && (await toolSwitches.count()) === n && (await toolSwitches.evaluateAll((els) => els.every((e) => e.getAttribute('aria-checked') === 'true'))))
check('touch targets: a tool switch row is at least 44px tall', (await box(toolSwitches.first())).height >= 44)
const firstTool = (await toolSwitches.first().getAttribute('aria-label')) ?? ''
await tap(toolSwitches.first())
check('toggle one tool off before saving', (await toolSwitches.first().getAttribute('aria-checked')) === 'false', firstTool)
await r.shot('probed')

// Save: the row shows the slug and the right counts, and the form resets.
await tap(save)
const row = card('Deep Wiki').filter({ hasText: 'deepwiki' }).first()
check('save: the row appears', await seen(row))
check('row shows slug deepwiki (mono)', (await row.locator('span.font-mono').first().textContent()) === 'deepwiki')
check('row shows the URL', (await row.textContent())?.includes(MCP_URL) ?? false)
check(`row shows "${n - 1} of ${n} tools on"`, await seen(row.getByRole('button', { name: `${n - 1} of ${n} tools on` })))
check('form reset after save', (await name.inputValue()) === '' && (await url.inputValue()) === '')
check('slug is not editable on the saved row', (await row.getByRole('textbox').count()) === 0)
const saved = await bySlug('deepwiki')
check('api: era set, no auth header, one tool off', !!saved && ['modern', 'legacy'].includes(saved.era) && saved.auth_header === '' && saved.tools.filter((t) => t.enabled).length === n - 1 && saved.tools.find((t) => !t.enabled)?.name === firstTool, saved ? `${saved.era} ${saved.tools.map((t) => `${t.name}:${t.enabled}`).join(' ')}` : 'missing')
check('no horizontal overflow', await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
await r.shot('saved')

// A second Connector with the same name and an untouched slug gets the next free one.
await name.fill('Deep Wiki')
await url.fill(MCP_URL)
await tap(probe)
await seen(found, 30_000)
await tap(save)
check('same name, untouched slug: saved as deepwiki2', await seen(section.locator('span.font-mono').filter({ hasText: /^deepwiki2$/ })))
const dup = await bySlug('deepwiki2')
check('api: deepwiki2 exists beside one deepwiki', !!dup && (await connectors()).filter((c) => c.slug === 'deepwiki').length === 1)
if (dup) await api(`/api/connectors/${dup.id}`, 'DELETE')
await page.reload()
await page.getByRole('heading', { name: 'Connectors' }).waitFor()

// An edited slug that is taken is refused inline.
await name.fill('Deep Wiki')
await slug.fill('x')
await slug.fill('deepwiki')
await url.fill(MCP_URL)
await tap(probe)
await seen(found, 30_000)
await tap(save)
check('taken edited slug: "That slug is taken"', await seen(alert.filter({ hasText: 'That slug is taken' })))
check('taken edited slug: still one deepwiki connector', (await connectors()).filter((c) => c.slug === 'deepwiki').length === 1)
await r.shot('slug-taken')
await slug.fill('keycheck')
check('editing the slug clears the message', await gone(alert, 5000))
await name.fill('Key Check')
check('an edited slug stops following the name', (await slug.inputValue()) === 'keycheck')

// An API key: header and secret under the toggle; sent, never shown again.
await tap(section.getByRole('switch', { name: 'Uses an API key' }))
await section.getByRole('textbox', { name: 'Header name' }).fill('X-API-Key')
await section.getByRole('textbox', { name: 'Secret' }).fill('not-a-real-key')
await tap(probe)
await seen(found, 30_000)
await tap(save)
const keyRow = card('Key Check').filter({ hasText: 'keycheck' }).first()
check('API-key connector: the row appears', await seen(keyRow))
const keyed = await bySlug('keycheck')
check('API-key connector: api returns the header name, never the secret', keyed?.auth_header === 'X-API-Key' && !JSON.stringify(keyed).includes('not-a-real-key'))

// Toggle a tool on the row: expand, flip, count and api follow.
await tap(row.getByRole('button', { name: `${n - 1} of ${n} tools on` }))
const rowTool = row.getByRole('switch', { name: firstTool })
check('expand: the row lists its tools', await seen(rowTool) && (await row.getByRole('switch').count()) === n)
await tap(rowTool)
check(`toggle on the row: "${n} of ${n} tools on"`, await seen(row.getByRole('button', { name: `${n} of ${n} tools on` })))
check('api: the tool is on', await until(async () => (await bySlug('deepwiki')).tools.every((t) => t.enabled)))
await r.shot('expanded')
await page.reload()
check('after reload: the row still reads all tools on', await seen(card('Deep Wiki').getByRole('button', { name: `${n} of ${n} tools on` })))

// Remove: confirm step, no key line without a key, Cancel keeps the row.
const row2 = card('Deep Wiki').filter({ hasText: 'deepwiki' }).first()
await tap(row2.getByRole('button', { name: 'Remove' }))
check('remove: "Remove Deep Wiki?"', await seen(row2.getByText('Remove Deep Wiki?', { exact: true })))
check('remove: no key line without a key', (await row2.getByText(/Also revoke this key/).count()) === 0)
await r.shot('confirm')
await tap(row2.getByRole('button', { name: 'Cancel' }))
check('cancel keeps the row and its Remove button', await seen(row2.getByRole('button', { name: 'Remove' })))
await tap(row2.getByRole('button', { name: 'Remove' }))
await tap(row2.getByRole('group').getByRole('button', { name: 'Remove' }))
check('confirm: the row is gone', await gone(card('Deep Wiki').filter({ hasText: 'deepwiki' }), 15_000))
check('api: deepwiki is gone', (await bySlug('deepwiki')) === undefined)

const keyRow2 = card('Key Check').filter({ hasText: 'keycheck' }).first()
await tap(keyRow2.getByRole('button', { name: 'Remove' }))
check("remove with a key: \"Also revoke this key in Key Check's settings\"", await seen(keyRow2.getByText("Also revoke this key in Key Check's settings", { exact: true })))
await r.shot('confirm-key')
await tap(keyRow2.getByRole('group').getByRole('button', { name: 'Remove' }))
check('confirm: the key row is gone', await gone(card('Key Check').filter({ hasText: 'keycheck' }), 15_000))
check('api: keycheck is gone', (await bySlug('keycheck')) === undefined)
await r.shot('done')

console.log('evidence:', await r.close())
exit()
