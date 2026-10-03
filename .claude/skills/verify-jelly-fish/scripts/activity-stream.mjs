#!/usr/bin/env node
// Drives the Activity Stream and live badges (features/activity-stream.md):
// a chat started in another tab reaches the list, two running chats badge
// live, needs-approval via a child, failed, hide/show reconnect, and the
// shared 20-stream cap. Approval and failed have no product trigger with the
// Fake Provider, so the drive sets the status by SQL and sends the same
// jf_activity hint Store.Append would.
// Usage: DATABASE_URL=… node activity-stream.mjs [evidence-dir]
// Needs the :9090 forward for the metrics check (skipped without it).
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { BASE, open, psql, setVisibility, sleep, streams } from './lib.mjs'

const dir = process.argv[2] ?? 'activity-stream'
const stamp = new Date().toISOString().slice(11, 19)
const results = []
const check = (name, ok, detail = '') => {
  results.push({ name, ok, detail })
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`)
}
const nav = (page) => page.getByRole('navigation', { name: 'Chats' })
const row = (page, title) => nav(page).getByRole('listitem').filter({ hasText: title })
const badge = (page, title, label) => row(page, title).getByRole('img', { name: label })
const seen = (loc, timeout = 15_000) => loc.waitFor({ timeout }).then(() => true, () => false)
const gone = (loc, timeout = 60_000) =>
  loc.waitFor({ state: 'detached', timeout }).then(() => true, () => false)
const hint = (sid) => psql(`SELECT pg_notify('jf_activity', user_id || ':' || id) FROM sessions WHERE id = '${sid}'`)

const r = await open({ dir, name: 'desktop', device: 'desktop' })
const { page, context } = r
await page.goto(BASE)
await nav(page).waitFor()
await r.shot('list')

// Tab 2 starts two slow chats through the same API the PWA uses.
const tab2 = await context.newPage()
await tab2.goto(BASE)
const titles = [`/slow 25s alpha ${stamp}`, `/slow 25s beta ${stamp}`]
const ids = []
for (const message of titles) {
  const id = crypto.randomUUID()
  const status = await tab2.evaluate(
    ([id, message]) =>
      fetch('/api/sessions', {
        method: 'POST',
        body: JSON.stringify({ session_id: id, client_msg_id: crypto.randomUUID(), message }),
      }).then((r) => r.status),
    [id, message],
  )
  ids.push(id)
  console.log('created', id, status)
}
await tab2.close()
const [a, b] = ids

check('other tab: both new chats reach the list (D17/D19)', await seen(row(page, titles[1])) && await seen(row(page, titles[0])))
check('two chats running: both rows show the spinner', await seen(badge(page, titles[0], 'running')) && await seen(badge(page, titles[1], 'running')))
await r.shot('two-running')
check('turns end: both spinners clear live', await gone(badge(page, titles[0], 'running')) && await gone(badge(page, titles[1], 'running')))
await r.shot('both-idle')

// Needs approval: a child of chat A enters awaiting_approval.
const child = psql(`INSERT INTO sessions (id, workspace_id, project_id, user_id, agent_id, status, parent_id, depth)
  SELECT gen_random_uuid(), workspace_id, project_id, user_id, agent_id, 'awaiting_approval', id, 1
  FROM sessions WHERE id = '${a}' RETURNING id`).split('\n')[0]
hint(child)
check('child awaiting_approval: dot on its root chat', await seen(badge(page, titles[0], 'needs approval')))
await r.shot('child-needs-approval')
psql(`UPDATE sessions SET status = 'completed' WHERE id = '${child}'`)
hint(child)
check('child completed: dot clears', await gone(badge(page, titles[0], 'needs approval'), 15_000))

// Failed: the badge only opens the chat.
psql(`UPDATE sessions SET status = 'failed' WHERE id = '${b}'`)
hint(b)
check('failed: badge on the row', await seen(badge(page, titles[1], 'failed')))
await r.shot('failed')
await row(page, titles[1]).getByRole('link').click()
await page.waitForURL(new RegExp(`/s/${b}$`))
check('failed: clicking the row opens the chat, no Retry in the list', (await nav(page).getByRole('button', { name: /retry/i }).count()) === 0)
psql(`UPDATE sessions SET status = 'awaiting_user' WHERE id = '${b}'`)
hint(b)
await gone(badge(page, titles[1], 'failed'), 15_000)

// Hide/show with chat A open: every EventSource closes, then reopens.
await row(page, titles[0]).getByRole('link').click()
await page.waitForURL(new RegExp(`/s/${a}$`))
await page.getByText(/^echo: alpha/).first().waitFor({ timeout: 30_000 })
await sleep(500)
const lastSeq = psql(`SELECT last_seq FROM sessions WHERE id = '${a}'`)
const before = (await streams(page)).length
await setVisibility(page, 'hidden')
await sleep(300)
const hidden = await streams(page)
check('hidden: every EventSource closed', hidden.every((s) => s.readyState === 2), `${hidden.length} streams`)
// A change while hidden must arrive through the fresh snapshot, not a frame.
psql(`UPDATE sessions SET status = 'failed' WHERE id = '${b}'`)
await setVisibility(page, 'visible')
await sleep(1000)
const reopened = (await streams(page)).slice(before)
const sess = reopened.find((s) => s.url.includes(`/api/sessions/${a}/events`))
check('visible: Session stream reopens with ?after=lastSeq', sess?.url.endsWith(`?after=${lastSeq}`) ?? false, sess?.url)
check('visible: Activity Stream reopens', reopened.some((s) => s.url.endsWith('/api/activity') && s.readyState === 1), reopened.map((s) => s.url).join(' '))
check('visible: fresh snapshot shows the change made while hidden', await seen(badge(page, titles[1], 'failed')))
await r.shot('after-show')
psql(`UPDATE sessions SET status = 'awaiting_user' WHERE id = '${b}'`)
hint(b)

// Stream cap: the page holds 2 streams (Session + Activity); open Activity
// Streams with the same cookie until one is refused.
const cookie = (await context.cookies(BASE)).map((c) => `${c.name}=${c.value}`).join('; ')
const metrics = async () =>
  fetch('http://localhost:9090/metrics').then((r) => r.text()).then(
    (t) => t.split('\n').filter((l) => /^jf_stream/.test(l)).join('\n'),
    () => null,
  )
const held = []
let refusedAt = null
for (let i = 0; i < 25 && refusedAt === null; i++) {
  const ctl = new AbortController()
  const res = await fetch(`${BASE}/api/activity`, { headers: { cookie, origin: BASE }, signal: ctl.signal })
  if (res.status === 429) refusedAt = i
  else held.push(ctl)
}
const open_ = (await streams(page)).filter((s) => s.readyState === 1).length
check('cap: the 21st stream (Session + Activity) gets 429', refusedAt !== null && open_ + held.length === 20, `page=${open_} extra=${held.length} refused at extra #${refusedAt + 1}`)
const m = await metrics()
if (m) {
  console.log(m)
  check('metrics: jf_streams_open{kind="activity"} counts Activity Streams', new RegExp(`jf_streams_open\\{kind="activity"\\} ${held.length + 1}\\b`).test(m))
  writeFileSync(join(r.out, 'metrics-at-cap.txt'), m + '\n')
} else console.log('SKIP metrics: no :9090 forward')
for (const c of held) c.abort()

writeFileSync(join(r.out, 'test-sessions.txt'), [...ids, child].join('\n') + '\n')
console.log('chats:', ids.join(' '), 'child:', child)
console.log('evidence:', await r.close())
process.exit(results.every((x) => x.ok) ? 0 : 1)
