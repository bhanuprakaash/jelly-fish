import { useState } from 'react'
import { interruptSession, retrySession } from './lib/api'
import type { Notice } from './lib/sessionNotice'
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

const retryable = (code: string, failed: boolean) =>
  failed || code === 'key_invalid' || code === 'billing' || code === 'bug'

const linkClass = 'btn btn-secondary btn-sm'

export function SessionNotice({ sessionId, notice, provider, picker }: Props) {
  const { run, busy, error } = useAction({}, 'Could not do that. Try again.')
  const [picking, setPicking] = useState(false)

  const act = (call: (sessionId: string) => Promise<void>) => run(() => call(sessionId))

  const button = (label: string, call: (sessionId: string) => Promise<void>, className = linkClass) => (
    <button type="button" disabled={busy} onClick={() => act(call)} className={className}>
      {label}
    </button>
  )

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
  const requestId = notice.kind === 'error' ? notice.requestId : ''
  return (
    <div
      role={sleeping ? 'status' : 'alert'}
      className={`w-full rounded-[14px] border px-4 py-3 ${
        sleeping ? 'border-line bg-sunk' : 'border-danger bg-danger-tint'
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
        <p className={`min-w-0 flex-1 basis-40 text-sm ${sleeping ? 'text-ink2' : 'font-semibold text-danger'}`}>{text}</p>
        <div className="flex flex-wrap gap-2">{actions}</div>
      </div>
      {requestId && <p className="mt-2 text-right font-mono text-xs text-muted">ID {requestId}</p>}
      {picking && notice.kind === 'error' && <div className="mt-2">{picker}</div>}
      {error && <p className="mt-2 text-sm text-danger">{error}</p>}
    </div>
  )
}
