import { useEffect, useState } from 'react'
import {
  deleteLoginSession,
  deleteProviderKey,
  getLoginSessions,
  getMe,
  getProviderKeys,
  logout,
  logoutAll,
  putProviderKey,
  type LoginSession,
  type Me,
  type ProviderKey,
} from './lib/api'
import { useAction } from './lib/useAction'
import { getTheme, setTheme, themes, type Theme } from './lib/theme'

type Props = {
  // onSignedOut is called once this device has no Login Session left.
  onSignedOut: () => void
}

// Settings is the Account page: who is signed in, and on which devices.
export function Settings({ onSignedOut }: Props) {
  const [me, setMe] = useState<Me | null>(null)
  const [devices, setDevices] = useState<LoginSession[] | null>(null)
  const { run, error, setError } = useAction()

  // Bumping reload refetches the page's data.
  const [reload, setReload] = useState(0)

  useEffect(() => {
    Promise.all([getMe(), getLoginSessions()]).then(
      ([m, list]) => {
        setMe(m)
        setDevices(list)
      },
      () => setError('Could not load your account.'),
    )
  }, [reload, setError])

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

        <ProviderKeys />

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
              theme === t ? 'bg-surface font-semibold text-ink [&:not(:focus-visible)]:shadow-[0_1px_2px_rgb(0_0_0/0.12)]' : 'font-medium text-muted hover:text-ink'
            }`}
          >
            {themeLabels[t]}
          </button>
        ))}
      </div>
    </section>
  )
}

const keyProviders = [
  { id: 'anthropic', name: 'Anthropic' },
  { id: 'openai', name: 'OpenAI' },
  { id: 'gemini', name: 'Gemini' },
]

// ProviderKeys is one row per Provider; a saved key shows only its last four
// characters and can be replaced or deleted, never read back.
function ProviderKeys() {
  const [keys, setKeys] = useState<ProviderKey[] | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    getProviderKeys().then(setKeys, () => setError('Could not load your provider keys.'))
  }, [])

  return (
    <section className="space-y-3 border-t border-line pt-6">
      <h2 className="section-heading">Provider Keys</h2>
      {error && <p className="text-sm text-danger">{error}</p>}
      {keys &&
        keyProviders.map((p) => (
          <ProviderKeyRow key={p.id} provider={p.id} name={p.name} initial={keys.find((k) => k.provider === p.id) ?? null} />
        ))}
    </section>
  )
}

type RowProps = { provider: string; name: string; initial: ProviderKey | null }

function ProviderKeyRow({ provider, name, initial }: RowProps) {
  const [saved, setSaved] = useState<ProviderKey | null>(initial)
  const [editing, setEditing] = useState(false)
  const [adding, setAdding] = useState(false)
  const [key, setKey] = useState('')
  const { run, busy, error } = useAction({
    422: 'Key rejected',
    502: `Couldn't reach ${name}, try again`,
  })

  const save = () =>
    run(async () => {
      setSaved(await putProviderKey(provider, key))
      setKey('')
      setEditing(false)
      setAdding(false)
    })

  const remove = async () => {
    if (!window.confirm('Chats using this Provider will stop until you add a key.')) return
    await run(async () => {
      await deleteProviderKey(provider)
      setSaved(null)
      setEditing(false)
    })
  }

  const showInput = (saved === null && adding) || editing

  return (
    <div className="space-y-3 rounded-card border border-line bg-surface p-4">
      <div className="flex items-center gap-3">
        <p className="min-w-0 flex-1">
          {name}
          {saved && <span className="ml-2 font-mono text-sm text-muted">sk-…{saved.last4}</span>}
        </p>
        {!saved && !adding && (
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
            placeholder={`${name} API key`}
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
  )
}
