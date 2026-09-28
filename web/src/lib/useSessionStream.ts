import { useEffect, useRef, useState } from 'react'
import type { UIEvent } from './api'

// useSessionStream replays a session's Session stream and keeps it live,
// closing on tab hide and reopening with ?after=lastSeq on show
// (docs/design/streaming.md §5.4). It only opens while enabled is true, so a
// brand-new chat can defer connecting until its session exists.
export function useSessionStream(sessionId: string, enabled: boolean) {
  const [events, setEvents] = useState<UIEvent[]>([])
  const [connected, setConnected] = useState(false)
  const lastSeqRef = useRef(0)

  useEffect(() => {
    if (!enabled) return

    let es: EventSource | null = null

    const open = () => {
      es = new EventSource(`/api/sessions/${sessionId}/events?after=${lastSeqRef.current}`)
      es.onopen = () => setConnected(true)
      es.onmessage = (e) => {
        const evt = JSON.parse(e.data) as UIEvent
        lastSeqRef.current = evt.seq
        setEvents((prev) => [...prev, evt])
      }
    }

    const onVisibility = () => {
      if (document.visibilityState === 'hidden') {
        es?.close()
        setConnected(false)
      } else {
        es?.close()
        open()
      }
    }

    open()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      es?.close()
    }
  }, [sessionId, enabled])

  return { events, connected }
}
