import { useEffect, useState } from 'react'
import { Chat } from './Chat'

const sessionPath = /^\/s\/([0-9a-f-]{36})$/i

function App() {
  const [path, setPath] = useState(window.location.pathname)
  const [justCreated, setJustCreated] = useState(false)

  useEffect(() => {
    const onPopState = () => {
      setPath(window.location.pathname)
      setJustCreated(false)
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  const match = sessionPath.exec(path)
  if (match) {
    return <Chat key={match[1]} sessionId={match[1]} isNew={justCreated} />
  }

  const startChat = () => {
    const id = crypto.randomUUID()
    window.history.pushState({}, '', `/s/${id}`)
    setJustCreated(true)
    setPath(`/s/${id}`)
  }

  return (
    <main className="flex min-h-svh items-center justify-center bg-neutral-950 px-4 text-neutral-100">
      <div className="w-full max-w-sm rounded-2xl bg-neutral-900 p-8 text-center shadow-xl sm:max-w-md">
        <h1 className="text-2xl font-semibold sm:text-3xl">Jelly-fish</h1>
        <button
          onClick={startChat}
          className="mt-6 rounded-xl bg-neutral-100 px-6 py-3 font-medium text-neutral-900"
        >
          New chat
        </button>
      </div>
    </main>
  )
}

export default App
