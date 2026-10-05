import { useState } from 'react'
import { verifyLink } from './lib/api'

type Props = {
  token: string
  onSignedIn: () => void
}

// LinkSignIn is the page the emailed link opens. Only the button consumes the
// link, so mail scanners that fetch the page don't use it up.
export function LinkSignIn({ token, onSignedIn }: Props) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const signIn = async () => {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      if (await verifyLink(token)) {
        window.history.replaceState({}, '', '/')
        onSignedIn()
        return
      }
      setError('This link is expired or already used. Request a new code.')
    } catch {
      setError('Something went wrong. Try again.')
    }
    setBusy(false)
  }

  return (
    <main className="flex min-h-svh items-center justify-center bg-bg px-4 text-ink">
      <div className="w-full max-w-sm rounded-panel border border-line bg-surface p-8 text-center sm:max-w-md">
        <h1 className="text-2xl font-semibold sm:text-3xl">Sign in to jelly-fish</h1>
        {error && <p className="mt-3 text-sm text-danger">{error}</p>}
        <button
          onClick={signIn}
          disabled={busy}
          className="btn btn-primary btn-lg mt-6 w-full"
        >
          Sign in
        </button>
      </div>
    </main>
  )
}
