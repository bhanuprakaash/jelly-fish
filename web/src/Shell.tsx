import { Chat } from './Chat'
import { ChatList } from './ChatList'
import { useChatList } from './lib/useChatList'

type Props = {
  // sessionId is the open chat, or null at "/".
  sessionId: string | null
  isNew: boolean
  // email is the signed-in User's, shown in the list footer.
  email: string
  navigate: (to: string) => void
  onNewChat: () => void
  onUnauthorized: () => void
}

// Shell is the signed-in home: the Chat List beside the open chat. On a phone
// only one of the two shows, the list at "/" and the chat at "/s/{id}".
export function Shell({ sessionId, isNew, email, navigate, onNewChat, onUnauthorized }: Props) {
  const { chats, error, refetch, badges, ready } = useChatList(onUnauthorized)

  return (
    <div className="flex min-h-svh bg-bg text-ink">
      <aside
        className={`${sessionId ? 'hidden' : 'flex'} h-svh w-full flex-col border-line md:border-r bg-surface md:sticky md:top-0 md:flex md:w-72 md:shrink-0`}
      >
        <ChatList
          chats={chats}
          error={error}
          badges={badges}
          ready={ready}
          email={email}
          activeId={sessionId}
          onNewChat={onNewChat}
          onOpen={(id) => navigate(`/s/${id}`)}
          onChanged={refetch}
          onUnauthorized={onUnauthorized}
        />
      </aside>

      <div className={`${sessionId ? 'flex' : 'hidden md:flex'} min-w-0 flex-1 flex-col`}>
        {sessionId ? (
          <Chat
            key={sessionId}
            sessionId={sessionId}
            isNew={isNew}
            navigate={navigate}
            listTitle={chats?.find((c) => c.id === sessionId)?.title}
            listIncognito={chats?.find((c) => c.id === sessionId)?.incognito}
            onTitleChanged={refetch}
            onCreated={refetch}
            onUnauthorized={onUnauthorized}
          />
        ) : (
          <p className="m-auto text-muted">Select a chat or start a new one</p>
        )}
      </div>
    </div>
  )
}
