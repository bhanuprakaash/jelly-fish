import { useState } from 'react'
import { HttpError } from './api'

// useAction wraps async handlers with busy and error state. A failure shows
// byStatus[status] for an HttpError with that status, else fallback.
export function useAction(byStatus: Record<number, string> = {}, fallback = 'Something went wrong. Try again.') {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const run = async (action: () => Promise<void>) => {
    setBusy(true)
    setError(null)
    try {
      await action()
    } catch (err) {
      setError((err instanceof HttpError && byStatus[err.status]) || fallback)
    } finally {
      setBusy(false)
    }
  }

  return { run, busy, error, setError }
}
