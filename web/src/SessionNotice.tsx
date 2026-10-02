import { useState } from 'react'
import { interruptSession, retrySession, UnauthorizedError } from './lib/api'
import type { Notice } from './lib/sessionNotice'

const BILLING_URL = 'https://console.anthropic.com/settings/billing'

type Props = {
  sessionId: string
  notice: Notice
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

const linkClass = 'rounded-lg bg-neutral-800 px-3 py-1 text-sm hover:bg-neutral-700'

export function SessionNotice({ sessionId, notice, onUnauthorized }: Props) {
  const [busy, setBusy] = useState(false)
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

  const button = (label: string, call: (sessionId: string) => Promise<void>) => (
    <button type="button" disabled={busy} onClick={() => act(call)} className={`${linkClass} disabled:opacity-50`}>
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
        {button('Stop retrying', interruptSession)}
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
        {retryable(notice.code, notice.failed) && button('Retry now', retrySession)}
      </>
    )
  }

  return (
    <div role="alert" className="mx-auto max-w-md rounded-2xl border border-neutral-800 bg-neutral-900 px-4 py-3">
      <p className="text-sm text-neutral-200">{text}</p>
      <div className="mt-2 flex flex-wrap gap-2">{actions}</div>
      {error && <p className="mt-2 text-sm text-red-400">{error}</p>}
    </div>
  )
}
