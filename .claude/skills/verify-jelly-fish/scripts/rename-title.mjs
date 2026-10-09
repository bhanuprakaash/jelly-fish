#!/usr/bin/env node
// Drives rename and the automatic Title (features/rename-title.md): a new
// chat gets its Title from the first message with no Turn or bubble for it;
// rename from the list's "⋯" menu updates the open chat's header; the
// rename validation answers.
// Usage: DATABASE_URL=… node rename-title.mjs [evidence-dir]
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { BASE, checker, fakeTitle, header, nav, open, psql, row, until } from './lib.mjs'

const dir = process.argv[2] ?? 'rename-title'
const stamp = new Date().toISOString().slice(11, 19)
const { check, exit } = checker()
const put = (page, id, title) =>
  page.evaluate(
    ([id, title]) =>
      fetch(`/api/sessions/${id}/title`, { method: 'PUT', body: JSON.stringify({ title }) }).then(async (r) => ({
        status: r.status,
        body: await r.text(),
      })),
    [id, title],
  )
const log = []

const r = await open({ dir, name: 'desktop', device: 'desktop' })
const { page } = r
await page.goto(BASE)
await nav(page).waitFor()

// 1. Automatic Title on a new chat (Fake Provider: the first message,
// whitespace collapsed, cut to 60 runes, no "…").
const first = `Rename check ${stamp}:   please   plan a weekend trip to the hills with friends and food`
const auto = fakeTitle(first)
await page.getByRole('button', { name: 'New chat' }).click()
await page.waitForURL(/\/s\/[0-9a-f-]{36}$/)
const id = page.url().split('/s/')[1]
await page.getByPlaceholder('Message').fill(first)
await page.getByRole('button', { name: 'Send' }).click()
await page.getByText(/^echo: Rename check/).first().waitFor({ timeout: 30_000 })
check('auto Title: header shows it', await until(async () => (await header(page).textContent())?.trim() === auto), JSON.stringify(auto))
check('auto Title: list row shows it (no …)', await until(async () => (await row(page, auto).count()) === 1))
log.push(`# events of ${id}\n${psql(`SELECT seq || ' ' || type || ' ' || payload::text FROM events WHERE session_id = '${id}' ORDER BY seq`)}`)
const renamed = psql(`SELECT payload->>'by' || '|' || (payload->>'title') FROM events WHERE session_id = '${id}' AND type = 'session.renamed'`)
check('auto Title: one session.renamed{by:auto}', renamed === `auto|${auto}`, renamed)
check('auto Title: sessions.title is its projection', psql(`SELECT title FROM sessions WHERE id = '${id}'`) === auto)
const turns = psql(`SELECT count(*) FROM events WHERE session_id = '${id}' AND type = 'turn.started'`)
const usage = psql(`SELECT count(*) FROM events WHERE session_id = '${id}' AND type = 'usage.recorded'`)
check('auto Title: not a Turn (one turn.started); its usage.recorded is on the session', turns === '1' && Number(usage) >= 2, `turn.started=${turns} usage.recorded=${usage}`)
const bubbles = await page.locator('main > div > div.whitespace-pre-wrap').count()
check('auto Title: nothing in the chat for it (2 bubbles)', bubbles === 2, `${bubbles} bubbles`)
await r.shot('auto-title')

// 2. Rename from the list menu while the chat is open.
await row(page, auto).getByRole('button', { name: 'Chat options' }).click()
await r.shot('menu')
await page.getByRole('menuitem', { name: 'Rename' }).click()
const input = nav(page).getByRole('textbox', { name: 'Title' })
await input.fill('   ')
check('rename UI: Save disabled for a blank title', await nav(page).getByRole('button', { name: 'Save' }).isDisabled())
await input.fill('x'.repeat(101))
await nav(page).getByRole('button', { name: 'Save' }).click()
check('rename UI: 101 chars shows the alert', await nav(page).getByRole('alert').waitFor({ timeout: 5000 }).then(() => true, () => false))
await r.shot('too-long')
const mine = `My trip ${stamp}`
await input.fill(`   ${mine}   `)
await input.press('Enter')
check('rename: list row shows the trimmed Title', await row(page, mine).waitFor({ timeout: 10_000 }).then(() => true, () => false))
check('rename: open chat header updates from session.renamed', await until(async () => (await header(page).textContent())?.trim() === mine))
check('rename: DB title and by=user', psql(`SELECT title FROM sessions WHERE id = '${id}'`) === mine &&
  psql(`SELECT payload->>'by' FROM events WHERE session_id = '${id}' AND type = 'session.renamed' ORDER BY seq DESC LIMIT 1`) === 'user')
await r.shot('renamed')

// 3. Validation through the API the menu uses.
for (const [label, title, want] of [
  ['empty', '', 400],
  ['blank', '    ', 400],
  ['101 chars', 'y'.repeat(101), 400],
  ['100 multi-byte chars', 'é'.repeat(100), 204],
]) {
  const res = await put(page, id, title)
  log.push(`PUT /api/sessions/${id}/title ${label} → ${res.status} ${res.body}`)
  check(`PUT title ${label} → ${want}`, res.status === want, `${res.status} ${res.body}`)
}
await put(page, id, mine)
const child = psql(`INSERT INTO sessions (id, workspace_id, project_id, user_id, agent_id, status, parent_id, depth)
  SELECT gen_random_uuid(), workspace_id, project_id, user_id, agent_id, 'completed', id, 1 FROM sessions WHERE id = '${id}' RETURNING id`).split('\n')[0]
const childRes = await put(page, child, 'child title')
log.push(`PUT /api/sessions/${child}/title (child) → ${childRes.status} ${childRes.body}`)
check('PUT title on a Child Session refused (422)', childRes.status === 422, `${childRes.status} ${childRes.body}`)
psql(`DELETE FROM sessions WHERE id = '${child}'`)

writeFileSync(join(r.out, 'db-and-api.txt'), log.join('\n\n') + '\n')
writeFileSync(join(r.out, 'test-sessions.txt'), id + '\n')
console.log('chat:', id)
console.log('evidence:', await r.close())
exit()
