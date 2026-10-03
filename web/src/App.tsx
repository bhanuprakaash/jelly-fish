import { useCallback, useEffect, useState } from 'react'
import { AdminUsers } from './AdminUsers'
import { LinkSignIn } from './LinkSignIn'
import { Login } from './Login'
import { Settings } from './Settings'
import { Shell } from './Shell'
import { getMe, UnauthorizedError } from './lib/api'

const sessionPath = /^\/s\/([0-9a-f-]{36})$/i

type SignIn = 'checking' | 'signed-in' | 'signed-out' | 'error'

function App() {
  const [signIn, setSignIn] = useState<SignIn>('checking')
  const [path, setPath] = useState(window.location.pathname)
  const [justCreated, setJustCreated] = useState(false)

  const unauthorized = useCallback(() => setSignIn('signed-out'), [])

  const signedOut = useCallback(() => {
    window.history.replaceState({}, '', '/')
    setPath('/')
    setSignIn('signed-out')
  }, [])

  useEffect(() => {
    getMe().then(
      () => setSignIn('signed-in'),
      (err) => setSignIn(err instanceof UnauthorizedError ? 'signed-out' : 'error'),
    )
  }, [])

  useEffect(() => {
    const onPopState = () => {
      setPath(window.location.pathname)
      setJustCreated(false)
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  if (path === '/auth/link') {
    const token = new URLSearchParams(window.location.search).get('t')
    if (token) {
      return (
        <LinkSignIn
          token={token}
          onSignedIn={() => {
            setSignIn('signed-in')
            setPath('/')
          }}
        />
      )
    }
  }

  if (signIn === 'checking') return null
  if (signIn === 'error') {
    return (
      <main className="flex min-h-svh items-center justify-center bg-neutral-950 text-red-400">
        Could not reach the server.
      </main>
    )
  }
  if (signIn === 'signed-out') {
    return <Login onSignedIn={() => setSignIn('signed-in')} />
  }

  if (path === '/settings') return <Settings onSignedOut={signedOut} />
  if (path === '/admin/users') return <AdminUsers onSignedOut={signedOut} />

  const navigate = (to: string) => {
    window.history.pushState({}, '', to)
    setPath(to)
    setJustCreated(false)
  }

  const startChat = () => {
    navigate(`/s/${crypto.randomUUID()}`)
    setJustCreated(true)
  }

  return (
    <Shell
      sessionId={sessionPath.exec(path)?.[1] ?? null}
      isNew={justCreated}
      navigate={navigate}
      onNewChat={startChat}
      onUnauthorized={unauthorized}
    />
  )
}

export default App
