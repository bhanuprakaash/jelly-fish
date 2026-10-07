import { useState } from 'react'
import type { Badge } from './lib/activity'
import { JellyGlyph } from './JellyGlyph'
import { renameSession, retrySession, type ChatSummary } from './lib/api'
import { statusLabel, statusTone, type JellyStatus } from './lib/status'
import { RenameForm, RowMenu } from './RowMenu'

const badgeStatus: Record<Badge, JellyStatus> = {
  approval: 'awaiting_approval',
  failed: 'failed',
  busy: 'running',
  sleeping: 'sleeping',
}

// rowStatus is the live badge's status. The Activity Stream lists every
// session that is not awaiting_user or completed, so once it is ready a chat
// with no badge is done or waiting on the User. Until then the stored status
// is the best guess.
function rowStatus(c: ChatSummary, badge: Badge | undefined, ready: boolean): JellyStatus {
  if (badge) return badgeStatus[badge]
  if (!ready) {
    return c.status in statusLabel ? (c.status as JellyStatus) : 'awaiting_user'
  }
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
  // chats is null until the first load succeeds.
  chats: ChatSummary[] | null
  // error is true when the last load failed.
  error: boolean
  // badges is each chat's live badge by id; a chat with none has no entry.
  badges: Record<string, Badge>
  // ready is true once the Activity Stream has sent its first snapshot.
  ready: boolean
  // email is the signed-in User's, shown in the footer.
  email: string
  activeId: string | null
  onNewChat: () => void
  onOpen: (id: string) => void
  // onChanged is called after a rename or a retry, or to retry a failed load,
  // so the list loads again.
  onChanged: () => void
}

// ChatList is the User's chats, newest activity first, with "New chat" on top.
export function ChatList({ chats, error, badges, ready, email, activeId, onNewChat, onOpen, onChanged }: Props) {
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [retryingId, setRetryingId] = useState<string | null>(null)
  const [retryFailedId, setRetryFailedId] = useState<string | null>(null)

  const retry = async (id: string) => {
    setRetryingId(id)
    setRetryFailedId(null)
    try {
      await retrySession(id)
      onChanged()
    } catch {
      setRetryFailedId(id)
    } finally {
      setRetryingId(null)
    }
  }

  const rows = (chats ?? []).map((c) => ({ chat: c, status: rowStatus(c, badges[c.id], ready) }))
  const todayKey = new Date().toDateString()
  const groups = [
    { label: 'Today', rows: rows.filter((r) => new Date(r.chat.updated_at).toDateString() === todayKey) },
    { label: 'Earlier', rows: rows.filter((r) => new Date(r.chat.updated_at).toDateString() !== todayKey) },
  ].filter((g) => g.rows.length > 0)
  const working = rows.filter((r) => ['runnable', 'running'].includes(r.status)).length
  const needsYou = rows.filter((r) => r.status === 'awaiting_approval').length

  return (
    <div className="flex h-full flex-col">
      <h2 className="flex items-center gap-2.5 px-5 pt-6 pb-1 text-3xl font-semibold tracking-tight md:px-6 md:pt-5 md:text-xl">
        <span className="hidden md:inline-flex">
          <JellyGlyph status="awaiting_user" />
        </span>
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
        {!chats ? (
          <div className="px-2 py-4 text-center text-sm">
            <p className={error ? 'text-danger' : 'text-muted'}>{error ? 'Could not load chats.' : 'Loading…'}</p>
            {error && (
              <button type="button" onClick={onChanged} className="btn btn-ghost btn-sm mt-2">
                Retry
              </button>
            )}
          </div>
        ) : chats.length === 0 ? (
          <p className="px-2 py-4 text-center text-sm text-muted">No chats yet</p>
        ) : (
          groups.map((g) => (
            <section key={g.label}>
              <h3 className="px-2.5 pt-3 pb-1.5 text-[11px] font-medium tracking-[0.14em] text-muted uppercase">
                {g.label}
              </h3>
              <ul>
                {g.rows.map(({ chat: c, status }) => (
                  <li
                    key={c.id}
                    className={`relative flex min-h-16 items-center rounded-[14px] md:min-h-11 md:rounded-btn has-[a:focus-visible]:shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent-ink)] ${
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
                      />
                    ) : (
                      <div className="flex min-w-0 flex-1 items-center gap-3 px-2.5 py-2 md:gap-2.5 md:py-1">
                        <JellyGlyph
                          status={status}
                          className="size-9 md:size-[26px] [&>svg]:size-[22px] md:[&>svg]:size-[18px]"
                        />
                        <span className="flex min-w-0 flex-1 flex-col">
                          <a
                            href={`/s/${c.id}`}
                            aria-current={c.id === activeId ? 'page' : undefined}
                            onClick={(e) => {
                              e.preventDefault()
                              onOpen(c.id)
                            }}
                            className="truncate text-[15px] font-medium text-ink outline-none after:absolute after:inset-0 md:text-sm md:font-normal"
                          >
                            {c.title}
                          </a>
                          <span className={`text-[13px] md:text-xs ${statusTone[status]}`}>
                            {statusLabel[status]}
                            {status === 'failed' && (
                              <>
                                {' · '}
                                <button
                                  type="button"
                                  disabled={retryingId === c.id}
                                  onClick={() => retry(c.id)}
                                  aria-label={`Retry ${c.title}`}
                                  className="relative rounded-btn font-medium underline disabled:opacity-60 focus-ring"
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
            </section>
          ))
        )}
      </nav>

      <div className="flex items-center gap-2.5 border-t border-line px-4 py-3">
        {email && (
          <>
            <span
              aria-hidden="true"
              className="inline-flex size-[30px] shrink-0 items-center justify-center rounded-full bg-line2 font-semibold text-ink"
            >
              {email[0].toUpperCase()}
            </span>
            <span className="min-w-0 flex-1 truncate text-[13px] text-muted">{email}</span>
          </>
        )}
        <span className="ml-auto flex shrink-0">
          <a
            href="/memories"
            aria-label="Memories"
            className="inline-flex size-10 items-center justify-center rounded-full text-muted hover:text-ink focus-ring"
          >
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M6 3h12v18l-6-4-6 4z" />
            </svg>
          </a>
          <a
            href="/settings"
            aria-label="Settings"
            className="inline-flex size-10 items-center justify-center rounded-full text-muted hover:text-ink focus-ring"
          >
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
              <circle cx="12" cy="12" r="3" />
              <path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" />
            </svg>
          </a>
        </span>
      </div>
    </div>
  )
}
