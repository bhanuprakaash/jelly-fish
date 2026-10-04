#!/usr/bin/env node
// Drives tool calls in the chat (features/tool-calls.md): a batch shows one
// chip per call going running -> done, a call for an unregistered tool shows
// an error chip, and the events match.
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
const chips = (state) => page.locator('main > div.rounded-full').filter({ hasText: state })

// 1. Batch: two parallel sleeps, then a serial slow_side_effect.
const a = await chat('/tool sleep 2s ; sleep 2s ; slow_side_effect 1s', /tools: slept 2s; slept 2s; done after 1s/)
check('batch: two sleep chips done', await until(async () => (await chips('sleep · done').count()) === 2))
check('batch: slow_side_effect chip done', (await chips('slow_side_effect · done').count()) === 1)
const types = psql(`SELECT string_agg(type, ',' ORDER BY seq) FROM events WHERE session_id = '${a}' AND type LIKE 'tool.call.%'`)
check('batch: 3 requested, 3 started, 3 completed', types.split(',').filter((t) => t === 'tool.call.requested').length === 3 && types.split(',').filter((t) => t === 'tool.call.started').length === 3 && types.split(',').filter((t) => t === 'tool.call.completed').length === 3, types)
await r.shot('batch')

// 2. A call for a tool nobody registered: error chip with the error text.
const b = await chat('/tool nosuch x', /tools:/)
check('unknown tool: failed chip with error text', await until(async () => (await chips('nosuch · failed · unknown tool "nosuch"').count()) === 1))
check('unknown tool: no empty bubble', (await page.locator('main > div.rounded-2xl').filter({ hasText: /^$/ }).count()) === 0)
await r.shot('unknown-tool')
console.log(`sessions: ${a} ${b}`)
await r.close()
exit()
