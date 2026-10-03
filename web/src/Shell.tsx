import { Chat } from './Chat'
import { ChatList } from './ChatList'
import { useChatList } from './lib/useChatList'

type Props = {
  // sessionId is the open chat, or null at "/".
  sessionId: string | null
  isNew: boolean
  navigate: (to: string) => void
  onNewChat: () => void
  onUnauthorized: () => void
}

// Shell is the signed-in home: the Chat List beside the open chat. On a phone
// only one of the two shows, the list at "/" and the chat at "/s/{id}".
export function Shell({ sessionId, isNew, navigate, onNewChat, onUnauthorized }: Props) {
  const { chats, error, refetch, badges } = useChatList(onUnauthorized)

  return (
    <div className="flex min-h-svh bg-neutral-950 text-neutral-100">
      <aside
        className={`${sessionId ? 'hidden' : 'flex'} w-full flex-col border-r border-neutral-800 md:sticky md:top-0 md:flex md:h-svh md:w-72 md:shrink-0`}
      >
        {chats ? (
          <ChatList
            chats={chats}
            badges={badges}
            activeId={sessionId}
            onNewChat={onNewChat}
            onOpen={(id) => navigate(`/s/${id}`)}
            onChanged={refetch}
            onUnauthorized={onUnauthorized}
          />
        ) : (
          <p className={`p-4 text-center text-sm ${error ? 'text-red-400' : 'text-neutral-500'}`}>
            {error ? 'Could not load chats.' : 'Loading…'}
          </p>
        )}
      </aside>

      <div className={`${sessionId ? 'flex' : 'hidden md:flex'} min-w-0 flex-1 flex-col`}>
        {sessionId ? (
          <Chat
            key={sessionId}
            sessionId={sessionId}
            isNew={isNew}
            navigate={navigate}
            listTitle={chats?.find((c) => c.id === sessionId)?.title}
            onTitleChanged={refetch}
            onCreated={refetch}
            onUnauthorized={onUnauthorized}
          />
        ) : (
          <p className="m-auto text-neutral-500">Select a chat or start a new one</p>
        )}
      </div>
    </div>
  )
}
