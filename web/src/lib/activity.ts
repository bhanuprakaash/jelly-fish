import type { ChatSummary } from './api'

// ActivityEntry is a session's status as the Activity Stream reports it
// (docs/design/streaming.md §4.2). root_id is the chat the session belongs to.
export type ActivityEntry = {
  session_id: string
  root_id: string
  parent_id: string | null
  status: string
  needs_approval: boolean
}

// ActivityMap holds the busy sessions by session_id.
export type ActivityMap = Record<string, ActivityEntry>

export type Badge = 'approval' | 'failed' | 'busy'

const BUSY = new Set(['runnable', 'running', 'sleeping', 'awaiting_children'])

// applySnapshot replaces the map: a snapshot is the whole truth.
export function applySnapshot(entries: ActivityEntry[]): ActivityMap {
  return Object.fromEntries(entries.map((e) => [e.session_id, e]))
}

// applyStatus upserts e, or drops it once the session no longer needs a badge
// (awaiting_user, completed: docs/design/streaming.md D18).
export function applyStatus(map: ActivityMap, e: ActivityEntry): ActivityMap {
  const next = { ...map }
  if (e.status === 'awaiting_user' || e.status === 'completed') delete next[e.session_id]
  else next[e.session_id] = e
  return next
}

// badgeFor is the badge for a chat row: an approval wanted by the chat or any
// of its children beats a failed chat, which beats a busy one. A failing child
// does not badge its root.
export function badgeFor(chatId: string, map: ActivityMap): Badge | null {
  const entries = Object.values(map)
  if (entries.some((e) => e.root_id === chatId && e.status === 'awaiting_approval')) return 'approval'
  const own = map[chatId]
  if (own?.status === 'failed') return 'failed'
  if (own && BUSY.has(own.status)) return 'busy'
  return null
}

// needsListRefetch reports whether e is about a chat the list doesn't have, or
// shows only a placeholder Title, so the list should reload (D19). It is false
// until the first load: that load covers everything.
export function needsListRefetch(chats: ChatSummary[] | null, e: ActivityEntry): boolean {
  if (!chats) return false
  return !chats.find((c) => c.id === e.root_id)?.titled
}
