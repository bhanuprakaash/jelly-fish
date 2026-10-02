import { useEffect, useState } from 'react'
import {
  deleteLoginSession,
  deleteProviderKey,
  getLoginSessions,
  getMe,
  getProviderKeys,
  HttpError,
  logout,
  logoutAll,
  putProviderKey,
  UnauthorizedError,
  type LoginSession,
  type Me,
  type ProviderKey,
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

        <ProviderKeys onSignedOut={onSignedOut} />

        {me?.is_admin && (
          <a
            href="/admin/users"
            className="block rounded-xl bg-neutral-900 px-6 py-3 text-center text-neutral-200"
          >
            Admin: Users
          </a>
        )}

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

const comingSoon = ['OpenAI', 'Gemini']

// ProviderKeys is one row per Provider; a saved key shows only its last four
// characters and can be replaced or deleted, never read back.
function ProviderKeys({ onSignedOut }: Props) {
  const [saved, setSaved] = useState<ProviderKey | null>(null)
  const [loaded, setLoaded] = useState(false)
  const [editing, setEditing] = useState(false)
  const [adding, setAdding] = useState(false)
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    getProviderKeys().then(
      (list) => {
        setSaved(list.find((k) => k.provider === 'anthropic') ?? null)
        setLoaded(true)
      },
      (err) => {
        if (err instanceof UnauthorizedError) onSignedOut()
        else setError('Could not load your provider keys.')
      },
    )
  }, [onSignedOut])

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      setSaved(await putProviderKey('anthropic', key))
      setKey('')
      setEditing(false)
      setAdding(false)
    } catch (err) {
      if (err instanceof UnauthorizedError) onSignedOut()
      else if (err instanceof HttpError && err.status === 422) setError('Key rejected')
      else if (err instanceof HttpError && err.status === 502)
        setError("Couldn't reach Anthropic, try again")
      else setError('Something went wrong. Try again.')
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!window.confirm('Chats using this Provider will stop until you add a key.')) return
    setError(null)
    try {
      await deleteProviderKey('anthropic')
      setSaved(null)
      setEditing(false)
    } catch (err) {
      if (err instanceof UnauthorizedError) onSignedOut()
      else setError('Something went wrong. Try again.')
    }
  }

  const showInput = loaded && ((saved === null && adding) || editing)

  return (
    <section className="space-y-3">
      <h2 className="text-lg font-medium">Provider Keys</h2>
      <div className="space-y-3 rounded-xl bg-neutral-900 p-4">
        <div className="flex items-center gap-3">
          <p className="min-w-0 flex-1">
            Anthropic
            {saved && <span className="ml-2 text-sm text-neutral-400">sk-…{saved.last4}</span>}
          </p>
          {!saved && loaded && !adding && (
            <button
              onClick={() => setAdding(true)}
              className="rounded-xl bg-neutral-800 px-4 py-2 text-sm"
            >
              Add key
            </button>
          )}
          {saved && !editing && (
            <>
              <button
                onClick={() => setEditing(true)}
                className="rounded-xl bg-neutral-800 px-4 py-2 text-sm"
              >
                Replace
              </button>
              <button onClick={remove} className="rounded-xl bg-neutral-800 px-4 py-2 text-sm">
                Delete
              </button>
            </>
          )}
        </div>
        {showInput && (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              save()
            }}
            className="flex gap-3"
          >
            <input
              type="password"
              autoComplete="off"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              placeholder="Anthropic API key"
              className="min-w-0 flex-1 rounded-xl bg-neutral-800 px-4 py-2"
            />
            <button
              type="submit"
              disabled={busy || key.trim() === ''}
              className="rounded-xl bg-neutral-100 px-4 py-2 text-sm font-medium text-neutral-900 disabled:opacity-50"
            >
              Save
            </button>
          </form>
        )}
        {error && <p className="text-sm text-red-400">{error}</p>}
      </div>
      {comingSoon.map((name) => (
        <div key={name} className="flex items-center gap-3 rounded-xl bg-neutral-900 p-4 opacity-50">
          <p className="min-w-0 flex-1">{name}</p>
          <span className="text-sm text-neutral-400">Coming soon</span>
        </div>
      ))}
    </section>
  )
}
