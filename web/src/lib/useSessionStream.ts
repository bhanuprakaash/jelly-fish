import { useEffect, useRef, useState } from 'react'
import type { Delta, UIEvent } from './api'
import { onVisibilityChange } from './visibility'

export type PendingCall = { turn_id: string; call_id: string; name: string }

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
  // turn's llm.response, turn.interrupted or session.error, cleared when an
  // attempt fails and another starts, and dropped on reconnect because the
  // deltas missed meanwhile are gone (event-log.md §5.12).
  const [partials, setPartials] = useState<Record<string, string>>({})
  // Streaming thinking so far, by turn_id; ends the same ways as partials.
  const [thoughts, setThoughts] = useState<Record<string, string>>({})
  // Tool calls the model has begun writing; each ends with its turn or with
  // its tool.call.requested, which brings the real card.
  const [pendingCalls, setPendingCalls] = useState<PendingCall[]>([])
  const lastSeqRef = useRef(0)
  // Turns that ended (llm.response, turn.interrupted or session.error); a late
  // delta for one is ignored.
  const doneTurnsRef = useRef(new Set<string>())

  useEffect(() => {
    if (!enabled) return

    let es: EventSource | null = null

    const open = () => {
      setFailed(false)
      setPartials({})
      setThoughts({})
      setPendingCalls([])
      // A stream opened later can't carry deltas of a turn that already ended.
      doneTurnsRef.current.clear()
      es = new EventSource(`/api/sessions/${sessionId}/events?after=${lastSeqRef.current}`)
      es.onopen = () => setConnected(true)
      es.onmessage = (e) => {
        const evt = JSON.parse(e.data) as UIEvent
        lastSeqRef.current = evt.seq
        setEvents((prev) => [...prev, evt])
        if (evt.type === 'tool.call.requested') {
          const callId = (evt.payload as { tool_call_id?: string }).tool_call_id
          setPendingCalls((prev) => prev.filter((c) => c.call_id !== callId))
        }
        if (
          evt.type === 'llm.response' ||
          evt.type === 'turn.interrupted' ||
          evt.type === 'session.error'
        ) {
          const payload = evt.payload as {
            turn_id?: string
            message?: { parts: { tu?: { id: string } }[] }
          }
          const turnId = payload.turn_id
          if (!turnId) return
          // The reply's tool calls get their tool.call.requested in a later step.
          const asked = new Set(
            evt.type === 'llm.response' ? payload.message?.parts.flatMap((p) => (p.tu ? [p.tu.id] : [])) : [],
          )
          doneTurnsRef.current.add(turnId)
          setPartials((prev) => {
            const next = { ...prev }
            delete next[turnId]
            return next
          })
          setThoughts((prev) => {
            const next = { ...prev }
            delete next[turnId]
            return next
          })
          setPendingCalls((prev) => prev.filter((c) => c.turn_id !== turnId || asked.has(c.call_id)))
        }
      }
      es.addEventListener('delta', (e) => {
        const d = JSON.parse((e as MessageEvent<string>).data) as Delta
        if (doneTurnsRef.current.has(d.turn_id)) return
        if (d.kind === 'reset') {
          setPartials((prev) => {
            const next = { ...prev }
            delete next[d.turn_id]
            return next
          })
          setThoughts((prev) => {
            const next = { ...prev }
            delete next[d.turn_id]
            return next
          })
          setPendingCalls((prev) => prev.filter((c) => c.turn_id !== d.turn_id))
          return
        }
        if (d.kind === 'tool_start') {
          const { call_id: callId } = d
          if (!callId) return
          setPendingCalls((prev) => [...prev, { turn_id: d.turn_id, call_id: callId, name: d.name ?? '' }])
          return
        }
        if (d.kind === 'thinking') {
          setThoughts((prev) => ({ ...prev, [d.turn_id]: (prev[d.turn_id] ?? '') + d.text }))
          return
        }
        if (d.kind !== 'text') return
        setPartials((prev) => ({ ...prev, [d.turn_id]: (prev[d.turn_id] ?? '') + d.text }))
      })
      es.onerror = () => {
        setConnected(false)
        // The browser reconnects on its own, and the deltas missed meanwhile
        // are gone, so a half-built bubble would only show the tail.
        setPartials({})
        setThoughts({})
        setPendingCalls([])
        if (es?.readyState === EventSource.CLOSED) {
          setFailed(true)
        }
      }
    }

    open()
    const stopWatching = onVisibilityChange(
      () => {
        es?.close()
        setConnected(false)
      },
      () => {
        es?.close()
        open()
      },
    )
    return () => {
      stopWatching()
      es?.close()
    }
  }, [sessionId, enabled])

  return { events, partials, thoughts, pendingCalls, connected, failed }
}
