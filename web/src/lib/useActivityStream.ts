import { useEffect, useRef, useState } from 'react'
import { applySnapshot, applyStatus, type ActivityEntry, type ActivityMap } from './activity'
import { onVisibilityChange } from './visibility'

// useActivityStream keeps the busy sessions of every chat live from the
// Activity Stream. It closes on tab hide and reopens on show, which starts
// from a fresh snapshot (docs/design/streaming.md §5.4). onFrame gets the
// entries of each snapshot and status frame, so the owner can decide to
// refetch the Chat List. A refused stream (429, 401) stays closed.
export function useActivityStream(onFrame: (entries: ActivityEntry[]) => void): ActivityMap {
  const [activity, setActivity] = useState<ActivityMap>({})
  const onFrameRef = useRef(onFrame)
  useEffect(() => {
    onFrameRef.current = onFrame
  }, [onFrame])

  useEffect(() => {
    let es: EventSource | null = null

    const open = () => {
      es = new EventSource('/api/activity')
      es.addEventListener('snapshot', (e) => {
        const entries = JSON.parse((e as MessageEvent<string>).data) as ActivityEntry[]
        setActivity(applySnapshot(entries))
        onFrameRef.current(entries)
      })
      es.addEventListener('status', (e) => {
        const entry = JSON.parse((e as MessageEvent<string>).data) as ActivityEntry
        setActivity((prev) => applyStatus(prev, entry))
        onFrameRef.current([entry])
      })
    }

    open()
    const stopWatching = onVisibilityChange(
      () => es?.close(),
      () => {
        es?.close()
        open()
      },
    )
    return () => {
      stopWatching()
      es?.close()
    }
  }, [])

  return activity
}
