import { useState } from 'react'
import { interruptSession, retrySession, UnauthorizedError } from './lib/api'
import type { Notice } from './lib/sessionNotice'

const BILLING_URL = 'https://console.anthropic.com/settings/billing'

type Props = {
  sessionId: string
  notice: Notice
  // picker is the model picker "Switch model" opens.
  picker?: React.ReactNode
  onUnauthorized: () => void
}

function errorText(code: string, requestId: string, failed: boolean): string {
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
      return `Internal error (ID ${requestId})`
  }
}

const retryable = (code: string, failed: boolean) =>
  failed || code === 'key_invalid' || code === 'billing' || code === 'bug'

const linkClass = 'btn btn-secondary btn-sm'

export function SessionNotice({ sessionId, notice, picker, onUnauthorized }: Props) {
  const [busy, setBusy] = useState(false)
  const [picking, setPicking] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const act = async (call: (sessionId: string) => Promise<void>) => {
    setBusy(true)
    setError(null)
    try {
      await call(sessionId)
    } catch (err) {
      if (err instanceof UnauthorizedError) {
        onUnauthorized()
        return
      }
      setError('Could not do that. Try again.')
    } finally {
      setBusy(false)
    }
  }

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
    text = errorText(notice.code, notice.requestId, notice.failed)
    actions = (
      <>
        {notice.code === 'key_invalid' && !notice.failed && (
          <a href="/settings" className={linkClass}>
            Update key
          </a>
        )}
        {notice.code === 'billing' && !notice.failed && (
          <a href={BILLING_URL} target="_blank" rel="noopener noreferrer" className={linkClass}>
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

  return (
    <div
      role="alert"
      className={`mx-auto max-w-md rounded-card border px-4 py-3 ${
        notice.kind === 'sleeping' ? 'border-line bg-sunk' : 'border-danger bg-surface'
      }`}
    >
      <p className="text-sm text-ink">{text}</p>
      <div className="mt-2 flex flex-wrap gap-2">{actions}</div>
      {picking && notice.kind === 'error' && <div className="mt-2">{picker}</div>}
      {error && <p className="mt-2 text-sm text-danger">{error}</p>}
    </div>
  )
}
