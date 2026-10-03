#!/usr/bin/env node
// Signs the Admin in once (or reuses the saved state) and prints /api/me.
// Usage: node signin.mjs
import { chromium } from 'playwright'
import { BASE, ensureSignedIn } from './lib.mjs'

const browser = await chromium.launch()
const state = await ensureSignedIn(browser)
const ctx = await browser.newContext({ storageState: state })
const res = await ctx.request.get(`${BASE}/api/me`)
console.log(res.status(), await res.text())
console.log('state:', state)
await browser.close()
