import { useEffect, useState, type SubmitEvent } from 'react'
import {
  changeModel,
  createSession,
  getMe,
  getModels,
  postMessage,
  UnauthorizedError,
  type Message,
  type Models,
} from './lib/api'
import { sessionModel } from './lib/sessionModel'
import { sessionNotice } from './lib/sessionNotice'
import { renamedTitle } from './lib/sessionTitle'
import { toolChips } from './lib/toolChips'
import { useSessionStream } from './lib/useSessionStream'
import { ModelPicker } from './ModelPicker'
import { SessionNotice } from './SessionNotice'

type Props = {
  sessionId: string
  // isNew is true right after "New chat" generated sessionId client-side, so
  // the session is known not to exist yet; false when the page opened
  // directly at /s/{id} (a reopen).
  isNew: boolean
  navigate: (to: string) => void
  // listTitle is the chat's title in the Chat List: a placeholder until it is
  // titled, undefined while the list doesn't have the chat.
  listTitle: string | undefined
  // onTitleChanged is called when the stream carries a Title the Chat List
  // doesn't show yet, so the list loads again.
  onTitleChanged: () => void
  // onCreated is called once the chat's first message has created the session.
  onCreated: () => void
  // onUnauthorized is called when the server says the Login Session is gone.
  onUnauthorized: () => void
}

function bubbleText(payload: unknown): string {
  const message = (payload as { message?: Message }).message
  return message?.parts.map((p) => p.text ?? '').join('') ?? ''
}

export function Chat({ sessionId, isNew, navigate, listTitle, onTitleChanged, onCreated, onUnauthorized }: Props) {
  const [started, setStarted] = useState(!isNew)
  const { events, partials, connected, failed } = useSessionStream(sessionId, started)
  // A brand-new chat has a session once its first message creates one; a
  // reopened chat has one once the stream confirms it (streaming's onopen
  // only succeeds once the session exists).
  const hasSession = isNew ? started : connected

  // EventSource cannot see a 401, so a failed stream is checked with /api/me.
  useEffect(() => {
    if (!failed) return
    getMe().catch((err) => {
      if (err instanceof UnauthorizedError) onUnauthorized()
    })
  }, [failed, onUnauthorized])

  const renamed = renamedTitle(events)
  useEffect(() => {
    if (renamed !== undefined && renamed !== listTitle) onTitleChanged()
  }, [renamed, listTitle, onTitleChanged])

  const [models, setModels] = useState<Models | null>(null)
  // picked is the model chosen before a new chat's first message.
  const [picked, setPicked] = useState<string | null>(null)
  const [switching, setSwitching] = useState(false)

  useEffect(() => {
    getModels()
      .then(setModels)
      .catch((err) => {
        if (err instanceof UnauthorizedError) onUnauthorized()
      })
  }, [onUnauthorized])

  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const send = async (e: SubmitEvent) => {
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
        await createSession(sessionId, clientMsgId, text, picked ?? undefined)
        setStarted(true)
        onCreated()
      }
      setDraft('')
    } catch (err) {
      if (err instanceof UnauthorizedError) {
        onUnauthorized()
        return
      }
      setError('Could not send. Try again.')
    } finally {
      setSending(false)
    }
  }

  const chips = new Map(toolChips(events).map((c) => [c.seq, c]))
  const bubbles = events.filter(
    (e) =>
      e.type === 'user.message' ||
      (e.type === 'llm.response' && bubbleText(e.payload) !== '') ||
      chips.has(e.seq),
  )
  const loading = !isNew && !connected && !failed
  const notice = sessionNotice(events)
  const current = hasSession ? sessionModel(events) : (picked ?? models?.default)

  const pick = async (model: string) => {
    if (!hasSession) {
      setPicked(model)
      return
    }
    setSwitching(true)
    setError(null)
    try {
      await changeModel(sessionId, model)
    } catch (err) {
      if (err instanceof UnauthorizedError) {
        onUnauthorized()
        return
      }
      setError('Could not switch model. Try again.')
    } finally {
      setSwitching(false)
    }
  }
  const picker = models && current && (
    <ModelPicker models={models} current={current} onPick={pick} disabled={switching || loading} />
  )

  return (
    <div className="flex min-h-svh flex-1 flex-col">
      <header className="flex items-center justify-between gap-2 border-b border-neutral-800 px-4 py-3">
        <a
          href="/"
          onClick={(e) => {
            e.preventDefault()
            navigate('/')
          }}
          className="text-sm text-neutral-400 hover:text-neutral-200 md:hidden"
        >
          ← Chats
        </a>
        <h1 className="min-w-0 flex-1 truncate font-medium">{renamed ?? listTitle ?? 'New chat'}</h1>
        {picker}
      </header>

      <main className="flex-1 space-y-3 overflow-y-auto px-4 py-6">
        {loading && <p className="text-center text-neutral-500">Loading…</p>}
        {failed && (
          <p className="text-center text-red-400">Could not open this chat.</p>
        )}
        {!loading && !failed && bubbles.length === 0 && (
          <p className="text-center text-neutral-500">Say something to start the chat.</p>
        )}
        {bubbles.map((e) => {
          const chip = chips.get(e.seq)
          if (chip) {
            return (
              <div key={e.seq} className="mr-auto max-w-md rounded-full bg-neutral-900 px-3 py-1 text-sm text-neutral-400">
                {chip.name} · {chip.state}
                {chip.error && <span className="text-red-400"> · {chip.error}</span>}
              </div>
            )
          }
          return (
            <div
              key={e.seq}
              className={`max-w-md rounded-2xl px-4 py-2 whitespace-pre-wrap ${
                e.type === 'user.message' ? 'ml-auto bg-neutral-800' : 'mr-auto bg-neutral-900'
              }`}
            >
              {bubbleText(e.payload)}
            </div>
          )
        })}
        {Object.entries(partials).map(([turnId, text]) => (
          <div
            key={turnId}
            className="mr-auto max-w-md rounded-2xl bg-neutral-900 px-4 py-2 whitespace-pre-wrap"
          >
            {text}
          </div>
        ))}
        {notice && <SessionNotice sessionId={sessionId} notice={notice} picker={picker} onUnauthorized={onUnauthorized} />}
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
