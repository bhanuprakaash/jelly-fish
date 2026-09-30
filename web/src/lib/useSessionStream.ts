import { useEffect, useRef, useState } from 'react'
import type { Delta, UIEvent } from './api'

// useSessionStream replays a session's Session stream and keeps it live,
// closing on tab hide and reopening with ?after=lastSeq on show
// (docs/design/streaming.md §5.4). It only opens while enabled is true, so a
// brand-new chat can defer connecting until its session exists.
export function useSessionStream(sessionId: string, enabled: boolean) {
  const [events, setEvents] = useState<UIEvent[]>([])
  const [connected, setConnected] = useState(false)
  // True once the browser gives up on the connection for good (e.g. a 404
  // for a session that doesn't exist): EventSource never retries that case,
  // so without tracking it separately "connected" would just stay false
  // forever with no way to tell "still connecting" from "never will".
  const [failed, setFailed] = useState(false)
  // Streaming reply text so far, by turn_id. Ephemeral: replaced by the
  // turn's llm.response or turn.interrupted, and dropped on reconnect because
  // the deltas missed meanwhile are gone (event-log.md §5.12).
  const [partials, setPartials] = useState<Record<string, string>>({})
  const lastSeqRef = useRef(0)
  // Turns that ended (llm.response or turn.interrupted); a late delta for one
  // is ignored.
  const doneTurnsRef = useRef(new Set<string>())

  useEffect(() => {
    if (!enabled) return

    let es: EventSource | null = null

    const open = () => {
      setFailed(false)
      setPartials({})
      // A stream opened later can't carry deltas of a turn that already ended.
      doneTurnsRef.current.clear()
      es = new EventSource(`/api/sessions/${sessionId}/events?after=${lastSeqRef.current}`)
      es.onopen = () => setConnected(true)
      es.onmessage = (e) => {
        const evt = JSON.parse(e.data) as UIEvent
        lastSeqRef.current = evt.seq
        setEvents((prev) => [...prev, evt])
        if (evt.type === 'llm.response' || evt.type === 'turn.interrupted') {
          const turnId = (evt.payload as { turn_id: string }).turn_id
          doneTurnsRef.current.add(turnId)
          setPartials((prev) => {
            const next = { ...prev }
            delete next[turnId]
            return next
          })
        }
      }
      es.addEventListener('delta', (e) => {
        const d = JSON.parse((e as MessageEvent<string>).data) as Delta
        if (d.kind !== 'text' || doneTurnsRef.current.has(d.turn_id)) return
        setPartials((prev) => ({ ...prev, [d.turn_id]: (prev[d.turn_id] ?? '') + d.text }))
      })
      es.onerror = () => {
        setConnected(false)
        // The browser reconnects on its own, and the deltas missed meanwhile
        // are gone, so a half-built bubble would only show the tail.
        setPartials({})
        if (es?.readyState === EventSource.CLOSED) {
          setFailed(true)
        }
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

  return { events, partials, connected, failed }
}
