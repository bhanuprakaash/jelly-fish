import { useCallback, useEffect, useState } from 'react'
import { getSessions, UnauthorizedError, type ChatSummary } from './api'

// useChatList loads the Chat List on mount and again on every refetch. chats
// stays null until the first load succeeds; a failed reload keeps the old list.
export function useChatList(onUnauthorized: () => void) {
  const [chats, setChats] = useState<ChatSummary[] | null>(null)
  const [error, setError] = useState(false)

  // Bumping reload refetches the list.
  const [reload, setReload] = useState(0)
  const refetch = useCallback(() => setReload((n) => n + 1), [])

  useEffect(() => {
    let stale = false
    getSessions().then(
      (list) => {
        if (stale) return
        setChats(list)
        setError(false)
      },
      (err) => {
        if (stale) return
        if (err instanceof UnauthorizedError) onUnauthorized()
        else setError(true)
      },
    )
    return () => {
      stale = true
    }
  }, [reload, onUnauthorized])

  return { chats, error, refetch }
}
