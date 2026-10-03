import type { ChatSummary } from './lib/api'

type Props = {
  chats: ChatSummary[]
  activeId: string | null
  onNewChat: () => void
  onOpen: (id: string) => void
}

// ChatList is the User's chats, newest activity first, with "New chat" on top.
export function ChatList({ chats, activeId, onNewChat, onOpen }: Props) {
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
                  className={`block truncate rounded-lg px-3 py-2 text-sm ${
                    c.id === activeId
                      ? 'bg-neutral-800 text-neutral-100'
                      : 'text-neutral-300 hover:bg-neutral-900'
                  }`}
                >
                  {c.title}
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
