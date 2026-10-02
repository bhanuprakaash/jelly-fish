import { useCallback, useEffect, useState } from 'react'
import {
  createInvite,
  getAdminUsers,
  HttpError,
  makeAdmin,
  resendInvite,
  revokeInvite,
  setUserDisabled,
  UnauthorizedError,
  type AdminInvite,
  type AdminUser,
} from './lib/api'

type Props = {
  // onSignedOut is called when the server says there is no Login Session.
  onSignedOut: () => void
}

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
export function AdminUsers({ onSignedOut }: Props) {
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
      (err) => {
        if (!current) return
        if (err instanceof UnauthorizedError) onSignedOut()
        else setError('Could not load users. Only admins can see this page.')
      },
    )
    return () => {
      current = false
    }
  }, [reload, onSignedOut])

  const run = useCallback(
    async (action: () => Promise<void>) => {
      setError(null)
      try {
        await action()
        setReload((n) => n + 1)
      } catch (err) {
        if (err instanceof UnauthorizedError) onSignedOut()
        else setError(actionError(err))
      }
    },
    [onSignedOut],
  )

  const invite = (e: React.FormEvent) => {
    e.preventDefault()
    void run(async () => {
      await createInvite(email)
      setEmail('')
    })
  }

  return (
    <div className="min-h-svh bg-neutral-950 text-neutral-100">
      <header className="border-b border-neutral-800 px-4 py-3">
        <a href="/settings" className="text-sm text-neutral-400 hover:text-neutral-200">
          ← Back
        </a>
      </header>
      <main className="mx-auto max-w-md space-y-6 px-4 py-6">
        <h1 className="text-2xl font-semibold">Users</h1>
        {error && <p className="text-sm text-red-400">{error}</p>}

        <form onSubmit={invite} className="flex gap-2">
          <input
            type="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="friend@example.com"
            className="min-w-0 flex-1 rounded-xl bg-neutral-900 px-4 py-2"
          />
          <button className="rounded-xl bg-neutral-100 px-4 py-2 font-medium text-neutral-900">
            Invite
          </button>
        </form>

        {data && data.invites.length > 0 && (
          <section className="space-y-3">
            <h2 className="text-lg font-medium">Invites</h2>
            {data.invites.map((inv) => (
              <div key={inv.id} className="rounded-xl bg-neutral-900 p-4">
                <p className="truncate">{inv.email}</p>
                <p className="text-sm text-neutral-500">
                  {new Date(inv.expires_at) < new Date() ? 'Expired' : 'Expires'}{' '}
                  {new Date(inv.expires_at).toLocaleDateString()}
                </p>
                <div className="mt-2 flex gap-2">
                  <button
                    onClick={() => run(() => resendInvite(inv.id))}
                    className="rounded-xl bg-neutral-800 px-4 py-2 text-sm"
                  >
                    Resend
                  </button>
                  <button
                    onClick={() => run(() => revokeInvite(inv.id))}
                    className="rounded-xl bg-neutral-800 px-4 py-2 text-sm"
                  >
                    Revoke
                  </button>
                </div>
              </div>
            ))}
          </section>
        )}

        <section className="space-y-3">
          <h2 className="text-lg font-medium">Users</h2>
          {data?.users.map((u) => (
            <div key={u.id} className="rounded-xl bg-neutral-900 p-4">
              <p className="truncate">
                {u.email}
                {u.is_admin && <span className="ml-2 text-sm text-neutral-400">(admin)</span>}
                {u.disabled_at && <span className="ml-2 text-sm text-red-400">(disabled)</span>}
              </p>
              <div className="mt-2 flex gap-2">
                <button
                  onClick={() => run(() => setUserDisabled(u.id, !u.disabled_at))}
                  className="rounded-xl bg-neutral-800 px-4 py-2 text-sm"
                >
                  {u.disabled_at ? 'Enable' : 'Disable'}
                </button>
                {!u.is_admin && (
                  <button
                    onClick={() => run(() => makeAdmin(u.id))}
                    className="rounded-xl bg-neutral-800 px-4 py-2 text-sm"
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
