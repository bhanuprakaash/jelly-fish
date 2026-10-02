import { useState, type SubmitEvent } from 'react'
import { requestCode, verifyCode } from './lib/api'

type Props = {
  onSignedIn: () => void
}

const inputClass = 'w-full rounded-xl bg-neutral-800 px-4 py-2 outline-none disabled:opacity-50'
const buttonClass =
  'mt-4 w-full rounded-xl bg-neutral-100 px-6 py-3 font-medium text-neutral-900 disabled:opacity-50'

export function Login({ onSignedIn }: Props) {
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [codeSent, setCodeSent] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async (e: SubmitEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      if (!codeSent) {
        await requestCode(email.trim())
        setCodeSent(true)
      } else if (await verifyCode(email.trim(), code.trim())) {
        onSignedIn()
      } else {
        setError('Wrong or expired code.')
      }
    } catch {
      setError('Something went wrong. Try again.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="flex min-h-svh items-center justify-center bg-neutral-950 px-4 text-neutral-100">
      <form
        onSubmit={submit}
        className="w-full max-w-sm rounded-2xl bg-neutral-900 p-8 shadow-xl sm:max-w-md"
      >
        <h1 className="text-center text-2xl font-semibold sm:text-3xl">Jelly-fish</h1>
        {codeSent ? (
          <>
            <p className="mt-6 text-center text-neutral-300">
              Check your email. If you're invited, a 6-digit code is on its way.
            </p>
            <input
              className={`${inputClass} mt-4 text-center tracking-widest`}
              value={code}
              onChange={(e) => setCode(e.target.value)}
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={6}
              placeholder="123456"
              aria-label="Code"
              autoFocus
            />
          </>
        ) : (
          <input
            className={`${inputClass} mt-6`}
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="email"
            placeholder="Email"
            aria-label="Email"
            required
            autoFocus
          />
        )}
        {error && <p className="mt-3 text-center text-sm text-red-400">{error}</p>}
        <button
          type="submit"
          disabled={busy || (codeSent ? code.trim().length !== 6 : !email.trim())}
          className={buttonClass}
        >
          {codeSent ? 'Sign in' : 'Email me a code'}
        </button>
      </form>
    </main>
  )
}
