import { useState } from 'react'
import type { Badge } from './lib/activity'
import { JellyGlyph, type JellyStatus } from './JellyGlyph'
import { renameSession, retrySession, UnauthorizedError, type ChatSummary } from './lib/api'
import { RenameForm, RowMenu } from './RowMenu'

const badgeStatus: Record<Badge, JellyStatus> = {
  approval: 'awaiting_approval',
  failed: 'failed',
  busy: 'running',
  sleeping: 'sleeping',
  waiting: 'awaiting_children',
}

const statusLabel: Record<JellyStatus, string> = {
  runnable: 'Working…',
  running: 'Working…',
  awaiting_approval: 'Needs approval',
  awaiting_children: 'Waiting…',
  sleeping: 'Sleeping',
  awaiting_user: 'Your turn',
  completed: 'Done',
  failed: 'Failed',
}

const statusTone: Partial<Record<JellyStatus, string>> = {
  awaiting_approval: 'text-attention',
  failed: 'text-danger',
}

// rowStatus is the live badge's status, else the chat's own: the list loads
// less often than the Activity Stream, so a stored busy status is stale.
function rowStatus(c: ChatSummary, badge: Badge | undefined): JellyStatus {
  if (badge) return badgeStatus[badge]
  return c.status === 'completed' || c.status === 'failed' ? c.status : 'awaiting_user'
}

function when(iso: string): string {
  const at = new Date(iso)
  const mins = Math.floor((Date.now() - at.getTime()) / 60_000)
  if (mins < 1) return 'now'
  if (mins < 60) return `${mins}m`
  if (mins < 24 * 60) return `${Math.floor(mins / 60)}h`
  if (mins < 7 * 24 * 60) return at.toLocaleDateString([], { weekday: 'short' })
  return at.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

type Props = {
  chats: ChatSummary[]
  // badges is each chat's live badge by id; a chat with none has no entry.
  badges: Record<string, Badge>
  activeId: string | null
  onNewChat: () => void
  onOpen: (id: string) => void
  // onChanged is called after a rename, so the list loads again.
  onChanged: () => void
  onUnauthorized: () => void
}

// ChatList is the User's chats, newest activity first, with "New chat" on top.
export function ChatList({ chats, badges, activeId, onNewChat, onOpen, onChanged, onUnauthorized }: Props) {
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [retryingId, setRetryingId] = useState<string | null>(null)

  const retry = async (id: string) => {
    setRetryingId(id)
    try {
      await retrySession(id)
    } catch (err) {
      if (err instanceof UnauthorizedError) onUnauthorized()
    } finally {
      setRetryingId(null)
    }
  }

  const rows = chats.map((c) => ({ chat: c, status: rowStatus(c, badges[c.id]) }))
  const working = rows.filter((r) => ['runnable', 'running', 'awaiting_children'].includes(r.status)).length
  const needsYou = rows.filter((r) => r.status === 'awaiting_approval').length

  return (
    <div className="flex h-full flex-col">
      <h2 className="px-5 pt-6 pb-1 text-3xl font-semibold tracking-tight md:px-6 md:pt-5 md:text-xl">
        <span className="md:hidden">Your swarm</span>
        <span className="hidden md:inline">jelly-fish</span>
      </h2>

      <div className="px-4 pt-3 pb-2">
        <button onClick={onNewChat} className="btn btn-jelly w-full">
          New chat
        </button>
      </div>

      {(working > 0 || needsYou > 0) && (
        <p className="mx-4 mb-2 flex items-center gap-2.5 rounded-card border border-accent-line bg-accent-tint px-3.5 py-3 text-sm text-accent-ink">
          {working > 0 && (
            <>
              <span aria-hidden="true" className="jelly-run size-2 rounded-full bg-accent-ink" />
              <span>{working} working</span>
            </>
          )}
          {working > 0 && needsYou > 0 && <span aria-hidden="true">·</span>}
          {needsYou > 0 && <strong className="font-medium text-attention">{needsYou} needs you</strong>}
        </p>
      )}

      <nav aria-label="Chats" className="flex-1 overflow-y-auto px-2">
        {chats.length === 0 ? (
          <p className="px-2 py-4 text-center text-sm text-muted">No chats yet</p>
        ) : (
          <ul>
            {rows.map(({ chat: c, status }) => (
              <li
                key={c.id}
                className={`flex items-center rounded-btn ${
                  c.id === activeId ? 'bg-accent-tint text-ink' : 'text-ink2 hover:bg-sunk'
                }`}
              >
                {renamingId === c.id ? (
                  <RenameForm
                    initial={c.title}
                    onSave={async (title) => {
                      await renameSession(c.id, title)
                      setRenamingId(null)
                      onChanged()
                    }}
                    onCancel={() => setRenamingId(null)}
                    onUnauthorized={onUnauthorized}
                  />
                ) : (
                  <a
                    href={`/s/${c.id}`}
                    aria-current={c.id === activeId ? 'page' : undefined}
                    onClick={(e) => {
                      e.preventDefault()
                      onOpen(c.id)
                    }}
                    className="flex min-w-0 flex-1 items-center gap-3 px-2.5 py-2"
                  >
                    <JellyGlyph status={status} />
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate text-sm font-medium text-ink">{c.title}</span>
                      <span className={`text-xs ${statusTone[status] ?? 'text-muted'}`}>{statusLabel[status]}</span>
                    </span>
                    <time dateTime={c.updated_at} className="shrink-0 text-xs text-muted">
                      {when(c.updated_at)}
                    </time>
                  </a>
                )}
                {status === 'failed' && renamingId !== c.id && (
                  <button
                    type="button"
                    disabled={retryingId === c.id}
                    onClick={() => retry(c.id)}
                    aria-label={`Retry ${c.title}`}
                    className="btn btn-ghost btn-sm shrink-0 text-danger"
                  >
                    Retry?
                  </button>
                )}
                <RowMenu renaming={renamingId === c.id} onRename={() => setRenamingId(c.id)} />
              </li>
            ))}
          </ul>
        )}
      </nav>

      <div className="flex gap-4 border-t border-line p-4">
        <a href="/memories" className="text-sm text-muted hover:text-ink">
          Memories
        </a>
        <a href="/settings" className="text-sm text-muted hover:text-ink">
          Settings
        </a>
      </div>
    </div>
  )
}
