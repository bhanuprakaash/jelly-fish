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
import { getTheme, setTheme, themes, type Theme } from './lib/theme'

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
    <div className="min-h-svh bg-bg text-ink">
      <header className="border-b border-line px-4 py-3">
        <a href="/" className="text-sm text-muted hover:text-ink">
          ← Back
        </a>
      </header>
      <main className="mx-auto max-w-2xl space-y-6 px-4 py-6 sm:px-8 sm:py-10">
        <div>
          <h1 className="text-3xl font-semibold tracking-[-0.035em] sm:text-4xl">Account</h1>
          {me && <p className="mt-1 text-muted">{me.email}</p>}
        </div>
        {error && <p className="text-sm text-danger">{error}</p>}

        <Appearance />

        <section className="space-y-3 border-t border-line pt-6">
          <h2 className="section-heading">Devices</h2>
          {devices?.map((d) => (
            <div key={d.id} className="flex items-center gap-3 rounded-card border border-line bg-surface p-4">
              <div className="min-w-0 flex-1">
                <p className="truncate">
                  {d.user_agent || 'Unknown device'}
                  {d.current && <span className="ml-2 text-sm text-muted">(this device)</span>}
                </p>
                <p className="text-sm text-muted">
                  Signed in {new Date(d.created_at).toLocaleDateString()}, last seen{' '}
                  {new Date(d.last_seen_at).toLocaleString()}
                </p>
              </div>
              <button
                onClick={() => logOutDevice(d)}
                className="btn btn-secondary btn-sm"
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
            className="btn btn-secondary btn-lg w-full"
          >
            Admin: Users
          </a>
        )}

        <button
          onClick={logOutEverywhere}
          className="btn btn-danger btn-lg w-full"
        >
          Log out everywhere
        </button>
      </main>
    </div>
  )
}

const themeLabels: Record<Theme, string> = {
  system: 'System',
  'jellyfish-light': 'Jellyfish light',
  'jellyfish-dark': 'Jellyfish dark',
  classic: 'Classic',
}

// Appearance picks the theme for this device only.
function Appearance() {
  const [theme, setChoice] = useState(getTheme)

  const choose = (t: Theme) => {
    setTheme(t)
    setChoice(t)
  }

  const arrow = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key]
    if (!step) return
    e.preventDefault()
    const next = themes[(themes.indexOf(theme) + step + themes.length) % themes.length]
    choose(next)
    e.currentTarget.querySelectorAll<HTMLElement>('[role=radio]')[themes.indexOf(next)].focus()
  }

  return (
    <section className="space-y-3 border-t border-line pt-6">
      <h2 className="section-heading">Appearance</h2>
      <div
        role="radiogroup"
        aria-label="Theme"
        onKeyDown={arrow}
        className="inline-flex max-w-full overflow-x-auto rounded-xl border border-line bg-sunk p-0.5"
      >
        {themes.map((t) => (
          <button
            key={t}
            role="radio"
            aria-checked={theme === t}
            tabIndex={theme === t ? 0 : -1}
            onClick={() => choose(t)}
            className={`h-9 shrink-0 cursor-pointer rounded-[8px] px-3 text-[13px] whitespace-nowrap focus-ring-inset sm:px-4 sm:text-sm ${
              theme === t ? 'bg-surface font-semibold text-ink shadow-[0_1px_2px_rgb(0_0_0/0.12)]' : 'font-medium text-muted hover:text-ink'
            }`}
          >
            {themeLabels[t]}
          </button>
        ))}
      </div>
    </section>
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
    <section className="space-y-3 border-t border-line pt-6">
      <h2 className="section-heading">Provider Keys</h2>
      <div className="space-y-3 rounded-card border border-line bg-surface p-4">
        <div className="flex items-center gap-3">
          <p className="min-w-0 flex-1">
            Anthropic
            {saved && <span className="ml-2 font-mono text-sm text-muted">sk-…{saved.last4}</span>}
          </p>
          {!saved && loaded && !adding && (
            <button
              onClick={() => setAdding(true)}
              className="btn btn-secondary btn-sm"
            >
              Add key
            </button>
          )}
          {saved && !editing && (
            <>
              <button
                onClick={() => setEditing(true)}
                className="btn btn-secondary btn-sm"
              >
                Replace
              </button>
              <button onClick={remove} className="btn btn-danger btn-sm">
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
              className="input min-w-0 flex-1"
            />
            <button
              type="submit"
              disabled={busy || key.trim() === ''}
              className="btn btn-primary"
            >
              Save
            </button>
          </form>
        )}
        {error && <p className="text-sm text-danger">{error}</p>}
      </div>
      {comingSoon.map((name) => (
        <div key={name} className="flex items-center gap-3 rounded-card border border-line bg-surface p-4 text-muted">
          <p className="min-w-0 flex-1">{name}</p>
          <span className="text-sm text-muted">Coming soon</span>
        </div>
      ))}
    </section>
  )
}
