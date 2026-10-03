#!/usr/bin/env node
// The S5 demo on one device profile (features/s5-demo.md): two chats running
// with live badges, a new chat gets its Title, rename it, then hide the page
// and show it again while the two chats finish, and check every stream comes
// back. On a phone the list and the chat are separate screens.
// Usage: DATABASE_URL=… node s5-demo.mjs <desktop|pixel7> [evidence-dir]
import { BASE, badge, checker, fakeTitle, header, nav, open, psql, row, seen, setVisibility, sleep, streams, until } from './lib.mjs'

const device = process.argv[2] ?? 'desktop'
const dir = process.argv[3] ?? 's5-demo'
const phone = device === 'pixel7'
const stamp = new Date().toISOString().slice(11, 19)
const { check, exit } = checker(`[${device}] `)
const tap = (loc) => (phone ? loc.tap() : loc.click())
const backToList = async (page) => {
  if (!phone) return
  await tap(page.getByRole('link', { name: '← Chats' }))
  await page.waitForURL(`${BASE}/`)
}

const r = await open({ dir, name: device, device })
const { page, context } = r
await page.goto(BASE)
await nav(page).waitFor()

// Two chats running, started from another tab.
const tab2 = await context.newPage()
await tab2.goto(BASE)
// The Title drops the /slow prefix, so rows are matched on the rest.
const slow = [`demo one ${device} ${stamp}`, `demo two ${device} ${stamp}`]
const slowIds = []
for (const text of slow) {
  const message = `/slow 45s ${text}`
  const id = crypto.randomUUID()
  await tab2.evaluate(
    ([id, message]) =>
      fetch('/api/sessions', {
        method: 'POST',
        body: JSON.stringify({ session_id: id, client_msg_id: crypto.randomUUID(), message }),
      }),
    [id, message],
  )
  slowIds.push(id)
}
await tab2.close()
check('two chats running: both rows spin', (await seen(badge(page, slow[0], 'running'))) && (await seen(badge(page, slow[1], 'running'))))
await r.shot('two-running')

// A new chat gets its Title.
const first = `Demo ${device} ${stamp}: what should I cook tonight with rice, eggs and spinach`
const auto = fakeTitle(first)
await tap(page.getByRole('button', { name: 'New chat' }))
await page.waitForURL(/\/s\/[0-9a-f-]{36}$/)
const id = page.url().split('/s/')[1]
await page.getByPlaceholder('Message').fill(first)
await tap(page.getByRole('button', { name: 'Send' }))
check('new chat: header gets the automatic Title', await until(async () => (await header(page).textContent())?.trim() === auto), auto)
await r.shot('auto-title')

// Rename it from the list.
await backToList(page)
check('new chat: list row shows the Title', await seen(row(page, auto)))
await tap(row(page, auto).getByRole('button', { name: 'Chat options' }))
await tap(page.getByRole('menuitem', { name: 'Rename' }))
const renamed = `Dinner ${device} ${stamp}`
await nav(page).getByRole('textbox', { name: 'Title' }).fill(renamed)
await tap(nav(page).getByRole('button', { name: 'Save' }))
check('rename: row shows the new Title', await seen(row(page, renamed)))
await r.shot('renamed-row')
await tap(row(page, renamed).getByRole('link'))
await page.waitForURL(new RegExp(`/s/${id}$`))
check('rename: chat header shows it', await until(async () => (await header(page).textContent())?.trim() === renamed))
// On a phone the list is off-screen here, so ask the DB.
check('still running: both slow chats', psql(`SELECT count(*) FROM sessions WHERE id IN ('${slowIds.join("','")}') AND status IN ('runnable', 'running')`) === '2')
await page.getByText(/^echo: Demo/).first().waitFor({ timeout: 30_000 })
await r.shot('chat-open')

// Hide while the two chats are still running; they finish while hidden.
const stillRunning = psql(`SELECT count(*) FROM sessions WHERE id IN ('${slowIds.join("','")}') AND status <> 'awaiting_user'`)
const lastSeq = psql(`SELECT last_seq FROM sessions WHERE id = '${id}'`)
const before = (await streams(page)).length
await setVisibility(page, 'hidden')
await sleep(300)
const hidden = await streams(page)
check('hide: every EventSource closed', hidden.every((s) => s.readyState === 2), `${hidden.length} streams, ${stillRunning} slow chats still running`)
await until(async () => psql(`SELECT count(*) FROM sessions WHERE id IN ('${slowIds.join("','")}') AND status = 'awaiting_user'`) === '2', 90_000)
await setVisibility(page, 'visible')
await sleep(1000)
const reopened = (await streams(page)).slice(before)
check('show: Session stream reopens with ?after=lastSeq', reopened.some((s) => s.url.endsWith(`/api/sessions/${id}/events?after=${lastSeq}`) && s.readyState === 1), reopened.map((s) => s.url).join(' '))
check('show: Activity Stream reopens', reopened.some((s) => s.url.endsWith('/api/activity') && s.readyState === 1))
await backToList(page)
check('show: fresh snapshot clears both spinners', (await badge(page, slow[0], 'running').count()) === 0 && (await badge(page, slow[1], 'running').count()) === 0)
await r.shot('after-show')

console.log('chats:', id, slowIds.join(' '))
console.log('evidence:', await r.close())
exit()
