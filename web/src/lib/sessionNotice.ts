import type { UIEvent } from './api'
import { sessionBudget } from './budget'

// Notice is what the chat shows beside the transcript when the session is
// waiting on a Provider problem (provider-gateway.md §9 D9).
export type Notice =
  | { kind: 'sleeping'; wakeAt: Date; longWait: boolean }
  | { kind: 'error'; code: string; requestId: string; failed: boolean }
  | { kind: 'budget'; approvalId: string; dimension: string; limit: number; used: number; nextLimit: number }
  | {
      kind: 'tool'
      approvalId: string
      // index is the 1-based place of the call among the calls of its batch that ask.
      index: number
      total: number
      connector: string
      tool: string
      args: unknown
      reason?: string
    }
  | { kind: 'elicitation'; elicitationId: string; connector: string; tool: string; requests: Record<string, ElicitationRequest> }

// ElicitationRequest is one thing a Connector's tool asks the user: a form
// over a flat schema, or a page to open.
export type ElicitationRequest = {
  mode: 'form' | 'url'
  message: string
  schema?: { properties?: Record<string, SchemaField>; required?: string[] }
  url?: string
}

export type SchemaField = { type?: string; title?: string; description?: string; enum?: string[]; default?: string | number | boolean }

type SessionError = { code: string; retryable: boolean; request_id?: string }
type TimerSet = { wake_at: string; reason: string }
type BudgetExceeded = { dimension: string; limit: number; used: number }
type ApprovalRequested = {
  approval_id: string
  kind: string
  dimension: string
  tool_call_id?: string
  tool?: string
  args?: unknown
  connector?: string
  reason?: string
}
type ApprovalResolved = { approval_id: string; decision: string }
type ElicitationRequested = {
  elicitation_id: string
  tool_call_id: string
  connector?: string
  requests: Record<string, ElicitationRequest>
}

const budgetField = { tokens: 'tokens', dollars: 'cost_micros', turns: 'turns' } as const

// openBudgetApproval finds the budget approval the session waits on: the
// newest approval.requested with no approval.resolved after it. Like the
// worker's fold, the limit is base × (1 + allows since the last budget change).
function openBudgetApproval(events: UIEvent[]): Notice | null {
  let open: ApprovalRequested | null = null
  let exceeded: BudgetExceeded | undefined
  const dimensions = new Map<string, string>()
  let allows: Record<string, number> = {}
  for (const e of events) {
    if (e.type === 'budget.exceeded') {
      exceeded = e.payload as BudgetExceeded
    } else if (e.type === 'approval.requested') {
      const a = e.payload as ApprovalRequested
      if (a.kind === 'budget') dimensions.set(a.approval_id, a.dimension)
      open = a.kind === 'budget' ? a : null
    } else if (e.type === 'approval.resolved') {
      const r = e.payload as ApprovalResolved
      const dimension = dimensions.get(r.approval_id)
      if (dimension && r.decision === 'allow') allows[dimension] = (allows[dimension] ?? 0) + 1
      if (open?.approval_id === r.approval_id) open = null
    } else if (e.type === 'session.config_changed' && (e.payload as { budget?: unknown }).budget) {
      allows = {}
    }
  }
  if (!open || !exceeded || exceeded.dimension !== open.dimension) return null
  const field = budgetField[open.dimension as keyof typeof budgetField]
  const base = (field && sessionBudget(events)?.[field]) || exceeded.limit
  const n = allows[open.dimension] ?? 0
  return { kind: 'budget', approvalId: open.approval_id, dimension: open.dimension, limit: base * (1 + n), used: exceeded.used, nextLimit: base * (2 + n) }
}

// openToolApproval finds the tool approval the session waits on, and counts
// the calls of the latest reply that ask, which the card steps through.
function openToolApproval(events: UIEvent[]): Notice | null {
  let open: ApprovalRequested | null = null
  let asking: string[] = []
  for (const e of events) {
    if (e.type === 'llm.response') {
      asking = []
    } else if (e.type === 'tool.call.requested') {
      const p = e.payload as { tool_call_id: string; ask?: boolean }
      if (p.ask) asking.push(p.tool_call_id)
    } else if (e.type === 'approval.requested') {
      const a = e.payload as ApprovalRequested
      open = a.kind === 'tool' ? a : null
    } else if (e.type === 'approval.resolved') {
      if (open?.approval_id === (e.payload as ApprovalResolved).approval_id) open = null
    }
  }
  if (!open) return null
  const name = open.tool ?? ''
  return {
    kind: 'tool',
    approvalId: open.approval_id,
    index: asking.indexOf(open.tool_call_id ?? '') + 1,
    total: asking.length,
    connector: open.connector ?? '',
    tool: name.includes('__') ? name.slice(name.indexOf('__') + 2) : name,
    args: open.args,
    reason: open.reason,
  }
}

// openElicitation finds the elicitation a tool call waits on: asked, not yet
// answered, and its call not ended.
function openElicitation(events: UIEvent[]): Notice | null {
  const open = new Map<string, ElicitationRequested>()
  const tools = new Map<string, string>()
  for (const e of events) {
    const p = e.payload as ElicitationRequested & { tool?: string }
    if (e.type === 'tool.call.requested') {
      tools.set(p.tool_call_id, p.tool ?? '')
    } else if (e.type === 'elicitation.requested') {
      open.set(p.elicitation_id, p)
    } else if (e.type === 'elicitation.resolved') {
      open.delete(p.elicitation_id)
    } else if (e.type === 'tool.call.completed' || e.type === 'tool.call.interrupted') {
      for (const [id, asked] of open) if (asked.tool_call_id === p.tool_call_id) open.delete(id)
    }
  }
  const asked = open.values().next().value
  if (!asked) return null
  const name = tools.get(asked.tool_call_id) ?? ''
  return {
    kind: 'elicitation',
    elicitationId: asked.elicitation_id,
    connector: asked.connector ?? '',
    tool: name.includes('__') ? name.slice(name.indexOf('__') + 2) : name,
    requests: asked.requests,
  }
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

  if (status === 'awaiting_approval') return openToolApproval(events) ?? openBudgetApproval(events)
  // A legacy elicitation holds a running session; a modern one parks it.
  if (status === 'awaiting_user' || status === 'running') {
    const asking = openElicitation(events)
    if (asking) return asking
  }
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
