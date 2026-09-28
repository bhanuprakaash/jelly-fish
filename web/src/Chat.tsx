import { useState, type FormEvent } from 'react'
import { createSession, postMessage, type Message } from './lib/api'
import { useSessionStream } from './lib/useSessionStream'

type Props = {
  sessionId: string
  // isNew is true right after "New chat" generated sessionId client-side, so
  // the session is known not to exist yet; false when the page opened
  // directly at /s/{id} (a reopen).
  isNew: boolean
}

function bubbleText(payload: unknown): string {
  const message = (payload as { message?: Message }).message
  return message?.parts.map((p) => p.text ?? '').join('') ?? ''
}

export function Chat({ sessionId, isNew }: Props) {
  const [started, setStarted] = useState(!isNew)
  const { events, connected } = useSessionStream(sessionId, started)
  // A brand-new chat has a session once its first message creates one; a
  // reopened chat has one once the stream confirms it (streaming's onopen
  // only succeeds once the session exists).
  const hasSession = isNew ? started : connected

  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const send = async (e: FormEvent) => {
    e.preventDefault()
    const text = draft.trim()
    if (!text || sending) return

    setSending(true)
    setError(null)
    try {
      const clientMsgId = crypto.randomUUID()
      if (hasSession) {
        await postMessage(sessionId, clientMsgId, text)
      } else {
        await createSession(sessionId, clientMsgId, text)
        setStarted(true)
      }
      setDraft('')
    } catch {
      setError('Could not send. Try again.')
    } finally {
      setSending(false)
    }
  }

  const bubbles = events.filter((e) => e.type === 'user.message')
  const loading = !isNew && !connected

  return (
    <div className="flex min-h-svh flex-col bg-neutral-950 text-neutral-100">
      <header className="border-b border-neutral-800 px-4 py-3">
        <a href="/" className="text-sm text-neutral-400 hover:text-neutral-200">
          ← New chat
        </a>
      </header>

      <main className="flex-1 space-y-3 overflow-y-auto px-4 py-6">
        {loading && <p className="text-center text-neutral-500">Loading…</p>}
        {!loading && bubbles.length === 0 && (
          <p className="text-center text-neutral-500">Say something to start the chat.</p>
        )}
        {bubbles.map((e) => (
          <div
            key={e.seq}
            className="ml-auto max-w-md rounded-2xl bg-neutral-800 px-4 py-2 whitespace-pre-wrap"
          >
            {bubbleText(e.payload)}
          </div>
        ))}
      </main>

      <form onSubmit={send} className="border-t border-neutral-800 p-4">
        {error && <p className="mb-2 text-sm text-red-400">{error}</p>}
        <div className="flex gap-2">
          <input
            className="flex-1 rounded-xl bg-neutral-900 px-4 py-2 outline-none disabled:opacity-50"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="Message"
            disabled={loading}
            autoFocus
          />
          <button
            type="submit"
            disabled={sending || loading || !draft.trim()}
            className="rounded-xl bg-neutral-100 px-4 py-2 font-medium text-neutral-900 disabled:opacity-50"
          >
            Send
          </button>
        </div>
      </form>
    </div>
  )
}
