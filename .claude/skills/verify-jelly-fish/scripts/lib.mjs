// Shared Playwright harness for verify-jelly-fish drives. Every drive hits the
// cluster api through its :8080 forward; nothing here starts a local server.
import { chromium, devices } from 'playwright'
import { existsSync, mkdirSync, readdirSync, renameSync, rmdirSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'

export const BASE = process.env.JF_URL ?? 'http://localhost:8080'
const MAILPIT = process.env.JF_MAILPIT ?? 'http://localhost:8025'
// Outside the repo: holds the Login Session cookie.
export const AUTH_STATE = join(tmpdir(), 'jf-verify-auth.json')
export const EVIDENCE_ROOT = process.env.JF_EVIDENCE ?? join(homedir(), 'Downloads', 'jelly-fish-verify')

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// adminEmail reads JF_ADMIN_EMAIL from deploy/k8s/.env.app.
export function adminEmail() {
  if (process.env.JF_EMAIL) return process.env.JF_EMAIL
  const root = execFileSync('git', ['rev-parse', '--show-toplevel'], { encoding: 'utf8' }).trim()
  const env = execFileSync('grep', ['^JF_ADMIN_EMAIL=', join(root, 'deploy/k8s/.env.app')], {
    encoding: 'utf8',
  })
  return env.trim().split('=')[1]
}

async function newestCode(email, since) {
  for (let i = 0; i < 30; i++) {
    const res = await fetch(`${MAILPIT}/api/v1/messages`)
    const { messages } = await res.json()
    const m = messages.find(
      (m) =>
        m.To.some((t) => t.Address === email) &&
        m.Subject.startsWith('Your jelly-fish code:') &&
        new Date(m.Created).getTime() >= since - 2000,
    )
    if (m) return m.Subject.split(':')[1].trim()
    await sleep(500)
  }
  throw new Error(`no login code for ${email} in Mailpit`)
}

// ensureSignedIn reuses AUTH_STATE while /api/me accepts it; otherwise it
// spends one login code (3 per email per 15 min) and saves a fresh state.
export async function ensureSignedIn(browser, email = adminEmail()) {
  if (existsSync(AUTH_STATE)) {
    const ctx = await browser.newContext({ storageState: AUTH_STATE })
    const res = await ctx.request.get(`${BASE}/api/me`)
    await ctx.close()
    if (res.status() === 200) return AUTH_STATE
  }
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  await page.goto(BASE)
  const since = Date.now()
  const status = await page.evaluate(
    (email) =>
      fetch('/api/auth/code', { method: 'POST', body: JSON.stringify({ email }) }).then((r) => r.status),
    email,
  )
  if (status !== 202) throw new Error(`POST /api/auth/code → ${status}`)
  const code = await newestCode(email, since)
  const verified = await page.evaluate(
    ([email, code]) =>
      fetch('/api/auth/code/verify', { method: 'POST', body: JSON.stringify({ email, code }) }).then(
        (r) => r.status,
      ),
    [email, code],
  )
  if (verified !== 204) throw new Error(`POST /api/auth/code/verify → ${verified}`)
  await ctx.storageState({ path: AUTH_STATE })
  await ctx.close()
  return AUTH_STATE
}

// trackEventSources wraps window.EventSource so a drive can read every stream
// the page opened: url, and whether it is still open.
function trackEventSources() {
  const Real = window.EventSource
  window.__jfES = []
  window.EventSource = class extends Real {
    constructor(url, init) {
      super(url, init)
      window.__jfES.push(this)
    }
  }
}

// open starts a recorded, traced, signed-in browser for one flow. device is
// 'desktop' (1280x800 Chrome) or 'pixel7' (Playwright's Pixel 7 emulation:
// desktop Chromium with a phone viewport, UA, touch and isMobile).
// Evidence lands in EVIDENCE_ROOT/<dir>/: <name>.webm, <name>-trace.zip, PNGs.
// contextOptions are passed to browser.newContext (e.g. serviceWorkers: 'block').
export async function open({ dir, name, device = 'desktop', signedIn = true, contextOptions = {} }) {
  const out = join(EVIDENCE_ROOT, dir)
  mkdirSync(out, { recursive: true })
  const browser = await chromium.launch({ headless: !process.env.HEADED })
  const storageState = signedIn ? await ensureSignedIn(browser) : undefined
  const profile =
    device === 'pixel7' ? devices['Pixel 7'] : { viewport: { width: 1280, height: 800 } }
  const videoDir = join(out, `.video-${name}`)
  const context = await browser.newContext({
    ...profile,
    storageState,
    recordVideo: { dir: videoDir, size: profile.viewport },
    ...contextOptions,
  })
  await context.addInitScript(trackEventSources)
  await context.tracing.start({ screenshots: true, snapshots: true, sources: false })
  const page = await context.newPage()
  let n = 0
  return {
    browser,
    context,
    page,
    out,
    // shot saves a numbered full-page screenshot and returns its path.
    async shot(label, p = page) {
      const path = join(out, `${name}-${String(++n).padStart(2, '0')}-${label}.png`)
      await p.screenshot({ path, fullPage: true })
      return path
    },
    async close() {
      await context.tracing.stop({ path: join(out, `${name}-trace.zip`) })
      await context.close()
      for (const [i, f] of readdirSync(videoDir).entries()) {
        renameSync(join(videoDir, f), join(out, `${name}${i ? `-${i}` : ''}.webm`))
      }
      rmdirSync(videoDir)
      await browser.close()
      return out
    },
  }
}

// streams lists the page's EventSources: url and readyState (0 connecting,
// 1 open, 2 closed).
export const streams = (page) =>
  page.evaluate(() => window.__jfES.map((es) => ({ url: es.url, readyState: es.readyState })))

// setVisibility fires visibilitychange with document.visibilityState forced
// to 'hidden' or 'visible', the same event a backgrounded tab or PWA gets.
export const setVisibility = (page, state) =>
  page.evaluate((state) => {
    Object.defineProperty(document, 'visibilityState', { value: state, configurable: true })
    Object.defineProperty(document, 'hidden', { value: state === 'hidden', configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
  }, state)

// psql runs one query against the cluster postgres via DATABASE_URL (from
// jellyfish_db) and returns its unaligned rows.
export function psql(sql) {
  if (!process.env.DATABASE_URL) throw new Error('DATABASE_URL unset')
  return execFileSync('psql', [process.env.DATABASE_URL, '-Atc', sql], { encoding: 'utf8' }).trim()
}

export { sleep }

// checker collects PASS/FAIL lines; exit() ends the drive non-zero on any FAIL.
export function checker(prefix = '') {
  const results = []
  return {
    check(name, ok, detail = '') {
      results.push(ok)
      console.log(`${ok ? 'PASS' : 'FAIL'} ${prefix}${name}${detail ? ` — ${detail}` : ''}`)
    },
    exit: () => process.exit(results.every(Boolean) ? 0 : 1),
  }
}

// Chat List handles (features/chat-list.md, activity-stream.md).
export const nav = (page) => page.getByRole('navigation', { name: 'Chats' })
export const row = (page, text) => nav(page).getByRole('listitem').filter({ hasText: text })
export const badge = (page, text, label) => row(page, text).getByRole('img', { name: label })
export const header = (page) => page.getByRole('heading', { level: 1 })

// seen and gone report whether loc appears / detaches within timeout.
export const seen = (loc, timeout = 15_000) => loc.waitFor({ timeout }).then(() => true, () => false)
export const gone = (loc, timeout = 60_000) =>
  loc.waitFor({ state: 'detached', timeout }).then(() => true, () => false)

// until polls fn until it returns true or timeout passes.
export async function until(fn, timeout = 20_000) {
  for (const end = Date.now() + timeout; Date.now() < end; await sleep(250)) if (await fn()) return true
  return false
}

// fakeTitle is the Fake Provider's automatic Title for a first message:
// whitespace collapsed, cut to 60, trimmed (event-log.md D45).
export const fakeTitle = (first) => first.split(/\s+/).join(' ').slice(0, 60).trim()
