import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { getSessions, UnauthorizedError, type ChatSummary } from './api'
import { badgeFor, needsListRefetch, type ActivityEntry, type Badge } from './activity'
import { useActivityStream } from './useActivityStream'

// useChatList loads the Chat List on mount and again on every refetch, and
// returns each chat's live badge from the Activity Stream. chats stays null
// until the first load succeeds; a failed reload keeps the old list.
export function useChatList(onUnauthorized: () => void) {
  const [chats, setChats] = useState<ChatSummary[] | null>(null)
  const [error, setError] = useState(false)
  const chatsRef = useRef<ChatSummary[] | null>(null)

  // One load at a time: a refetch asked for meanwhile runs once more after it,
  // so a burst of frames costs at most two requests and the last one wins.
  const loading = useRef(false)
  const again = useRef(false)
  const alive = useRef(true)

  const load = useCallback(async () => {
    if (loading.current) {
      again.current = true
      return
    }
    loading.current = true
    try {
      do {
        again.current = false
        try {
          const list = await getSessions()
          if (!alive.current) return
          chatsRef.current = list
          setChats(list)
          setError(false)
        } catch (err) {
          if (!alive.current) return
          if (err instanceof UnauthorizedError) onUnauthorized()
          else setError(true)
        }
      } while (again.current)
    } finally {
      loading.current = false
    }
  }, [onUnauthorized])

  useEffect(() => {
    alive.current = true
    void load()
    return () => {
      alive.current = false
    }
  }, [load])

  const refetch = useCallback(() => {
    void load()
  }, [load])

  const onFrame = useCallback(
    (entries: ActivityEntry[]) => {
      if (entries.some((e) => needsListRefetch(chatsRef.current, e))) refetch()
    },
    [refetch],
  )
  const activity = useActivityStream(onFrame)

  const badges = useMemo(() => {
    const out: Record<string, Badge> = {}
    for (const c of chats ?? []) {
      const badge = badgeFor(c.id, activity)
      if (badge) out[c.id] = badge
    }
    return out
  }, [chats, activity])

  return { chats, error, refetch, badges }
}
