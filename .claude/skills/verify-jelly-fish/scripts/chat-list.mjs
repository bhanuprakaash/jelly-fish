#!/usr/bin/env node
// Drives the Chat List (features/chat-list.md): desktop sidebar create → open
// (replay from seq 0), then the Pixel 7 list → chat → back flow.
// Usage: DATABASE_URL=… node chat-list.mjs [evidence-dir]   (default chat-list)
import { adminEmail, BASE, checker, nav, open, psql, streams } from './lib.mjs'

const dir = process.argv[2] ?? 'chat-list'
const stamp = new Date().toISOString().slice(11, 19)
const long = `Chat list check ${stamp}: a long first message that runs well past the sixty character cut`
const short = `Second chat ${stamp}`
const { check, exit } = checker()

async function send(page, text) {
  await page.getByPlaceholder('Message').fill(text)
  await page.getByRole('button', { name: 'Send' }).click()
}

// Desktop: create two chats from the sidebar, then reopen the first.
{
  const r = await open({ dir, name: 'desktop', device: 'desktop' })
  const { page } = r
  await page.goto(BASE)
  await page.getByRole('button', { name: 'New chat' }).waitFor()
  check('desktop: sidebar shows New chat and the empty pane', await page.getByText('Select a chat or start a new one').isVisible())
  await r.shot('home')

  await page.getByRole('button', { name: 'New chat' }).click()
  await page.waitForURL(/\/s\/[0-9a-f-]{36}$/)
  const firstId = page.url().split('/s/')[1]
  await send(page, long)
  // The row's text moves from the placeholder to the automatic Title, so
  // rows are located by their link.
  const row1 = nav(page).locator(`a[href="/s/${firstId}"]`)
  await row1.waitFor({ timeout: 10_000 })
  check('create: row appears without reload', true, JSON.stringify((await row1.textContent()).trim()))
  await page.getByText(/^echo:/).first().waitFor({ timeout: 30_000 })
  await r.shot('first-chat-replied')

  await page.getByRole('button', { name: 'New chat' }).click()
  await page.waitForURL((u) => !u.pathname.endsWith(firstId))
  const secondId = page.url().split('/s/')[1]
  await send(page, short)
  await nav(page).getByRole('link', { name: short }).waitFor({ timeout: 10_000 })
  await page.getByText(/^echo:/).first().waitFor({ timeout: 30_000 })
  await r.shot('second-chat')

  const list = await (await page.request.get(`${BASE}/api/sessions`)).json()
  const ids = list.map((c) => c.id)
  check('GET /api/sessions newest first', ids[0] === secondId && ids[1] === firstId, `top=${ids.slice(0, 2)}`)
  const sorted = list.every((c, i) => i === 0 || list[i - 1].updated_at >= c.updated_at)
  check('GET /api/sessions ordered by updated_at desc', sorted)
  check('row JSON keys', JSON.stringify(Object.keys(list[0])) === '["id","title","titled","status","updated_at"]', Object.keys(list[0]).join(','))
  // Placeholder: an untitled chat shows its first user message, whitespace
  // collapsed, cut to 60 runes + "…".
  const untitled = list.find((c) => !c.titled && c.title.endsWith('…')) ?? list.find((c) => !c.titled)
  if (untitled && process.env.DATABASE_URL) {
    const parts = JSON.parse(psql(`SELECT payload->'message'->'parts' FROM events WHERE session_id = '${untitled.id}' AND type = 'user.message' ORDER BY seq LIMIT 1`))
    const text = parts.map((p) => p.text ?? '').join('').split(/\s+/).filter(Boolean).join(' ')
    const runes = Array.from(text)
    const want = runes.length > 60 ? runes.slice(0, 60).join('') + '…' : text
    check('placeholder: untitled chat shows its first message cut to 60 + …', untitled.title === want, JSON.stringify(untitled.title))
  } else console.log('SKIP placeholder: no untitled chat in the list')
  if (process.env.DATABASE_URL) {
    const want = psql(`SELECT s.id FROM sessions s JOIN users u ON u.id = s.user_id
      WHERE u.email = '${adminEmail()}' AND s.parent_id IS NULL AND s.trigger = 'user_message'
      ORDER BY s.updated_at DESC, s.id`).split('\n')
    check('list == DB top-level user_message sessions', JSON.stringify(want) === JSON.stringify(ids), `${ids.length} rows`)
  }

  const before = (await streams(page)).length
  await row1.click()
  await page.waitForURL(new RegExp(`/s/${firstId}$`))
  await page.getByText(long, { exact: true }).waitFor()
  const opened = (await streams(page)).slice(before)
  check('open: stream starts at ?after=0', opened.some((s) => s.url.endsWith(`/api/sessions/${firstId}/events?after=0`)), opened.map((s) => s.url).join(' '))
  // The turn may still be streaming: wait for its llm.response, which starts "echo:".
  const reply = await page.getByText(/^echo: Chat list check/).first().waitFor({ timeout: 30_000 }).then(() => true, () => false)
  check('open: replay shows first message and its reply', reply)
  check('open: row is aria-current', (await row1.getAttribute('aria-current')) === 'page')
  await r.shot('reopened-first')

  console.log('chats:', firstId, secondId)
  await r.close()
  globalThis.chats = { firstId, secondId }
}

// Phone: list screen at /, tap a chat, back via "← Chats" and via browser back.
{
  const { firstId } = globalThis.chats
  const r = await open({ dir, name: 'pixel7', device: 'pixel7' })
  const { page } = r
  const row1 = nav(page).locator(`a[href="/s/${firstId}"]`)
  await page.goto(BASE)
  await nav(page).waitFor()
  check('phone: / is the list, no chat pane', !(await page.getByText('Select a chat or start a new one').isVisible()))
  await r.shot('list')

  await row1.tap()
  await page.waitForURL(new RegExp(`/s/${firstId}$`))
  await page.getByText(long, { exact: true }).waitFor()
  check('phone: tap opens /s/{id}, list hidden', !(await nav(page).isVisible()))
  await r.shot('chat')

  await page.getByRole('link', { name: '← Chats' }).tap()
  await page.waitForURL(`${BASE}/`)
  check('phone: ← Chats returns to the list', await nav(page).isVisible())
  await r.shot('back-link')

  await row1.tap()
  await page.waitForURL(new RegExp(`/s/${firstId}$`))
  await page.goBack()
  await page.waitForURL(`${BASE}/`)
  check('phone: browser back returns to the list', await nav(page).isVisible())
  await r.shot('browser-back')
  console.log('evidence:', await r.close())
}

exit()
