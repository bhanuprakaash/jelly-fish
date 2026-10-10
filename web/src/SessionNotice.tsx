import { useState } from 'react'
import { interruptSession, resolveApproval, resolveElicitation, retrySession } from './lib/api'
import type { ElicitationRequest, Notice } from './lib/sessionNotice'
import { useAction } from './lib/useAction'

const billingURLs: Record<string, string> = {
  anthropic: 'https://console.anthropic.com/settings/billing',
  openai: 'https://platform.openai.com/settings/organization/billing/overview',
  gemini: 'https://aistudio.google.com/usage',
}

type Props = {
  sessionId: string
  notice: Notice
  provider?: string
  // picker is the model picker "Switch model" opens.
  picker?: React.ReactNode
}

function errorText(code: string, failed: boolean): string {
  if (failed) {
    return code === 'provider_down' || code === 'rate_limited'
      ? 'The provider kept failing. Try again.'
      : 'This chat hit an error.'
  }
  switch (code) {
    case 'key_invalid':
      return 'Key rejected'
    case 'billing':
      return 'Provider account out of credit / limit hit'
    case 'model_unavailable':
      return "This key can't use this model"
    case 'too_large':
      return 'File too large'
    default:
      return 'Internal error'
  }
}

function budgetAmount(dimension: string, n: number): string {
  return dimension === 'dollars' ? `$${(n / 1_000_000).toFixed(2)}` : n.toLocaleString('en-US')
}

const retryable = (code: string, failed: boolean) =>
  failed || code === 'key_invalid' || code === 'billing' || code === 'bug'

const linkClass = 'btn btn-secondary btn-sm'

function ToolApproval({ sessionId, notice }: { sessionId: string; notice: Extract<Notice, { kind: 'tool' }> }) {
  const { run, busy, error } = useAction({}, 'Could not do that. Try again.')
  const [reason, setReason] = useState('')
  const answer = (decision: 'allow' | 'deny', extra: { reason?: string; all?: boolean } = {}) =>
    run(() => resolveApproval(sessionId, notice.approvalId, decision, extra))
  const args = notice.args === undefined || notice.args === null ? '' : JSON.stringify(notice.args, null, 2)
  return (
    <div role="alert" className="w-full space-y-3 rounded-[14px] border border-attention-line bg-attention-tint px-4 py-3">
      <p className="text-sm font-semibold text-ink">
        {notice.index} of {notice.total} · {notice.connector} · <span className="font-mono">{notice.tool}</span>
      </p>
      {notice.reason && <p className="text-sm text-attention">{notice.reason}</p>}
      {args && (
        <pre className="overflow-x-auto whitespace-pre-wrap break-words rounded-btn border border-line bg-surface p-3 font-mono text-xs text-ink2">
          {args}
        </pre>
      )}
      <input
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        aria-label="Reason for denying"
        placeholder="Reason for denying (optional)"
        className="input w-full"
      />
      <div className="flex flex-wrap gap-2">
        <button type="button" disabled={busy} onClick={() => answer('allow')} className="btn btn-attention btn-sm">
          Approve once
        </button>
        {notice.total > 1 && (
          <button type="button" disabled={busy} onClick={() => answer('allow', { all: true })} className={linkClass}>
            Approve all
          </button>
        )}
        <button
          type="button"
          disabled={busy}
          onClick={() => answer('deny', { reason: reason.trim() || undefined })}
          className={linkClass}
        >
          Deny
        </button>
      </div>
      {error && <p className="text-sm text-danger">{error}</p>}
    </div>
  )
}

type Field = { name: string; label: string; type: string; options?: string[]; required: boolean; initial: string | boolean }

// formFields lists the properties of every form request, which one answer covers.
function formFields(requests: Record<string, ElicitationRequest>): Field[] {
  return Object.values(requests).flatMap((r) =>
    r.mode !== 'form' || !r.schema?.properties
      ? []
      : Object.entries(r.schema.properties).map(([name, f]) => ({
          name,
          label: f.title ?? name,
          type: f.enum ? 'enum' : (f.type ?? 'string'),
          options: f.enum,
          required: r.schema?.required?.includes(name) ?? false,
          initial: f.type === 'boolean' ? f.default === true : String(f.default ?? ''),
        })),
  )
}

function pageHost(url: string): string | null {
  try {
    const u = new URL(url)
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.hostname : null
  } catch {
    return null
  }
}

function Elicitation({ sessionId, notice }: { sessionId: string; notice: Extract<Notice, { kind: 'elicitation' }> }) {
  const { run, busy, error } = useAction({}, 'Could not do that. Try again.')
  const fields = formFields(notice.requests)
  const [values, setValues] = useState<Record<string, string | boolean>>(() =>
    Object.fromEntries(fields.map((f) => [f.name, f.initial])),
  )
  const answer = (action: 'accept' | 'decline' | 'cancel', content?: Record<string, unknown>) =>
    run(() => resolveElicitation(sessionId, notice.elicitationId, action, content))
  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const content: Record<string, unknown> = {}
    for (const f of fields) {
      const v = values[f.name]
      if (f.type === 'boolean') content[f.name] = v === true
      else if (v !== '') content[f.name] = f.type === 'number' || f.type === 'integer' ? Number(v) : v
    }
    answer('accept', fields.length > 0 ? content : undefined)
  }
  const set = (name: string, v: string | boolean) => setValues((prev) => ({ ...prev, [name]: v }))
  const requests = Object.entries(notice.requests)
  return (
    <form
      onSubmit={submit}
      role="alert"
      className="w-full space-y-3 rounded-[14px] border border-attention-line bg-attention-tint px-4 py-3"
    >
      <div className="flex items-start gap-3">
        <p className="min-w-0 flex-1 text-sm font-semibold text-ink">
          {notice.connector} · <span className="font-mono">{notice.tool}</span> needs your input
        </p>
        <button
          type="button"
          disabled={busy}
          onClick={() => answer('cancel')}
          aria-label="Cancel"
          className="btn btn-ghost btn-sm"
        >
          ✕
        </button>
      </div>
      {requests.map(([key, r]) => {
        const host = r.mode === 'url' && r.url ? pageHost(r.url) : null
        return (
          <div key={key} className="space-y-2">
            {r.message && <p className="whitespace-pre-wrap text-sm text-ink2">{r.message}</p>}
            {r.mode === 'url' && (
              <div className="flex flex-wrap items-center gap-3 text-sm">
                <span>
                  <span className="font-mono">{notice.tool}</span> wants to open{' '}
                  <span className="font-semibold">{host ?? 'an address that cannot be opened'}</span>
                </span>
                {host && (
                  <a href={r.url} target="_blank" rel="noopener noreferrer" className={linkClass}>
                    Open
                  </a>
                )}
              </div>
            )}
          </div>
        )
      })}
      {fields.map((f) => (
        <label key={f.name} className="flex flex-col gap-1 text-sm text-ink">
          {f.type === 'boolean' ? (
            <span className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={values[f.name] === true}
                onChange={(e) => set(f.name, e.target.checked)}
              />
              {f.label}
            </span>
          ) : (
            <>
              {f.label}
              {f.type === 'enum' ? (
                <select
                  value={String(values[f.name])}
                  required={f.required}
                  onChange={(e) => set(f.name, e.target.value)}
                  className="input w-full"
                >
                  <option value="" />
                  {f.options?.map((o) => (
                    <option key={o} value={o}>
                      {o}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  type={f.type === 'number' || f.type === 'integer' ? 'number' : 'text'}
                  step={f.type === 'integer' ? 1 : 'any'}
                  value={String(values[f.name])}
                  required={f.required}
                  onChange={(e) => set(f.name, e.target.value)}
                  className="input w-full"
                />
              )}
            </>
          )}
        </label>
      ))}
      <div className="flex flex-wrap gap-2">
        <button type="submit" disabled={busy} className="btn btn-attention btn-sm">
          {fields.length > 0 || requests.every(([, r]) => r.mode === 'form') ? 'Submit' : 'Done'}
        </button>
        <button type="button" disabled={busy} onClick={() => answer('decline')} className={linkClass}>
          Decline
        </button>
      </div>
      {error && <p className="text-sm text-danger">{error}</p>}
    </form>
  )
}

export function SessionNotice({ sessionId, notice, provider, picker }: Props) {
  const { run, busy, error } = useAction({}, 'Could not do that. Try again.')
  const [picking, setPicking] = useState(false)

  const act = (call: (sessionId: string) => Promise<void>) => run(() => call(sessionId))

  const button = (label: string, call: (sessionId: string) => Promise<void>, className = linkClass) => (
    <button type="button" disabled={busy} onClick={() => act(call)} className={className}>
      {label}
    </button>
  )

  if (notice.kind === 'tool') return <ToolApproval key={notice.approvalId} sessionId={sessionId} notice={notice} />
  if (notice.kind === 'elicitation') return <Elicitation key={notice.elicitationId} sessionId={sessionId} notice={notice} />

  let text: string
  let actions: React.ReactNode
  if (notice.kind === 'sleeping') {
    const at = notice.wakeAt.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
    text = `${notice.longWait ? 'Provider busy' : 'Provider having trouble'}, retrying at ${at}`
    actions = (
      <>
        {button('Retry now', retrySession)}
        {button('Stop retrying', interruptSession, 'btn btn-ghost btn-sm')}
      </>
    )
  } else if (notice.kind === 'budget') {
    const { approvalId, dimension, limit, used, nextLimit } = notice
    const unit = dimension === 'dollars' ? '' : ` ${dimension}`
    text = `Budget reached: ${budgetAmount(dimension, used)} of ${budgetAmount(dimension, limit)}${unit}`
    const answer = (decision: 'allow' | 'deny') => run(() => resolveApproval(sessionId, approvalId, decision))
    actions = (
      <>
        <button type="button" disabled={busy} onClick={() => answer('allow')} className="btn btn-attention btn-sm">
          Allow (up to {budgetAmount(dimension, nextLimit)})
        </button>
        <button type="button" disabled={busy} onClick={() => answer('deny')} className="btn btn-secondary btn-sm">
          Deny
        </button>
      </>
    )
  } else {
    text = errorText(notice.code, notice.failed)
    actions = (
      <>
        {notice.code === 'key_invalid' && !notice.failed && (
          <a href="/settings" className="btn btn-primary btn-sm">
            Update key
          </a>
        )}
        {notice.code === 'billing' && !notice.failed && provider && billingURLs[provider] && (
          <a href={billingURLs[provider]} target="_blank" rel="noopener noreferrer" className={linkClass}>
            Open provider billing
          </a>
        )}
        {(notice.code === 'model_unavailable' || notice.code === 'billing') && !notice.failed && picker && (
          <button type="button" onClick={() => setPicking(true)} className={linkClass}>
            Switch model
          </button>
        )}
        {retryable(notice.code, notice.failed) && button('Retry now', retrySession)}
      </>
    )
  }

  const sleeping = notice.kind === 'sleeping'
  const budget = notice.kind === 'budget'
  const requestId = notice.kind === 'error' ? notice.requestId : ''
  return (
    <div
      role={sleeping ? 'status' : 'alert'}
      className={`w-full rounded-[14px] border px-4 py-3 ${
        sleeping ? 'border-line bg-sunk' : budget ? 'border-attention-line bg-attention-tint' : 'border-danger bg-danger-tint'
      }`}
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {sleeping && (
          <svg
            width="16"
            height="16"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            aria-hidden="true"
            className="shrink-0 text-ink2"
          >
            <circle cx="12" cy="12" r="9" />
            <path d="M12 7v5l3 2" />
          </svg>
        )}
        <p
          className={`min-w-0 flex-1 basis-40 text-sm ${
            sleeping ? 'text-ink2' : budget ? 'font-semibold text-ink' : 'font-semibold text-danger'
          }`}
        >
          {text}
        </p>
        <div className="flex flex-wrap gap-2">{actions}</div>
      </div>
      {requestId && <p className="mt-2 text-right font-mono text-xs text-muted">ID {requestId}</p>}
      {picking && notice.kind === 'error' && <div className="mt-2">{picker}</div>}
      {error && <p className="mt-2 text-sm text-danger">{error}</p>}
    </div>
  )
}
