import type { UIEvent } from './api'

// Notice is what the chat shows beside the transcript when the session is
// waiting on a Provider problem (provider-gateway.md §9 D9).
export type Notice =
  | { kind: 'sleeping'; wakeAt: Date; longWait: boolean }
  | { kind: 'error'; code: string; requestId: string; failed: boolean }

type SessionError = { code: string; retryable: boolean; request_id?: string }
type TimerSet = { wake_at: string; reason: string }

// sessionNotice derives the Notice from the stream's events: the session's
// status is its latest status change, and the cause is the last event that
// isn't one (a rename says nothing about the session's state either).
export function sessionNotice(events: UIEvent[]): Notice | null {
  let status = ''
  let cause: UIEvent | undefined
  for (let i = events.length - 1; i >= 0 && (!status || !cause); i--) {
    const e = events[i]
    if (e.type === 'session.status_changed') {
      status ||= (e.payload as { to: string }).to
    } else if (e.type !== 'session.renamed') {
      cause ??= e
    }
  }
  if (!cause) return null

  if (status === 'sleeping' && cause.type === 'timer.set') {
    const t = cause.payload as TimerSet
    return { kind: 'sleeping', wakeAt: new Date(t.wake_at), longWait: t.reason === 'long_wait' }
  }
  if (cause.type === 'session.error' && (status === 'awaiting_user' || status === 'failed')) {
    const err = cause.payload as SessionError
    if (!err.retryable) {
      return { kind: 'error', code: err.code, requestId: err.request_id ?? '', failed: status === 'failed' }
    }
  }
  return null
}
