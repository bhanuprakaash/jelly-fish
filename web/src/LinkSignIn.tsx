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
    <main className="flex min-h-svh items-center justify-center bg-neutral-950 px-4 text-neutral-100">
      <div className="w-full max-w-sm rounded-2xl bg-neutral-900 p-8 text-center shadow-xl sm:max-w-md">
        <h1 className="text-2xl font-semibold sm:text-3xl">Sign in to jelly-fish</h1>
        {error && <p className="mt-3 text-sm text-red-400">{error}</p>}
        <button
          onClick={signIn}
          disabled={busy}
          className="mt-6 w-full rounded-xl bg-neutral-100 px-6 py-3 font-medium text-neutral-900 disabled:opacity-50"
        >
          Sign in
        </button>
      </div>
    </main>
  )
}
