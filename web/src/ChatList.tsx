import { useState } from 'react'
import type { Badge } from './lib/activity'
import { JellyGlyph } from './JellyGlyph'
import { renameSession, retrySession, UnauthorizedError, type ChatSummary } from './lib/api'
import { statusLabel, statusTone, type JellyStatus } from './lib/status'
import { RenameForm, RowMenu } from './RowMenu'

const badgeStatus: Record<Badge, JellyStatus> = {
  approval: 'awaiting_approval',
  failed: 'failed',
  busy: 'running',
  sleeping: 'sleeping',
}

// rowStatus is the live badge's status. The Activity Stream lists every
// session that is not awaiting_user or completed, so a chat with no badge is
// done or waiting on the User; the list's stored status is stale for anything else.
function rowStatus(c: ChatSummary, badge: Badge | undefined): JellyStatus {
  if (badge) return badgeStatus[badge]
  return c.status === 'completed' ? 'completed' : 'awaiting_user'
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
  // onChanged is called after a rename or a retry, so the list loads again.
  onChanged: () => void
  onUnauthorized: () => void
}

// ChatList is the User's chats, newest activity first, with "New chat" on top.
export function ChatList({ chats, badges, activeId, onNewChat, onOpen, onChanged, onUnauthorized }: Props) {
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [retryingId, setRetryingId] = useState<string | null>(null)
  const [retryFailedId, setRetryFailedId] = useState<string | null>(null)

  const retry = async (id: string) => {
    setRetryingId(id)
    setRetryFailedId(null)
    try {
      await retrySession(id)
      onChanged()
    } catch (err) {
      if (err instanceof UnauthorizedError) onUnauthorized()
      else setRetryFailedId(id)
    } finally {
      setRetryingId(null)
    }
  }

  const rows = chats.map((c) => ({ chat: c, status: rowStatus(c, badges[c.id]) }))
  const working = rows.filter((r) => ['runnable', 'running'].includes(r.status)).length
  const needsYou = rows.filter((r) => r.status === 'awaiting_approval').length

  return (
    <div className="flex h-full flex-col">
      <h2 className="px-5 pt-6 pb-1 text-3xl font-semibold tracking-tight md:px-6 md:pt-5 md:text-xl">
        <span className="md:hidden">Chats</span>
        <span className="hidden md:inline">jelly-fish</span>
      </h2>

      <div className="order-last px-4 pt-2 pb-4 md:order-none md:pt-3 md:pb-2">
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

      <nav aria-label="Chats" className="min-h-0 flex-1 overflow-y-auto px-2">
        {chats.length === 0 ? (
          <p className="px-2 py-4 text-center text-sm text-muted">No chats yet</p>
        ) : (
          <ul>
            {rows.map(({ chat: c, status }) => (
              <li
                key={c.id}
                className={`relative flex items-center rounded-btn has-[a:focus-visible]:shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent-ink)] ${
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
                  <div className="flex min-w-0 flex-1 items-center gap-3 px-2.5 py-2">
                    <JellyGlyph status={status} />
                    <span className="flex min-w-0 flex-1 flex-col">
                      <a
                        href={`/s/${c.id}`}
                        aria-current={c.id === activeId ? 'page' : undefined}
                        onClick={(e) => {
                          e.preventDefault()
                          onOpen(c.id)
                        }}
                        className="truncate text-sm font-medium text-ink outline-none after:absolute after:inset-0"
                      >
                        {c.title}
                      </a>
                      <span className={`text-xs ${statusTone[status]}`}>
                        {statusLabel[status]}
                        {status === 'failed' && (
                          <>
                            {' · '}
                            <button
                              type="button"
                              disabled={retryingId === c.id}
                              onClick={() => retry(c.id)}
                              aria-label={`Retry ${c.title}`}
                              className="relative rounded-btn font-medium underline focus-visible:shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent-ink)] focus-visible:outline-none disabled:opacity-60"
                            >
                              Retry?
                            </button>
                          </>
                        )}
                      </span>
                      {retryFailedId === c.id && (
                        <span role="alert" className="text-xs text-danger">
                          Could not retry
                        </span>
                      )}
                    </span>
                    <time dateTime={c.updated_at} className="shrink-0 text-xs text-muted">
                      {when(c.updated_at)}
                    </time>
                  </div>
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
