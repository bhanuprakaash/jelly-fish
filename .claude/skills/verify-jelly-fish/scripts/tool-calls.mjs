#!/usr/bin/env node
// Drives tool calls in the chat (features/tool-calls.md): a batch shows one
// row per call going running -> done, a call for an unregistered tool shows
// a failed row with its error note, and the events match.
// Usage: DATABASE_URL=… node tool-calls.mjs [evidence-dir]
import { BASE, checker, nav, open, psql, until } from './lib.mjs'

const dir = process.argv[2] ?? 'tool-calls'
const { check, exit } = checker()
const r = await open({ dir, name: 'desktop', device: 'desktop' })
const { page } = r
await page.goto(BASE)
await nav(page).waitFor()

async function chat(message, reply) {
  await page.getByRole('button', { name: 'New chat' }).click()
  await page.waitForURL(/\/s\/[0-9a-f-]{36}$/)
  const id = page.url().split('/s/')[1]
  await page.getByPlaceholder('Message').fill(message)
  await page.getByRole('button', { name: 'Send' }).click()
  await page.getByText(reply).first().waitFor({ timeout: 60_000 })
  return id
}
const rows = page.locator('main .rounded-card button[aria-expanded]')
const named = (name) => rows.filter({ has: page.locator('span.font-mono', { hasText: new RegExp(`^${name}$`) }) })

// 1. Batch: two parallel sleeps, then a serial slow_side_effect.
const a = await chat('/tool sleep 2s ; sleep 2s ; slow_side_effect 1s', /tools: slept 2s; slept 2s; done after 1s/)
check('batch: two sleep rows done', await until(async () => (await named('sleep').filter({ hasText: 'done' }).count()) === 2))
check('batch: slow_side_effect row done', (await named('slow_side_effect').filter({ hasText: 'done' }).count()) === 1)
const look = await rows.evaluateAll((els) =>
  els.map((el) => ({
    circle: getComputedStyle(el.querySelector('span.rounded-full')).backgroundColor,
    summary: el.querySelector('span.truncate')?.textContent ?? '',
    duration: el.lastElementChild.classList.contains('text-xs') ? el.lastElementChild.textContent : '',
  })),
)
check('batch: each row has a tinted state circle', look.length === 3 && look.every((l) => l.circle !== 'rgba(0, 0, 0, 0)'), look.map((l) => l.circle).join(' | '))
check('batch: each row has an arg summary and a duration', look.every((l) => l.summary && /^\d+(\.\d)?s$|^\d+ms$/.test(l.duration)), look.map((l) => `${l.summary} ${l.duration}`).join(' | '))
const types = psql(`SELECT string_agg(type, ',' ORDER BY seq) FROM events WHERE session_id = '${a}' AND type LIKE 'tool.call.%'`)
check('batch: 3 requested, 3 started, 3 completed', types.split(',').filter((t) => t === 'tool.call.requested').length === 3 && types.split(',').filter((t) => t === 'tool.call.started').length === 3 && types.split(',').filter((t) => t === 'tool.call.completed').length === 3, types)
await rows.first().click()
const args = page.locator('main .rounded-card pre').first()
check('batch: a row expands to its args', (await rows.first().getAttribute('aria-expanded')) === 'true' && (await args.isVisible()), JSON.stringify(await args.textContent()))
await r.shot('batch')

// 2. A call for a tool nobody registered: failed row with the error as a note line.
const b = await chat('/tool nosuch x', /tools:/)
const bad = named('nosuch')
check('unknown tool: failed row', await until(async () => (await bad.filter({ hasText: 'failed' }).count()) === 1))
const note = bad.locator('span.line-clamp-2')
check('unknown tool: danger note line with error text', (await note.textContent()) === 'unknown tool "nosuch"' && /text-danger/.test(await note.getAttribute('class')), JSON.stringify(await note.textContent()))
check('unknown tool: no empty bubble', (await page.locator('main div.whitespace-pre-wrap').filter({ hasText: /^$/ }).count()) === 0)
await r.shot('unknown-tool')
console.log(`sessions: ${a} ${b}`)
await r.close()
exit()
