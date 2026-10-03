import type { Badge } from './lib/activity'
import type { ChatSummary } from './lib/api'

type Props = {
  chats: ChatSummary[]
  // badges is each chat's live badge by id; a chat with none has no entry.
  badges: Record<string, Badge>
  activeId: string | null
  onNewChat: () => void
  onOpen: (id: string) => void
}

// ChatList is the User's chats, newest activity first, with "New chat" on top.
export function ChatList({ chats, badges, activeId, onNewChat, onOpen }: Props) {
  return (
    <div className="flex h-full flex-col">
      <div className="p-4">
        <button
          onClick={onNewChat}
          className="w-full rounded-xl bg-neutral-100 px-4 py-2 font-medium text-neutral-900"
        >
          New chat
        </button>
      </div>

      <nav aria-label="Chats" className="flex-1 overflow-y-auto px-2">
        {chats.length === 0 ? (
          <p className="px-2 py-4 text-center text-sm text-neutral-500">No chats yet</p>
        ) : (
          <ul>
            {chats.map((c) => (
              <li key={c.id}>
                <a
                  href={`/s/${c.id}`}
                  aria-current={c.id === activeId ? 'page' : undefined}
                  onClick={(e) => {
                    e.preventDefault()
                    onOpen(c.id)
                  }}
                  className={`flex items-center gap-2 rounded-lg px-3 py-2 text-sm ${
                    c.id === activeId
                      ? 'bg-neutral-800 text-neutral-100'
                      : 'text-neutral-300 hover:bg-neutral-900'
                  }`}
                >
                  <span className="min-w-0 flex-1 truncate">{c.title}</span>
                  <ActivityBadge badge={badges[c.id]} />
                </a>
              </li>
            ))}
          </ul>
        )}
      </nav>

      <div className="border-t border-neutral-800 p-4">
        <a href="/settings" className="text-sm text-neutral-400 hover:text-neutral-200">
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
          className="size-3.5 shrink-0 rounded-full border-2 border-neutral-600 border-t-neutral-200 motion-safe:animate-spin"
        />
      )
    case 'approval':
      return (
        <span
          role="img"
          aria-label="needs approval"
          className="size-2.5 shrink-0 rounded-full bg-amber-400"
        />
      )
    case 'failed':
      return (
        <span role="img" aria-label="failed" className="shrink-0 text-red-400">
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
