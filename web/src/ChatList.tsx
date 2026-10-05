import { useState } from 'react'
import type { Badge } from './lib/activity'
import { renameSession, type ChatSummary } from './lib/api'
import { RenameForm, RowMenu } from './RowMenu'

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

  return (
    <div className="flex h-full flex-col">
      <div className="p-4">
        <button
          onClick={onNewChat}
          className="btn btn-jelly w-full"
        >
          New chat
        </button>
      </div>

      <nav aria-label="Chats" className="flex-1 overflow-y-auto px-2">
        {chats.length === 0 ? (
          <p className="px-2 py-4 text-center text-sm text-muted">No chats yet</p>
        ) : (
          <ul>
            {chats.map((c) => (
              <li
                key={c.id}
                className={`flex items-center rounded-lg ${
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
                    className="flex min-w-0 flex-1 items-center gap-2 px-3 py-2 text-sm"
                  >
                    <span className="min-w-0 flex-1 truncate">{c.title}</span>
                    <ActivityBadge badge={badges[c.id]} />
                  </a>
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

// ActivityBadge marks a chat that is busy, needs approval, or failed; opening
// the chat is the only action.
function ActivityBadge({ badge }: { badge: Badge | undefined }) {
  switch (badge) {
    case 'busy':
      return (
        <span
          role="img"
          aria-label="running"
          className="size-3.5 shrink-0 rounded-full border-2 border-line2 border-t-accent-ink motion-safe:animate-spin"
        />
      )
    case 'approval':
      return (
        <span
          role="img"
          aria-label="needs approval"
          className="size-2.5 shrink-0 rounded-full bg-attention"
        />
      )
    case 'failed':
      return (
        <span role="img" aria-label="failed" className="shrink-0 text-danger">
          <svg aria-hidden="true" viewBox="0 0 12 12" className="size-3">
            <path
              d="M2 2l8 8M10 2l-8 8"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              fill="none"
            />
          </svg>
        </span>
      )
    default:
      return null
  }
}
