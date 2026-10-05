import { useCallback, useEffect, useState, type SubmitEvent } from 'react'
import {
  changeModel,
  createSession,
  getMe,
  getMemories,
  getModels,
  postMessage,
  UnauthorizedError,
  type Memory,
  type Message,
  type Models,
} from './lib/api'
import { sessionModel } from './lib/sessionModel'
import { sessionNotice } from './lib/sessionNotice'
import { renamedTitle } from './lib/sessionTitle'
import { toolChips } from './lib/toolChips'
import { useSessionStream } from './lib/useSessionStream'
import { MemoryChip } from './MemoryChip'
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
  // listIncognito is whether the Chat List says the chat is incognito.
  listIncognito: boolean | undefined
  // onTitleChanged is called when the stream carries a Title the Chat List
  // doesn't show yet, so the list loads again.
  onTitleChanged: () => void
  // onCreated is called once the chat's first message has created the session.
  onCreated: () => void
  // onUnauthorized is called when the server says the Login Session is gone.
  onUnauthorized: () => void
}

const chipColors = {
  running: 'text-accent-ink',
  done: 'text-success',
  failed: 'text-danger',
  interrupted: 'text-muted',
}

function bubbleText(payload: unknown): string {
  const message = (payload as { message?: Message }).message
  return message?.parts.map((p) => p.text ?? '').join('') ?? ''
}

export function Chat({ sessionId, isNew, navigate, listTitle, listIncognito, onTitleChanged, onCreated, onUnauthorized }: Props) {
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
  const [incognito, setIncognito] = useState(false)

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
        await createSession(sessionId, clientMsgId, text, picked ?? undefined, incognito)
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

  const isIncognito = incognito || listIncognito === true
  const chips = new Map(
    toolChips(events)
      .filter((c) => !(isIncognito && c.name === 'memory'))
      .map((c) => [c.seq, c]),
  )
  const [memories, setMemories] = useState<Memory[] | null>(null)
  const loadMemories = useCallback(() => {
    getMemories().then(
      (d) => setMemories(d.memories),
      (err) => {
        if (err instanceof UnauthorizedError) onUnauthorized()
      },
    )
  }, [onUnauthorized])
  const memoryChips = [...chips.values()].filter((c) => c.memory).length
  useEffect(() => {
    if (memoryChips > 0) loadMemories()
  }, [memoryChips, loadMemories])
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
      <header className="flex items-center justify-between gap-2 border-b border-line px-4 py-3">
        <a
          href="/"
          onClick={(e) => {
            e.preventDefault()
            navigate('/')
          }}
          className="text-sm text-muted hover:text-ink md:hidden"
        >
          ← Chats
        </a>
        <h1 className="min-w-0 flex-1 truncate font-medium">{renamed ?? listTitle ?? 'New chat'}</h1>
        {isIncognito && (
          <span className="rounded-full bg-sunk px-2 py-0.5 text-xs text-ink2">Incognito</span>
        )}
        {picker}
      </header>

      <main className="flex-1 space-y-3 overflow-y-auto px-4 py-6">
        {loading && <p className="text-center text-muted">Loading…</p>}
        {failed && (
          <p className="text-center text-danger">Could not open this chat.</p>
        )}
        {!loading && !failed && bubbles.length === 0 && (
          <p className="text-center text-muted">Say something to start the chat.</p>
        )}
        {bubbles.map((e) => {
          const chip = chips.get(e.seq)
          if (chip?.memory) {
            return (
              <MemoryChip
                key={e.seq}
                chip={{ ...chip, memory: chip.memory }}
                memories={memories}
                onChanged={loadMemories}
                onUnauthorized={onUnauthorized}
              />
            )
          }
          if (chip) {
            return (
              <div
                key={e.seq}
                className={`mr-auto flex max-w-md items-center gap-2 rounded-full border border-line bg-surface px-3 py-1 text-sm ${chipColors[chip.state]}`}
              >
                {chip.state === 'running' && (
                  <span
                    aria-hidden="true"
                    className="size-3 shrink-0 rounded-full border-2 border-line2 border-t-accent-ink motion-safe:animate-spin"
                  />
                )}
                <span>
                  <span className="font-mono">{chip.name}</span> · {chip.state}
                  {chip.error && <span className="text-danger"> · {chip.error}</span>}
                </span>
              </div>
            )
          }
          return (
            <div
              key={e.seq}
              className={`max-w-md whitespace-pre-wrap ${
                e.type === 'user.message' ? 'ml-auto rounded-card bg-accent-tint px-4 py-2' : 'mr-auto'
              }`}
            >
              {bubbleText(e.payload)}
            </div>
          )
        })}
        {Object.entries(partials).map(([turnId, text]) => (
          <div key={turnId} className="mr-auto max-w-md whitespace-pre-wrap">
            {text}
          </div>
        ))}
        {notice && <SessionNotice sessionId={sessionId} notice={notice} picker={picker} onUnauthorized={onUnauthorized} />}
      </main>

      <form onSubmit={send} className="border-t border-line p-4">
        {error && <p className="mb-2 text-sm text-danger">{error}</p>}
        {!hasSession && (
          <label className="mb-2 flex items-center gap-2 text-sm text-muted">
            <input type="checkbox" checked={incognito} onChange={(e) => setIncognito(e.target.checked)} />
            Incognito: no memory in this chat
          </label>
        )}
        <div className="flex gap-2">
          <input
            className="input flex-1 rounded-card border-accent-line"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="Message"
            disabled={loading}
            autoFocus
          />
          <button
            type="submit"
            disabled={sending || loading || !draft.trim()}
            className="btn btn-primary"
          >
            Send
          </button>
        </div>
      </form>
    </div>
  )
}
