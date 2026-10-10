import type { UIEvent } from './api'
import { sessionBudget } from './budget'

// Notice is what the chat shows beside the transcript when the session is
// waiting on a Provider problem (provider-gateway.md §9 D9).
export type Notice =
  | { kind: 'sleeping'; wakeAt: Date; longWait: boolean }
  | { kind: 'error'; code: string; requestId: string; failed: boolean }
  | { kind: 'budget'; approvalId: string; dimension: string; limit: number; used: number; nextLimit: number }

type SessionError = { code: string; retryable: boolean; request_id?: string }
type TimerSet = { wake_at: string; reason: string }
type BudgetExceeded = { dimension: string; limit: number; used: number }
type ApprovalRequested = { approval_id: string; kind: string; dimension: string }
type ApprovalResolved = { approval_id: string; decision: string }

const budgetField = { tokens: 'tokens', dollars: 'cost_micros', turns: 'turns' } as const

// openBudgetApproval finds the budget approval the session waits on: the
// newest approval.requested with no approval.resolved after it. An allow adds
// one snapshot limit, so the next limit is the limit plus the snapshot's.
function openBudgetApproval(events: UIEvent[]): Notice | null {
  let open: ApprovalRequested | null = null
  let exceeded: BudgetExceeded | undefined
  for (const e of events) {
    if (e.type === 'budget.exceeded') {
      exceeded = e.payload as BudgetExceeded
    } else if (e.type === 'approval.requested') {
      const a = e.payload as ApprovalRequested
      open = a.kind === 'budget' ? a : null
    } else if (e.type === 'approval.resolved') {
      if (open?.approval_id === (e.payload as ApprovalResolved).approval_id) open = null
    }
  }
  if (!open || !exceeded || exceeded.dimension !== open.dimension) return null
  const { limit, used } = exceeded
  const field = budgetField[open.dimension as keyof typeof budgetField]
  const base = (field && sessionBudget(events)?.[field]) || 0
  return { kind: 'budget', approvalId: open.approval_id, dimension: open.dimension, limit, used, nextLimit: limit + base }
}

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

  if (status === 'awaiting_approval') return openBudgetApproval(events)
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
