import { useCallback, useEffect, useState } from 'react'
import {
  createInvite,
  getAdminUsers,
  HttpError,
  makeAdmin,
  resendInvite,
  revokeInvite,
  setUserDisabled,
  type AdminInvite,
  type AdminUser,
} from './lib/api'

type Loaded = { users: AdminUser[]; invites: AdminInvite[] }

function actionError(err: unknown): string {
  if (err instanceof HttpError && err.status === 409) {
    return 'Not allowed: that email is already a user, or it is the last active admin.'
  }
  if (err instanceof HttpError && err.status === 502) {
    return 'Invite saved, but the email could not be sent. Try Resend.'
  }
  return 'Something went wrong. Try again.'
}

// AdminUsers is the Admin → Users page: invite people and manage who can sign in.
export function AdminUsers() {
  const [data, setData] = useState<Loaded | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [email, setEmail] = useState('')

  // Bumping reload refetches the page's data.
  const [reload, setReload] = useState(0)

  useEffect(() => {
    // Ignores an answer that arrives after a newer reload started.
    let current = true
    getAdminUsers().then(
      (d) => {
        if (current) setData(d)
      },
      () => {
        if (current) setError('Could not load users. Only admins can see this page.')
      },
    )
    return () => {
      current = false
    }
  }, [reload])

  const run = useCallback(
    async (action: () => Promise<void>) => {
      setError(null)
      try {
        await action()
        setReload((n) => n + 1)
      } catch (err) {
        setError(actionError(err))
      }
    },
    [],
  )

  const invite = (e: React.FormEvent) => {
    e.preventDefault()
    void run(async () => {
      await createInvite(email)
      setEmail('')
    })
  }

  return (
    <div className="min-h-svh bg-bg text-ink">
      <header className="border-b border-line px-4 py-3">
        <a href="/settings" className="text-sm text-muted hover:text-ink">
          ← Back
        </a>
      </header>
      <main className="mx-auto max-w-md space-y-6 px-4 py-6">
        <h1 className="text-3xl font-semibold tracking-[-0.035em] sm:text-4xl">Users</h1>
        {error && <p className="text-sm text-danger">{error}</p>}

        <form onSubmit={invite} className="flex gap-2">
          <input
            type="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="friend@example.com"
            className="input min-w-0 flex-1"
          />
          <button className="btn btn-primary">
            Invite
          </button>
        </form>

        {data && data.invites.length > 0 && (
          <section className="space-y-3">
            <h2 className="section-heading">Invites</h2>
            {data.invites.map((inv) => (
              <div key={inv.id} className="rounded-card border border-line bg-surface p-4">
                <p className="truncate">{inv.email}</p>
                <p className="text-sm text-muted">
                  {new Date(inv.expires_at) < new Date() ? 'Expired' : 'Expires'}{' '}
                  {new Date(inv.expires_at).toLocaleDateString()}
                </p>
                <div className="mt-2 flex gap-2">
                  <button
                    onClick={() => run(() => resendInvite(inv.id))}
                    className="btn btn-secondary btn-sm"
                  >
                    Resend
                  </button>
                  <button
                    onClick={() => run(() => revokeInvite(inv.id))}
                    className="btn btn-danger btn-sm"
                  >
                    Revoke
                  </button>
                </div>
              </div>
            ))}
          </section>
        )}

        <section className="space-y-3">
          <h2 className="section-heading">Users</h2>
          {data?.users.map((u) => (
            <div key={u.id} className="rounded-card border border-line bg-surface p-4">
              <p className="truncate">
                {u.email}
                {u.is_admin && <span className="ml-2 text-sm text-muted">(admin)</span>}
                {u.disabled_at && <span className="ml-2 text-sm text-danger">(disabled)</span>}
              </p>
              <div className="mt-2 flex gap-2">
                <button
                  onClick={() => run(() => setUserDisabled(u.id, !u.disabled_at))}
                  className="btn btn-secondary btn-sm"
                >
                  {u.disabled_at ? 'Enable' : 'Disable'}
                </button>
                {!u.is_admin && (
                  <button
                    onClick={() => run(() => makeAdmin(u.id))}
                    className="btn btn-secondary btn-sm"
                  >
                    Make admin
                  </button>
                )}
              </div>
            </div>
          ))}
        </section>
      </main>
    </div>
  )
}
