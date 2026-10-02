import { useEffect, useState } from 'react'
import {
  deleteLoginSession,
  getLoginSessions,
  getMe,
  logout,
  logoutAll,
  UnauthorizedError,
  type LoginSession,
  type Me,
} from './lib/api'

type Props = {
  // onSignedOut is called once this device has no Login Session left.
  onSignedOut: () => void
}

// Settings is the Account page: who is signed in, and on which devices.
export function Settings({ onSignedOut }: Props) {
  const [me, setMe] = useState<Me | null>(null)
  const [devices, setDevices] = useState<LoginSession[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Bumping reload refetches the page's data.
  const [reload, setReload] = useState(0)

  useEffect(() => {
    Promise.all([getMe(), getLoginSessions()]).then(
      ([m, list]) => {
        setMe(m)
        setDevices(list)
      },
      (err) => {
        if (err instanceof UnauthorizedError) onSignedOut()
        else setError('Could not load your account.')
      },
    )
  }, [reload, onSignedOut])

  const run = async (action: () => Promise<void>) => {
    setError(null)
    try {
      await action()
    } catch (err) {
      if (err instanceof UnauthorizedError) onSignedOut()
      else setError('Something went wrong. Try again.')
    }
  }

  const logOutDevice = (d: LoginSession) =>
    run(async () => {
      if (d.current) {
        await logout()
        onSignedOut()
        return
      }
      await deleteLoginSession(d.id)
      setReload((n) => n + 1)
    })

  const logOutEverywhere = () =>
    run(async () => {
      await logoutAll()
      onSignedOut()
    })

  return (
    <div className="min-h-svh bg-neutral-950 text-neutral-100">
      <header className="border-b border-neutral-800 px-4 py-3">
        <a href="/" className="text-sm text-neutral-400 hover:text-neutral-200">
          ← Back
        </a>
      </header>
      <main className="mx-auto max-w-md space-y-6 px-4 py-6">
        <h1 className="text-2xl font-semibold">Account</h1>
        {me && <p className="text-neutral-300">{me.email}</p>}
        {error && <p className="text-sm text-red-400">{error}</p>}

        <section className="space-y-3">
          <h2 className="text-lg font-medium">Devices</h2>
          {devices?.map((d) => (
            <div key={d.id} className="flex items-center gap-3 rounded-xl bg-neutral-900 p-4">
              <div className="min-w-0 flex-1">
                <p className="truncate">
                  {d.user_agent || 'Unknown device'}
                  {d.current && <span className="ml-2 text-sm text-neutral-400">(this device)</span>}
                </p>
                <p className="text-sm text-neutral-500">
                  Signed in {new Date(d.created_at).toLocaleDateString()}, last seen{' '}
                  {new Date(d.last_seen_at).toLocaleString()}
                </p>
              </div>
              <button
                onClick={() => logOutDevice(d)}
                className="rounded-xl bg-neutral-800 px-4 py-2 text-sm"
              >
                Log out
              </button>
            </div>
          ))}
        </section>

        <button
          onClick={logOutEverywhere}
          className="w-full rounded-xl bg-neutral-100 px-6 py-3 font-medium text-neutral-900"
        >
          Log out everywhere
        </button>
      </main>
    </div>
  )
}
