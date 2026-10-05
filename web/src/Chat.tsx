import { Fragment, useCallback, useEffect, useRef, useState, type KeyboardEvent, type Ref, type SubmitEvent } from 'react'
import {
  changeModel,
  createSession,
  getMe,
  getMemories,
  getModels,
  interruptSession,
  postMessage,
  UnauthorizedError,
  type Memory,
  type Message,
  type Models,
  type UIEvent,
} from './lib/api'
import { JellyGlyph, type JellyStatus } from './JellyGlyph'
import { sessionModel } from './lib/sessionModel'
import { sessionNotice } from './lib/sessionNotice'
import { renamedTitle } from './lib/sessionTitle'
import { thinkingText, toolChips, type ToolChip } from './lib/toolChips'
import { useSessionStream } from './lib/useSessionStream'
import { MemoryChip } from './MemoryChip'
import { ModelPicker } from './ModelPicker'
import { SessionNotice } from './SessionNotice'
import { Thought, ToolCalls } from './ToolCalls'

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

const statusLabel: Record<JellyStatus, string> = {
  runnable: 'Working…',
  running: 'Working…',
  awaiting_approval: 'Needs approval',
  awaiting_children: 'Waiting…',
  sleeping: 'Sleeping',
  awaiting_user: 'Your turn',
  completed: 'Done',
  failed: 'Failed',
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
  const [active, setActive] = useState(0)
  // closedAt is the draft the slash menu was dismissed at; typing opens it again.
  const [closedAt, setClosedAt] = useState<string | null>(null)
  const [stopping, setStopping] = useState(false)
  const modelRef = useRef<HTMLSelectElement>(null)
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
      (e.type === 'llm.response' && (bubbleText(e.payload) !== '' || thinkingText(e.payload) !== '')) ||
      chips.has(e.seq),
  )
  // Consecutive tool calls share one card.
  const transcript: (UIEvent | ToolChip[])[] = []
  for (const e of bubbles) {
    const chip = chips.get(e.seq)
    if (!chip || chip.memory) {
      transcript.push(e)
      continue
    }
    const last = transcript.at(-1)
    if (Array.isArray(last)) last.push(chip)
    else transcript.push([chip])
  }
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
  const renderPicker = (ref?: Ref<HTMLSelectElement>) =>
    models && current && (
      <ModelPicker ref={ref} models={models} current={current} onPick={pick} disabled={switching || loading} />
    )
  const picker = renderPicker()

  let status: JellyStatus = 'runnable'
  for (const e of events) {
    if (e.type === 'session.status_changed') status = (e.payload as { to: JellyStatus }).to
  }
  const canStop = hasSession && ['runnable', 'running', 'sleeping'].includes(status)
  const steering = hasSession && ['runnable', 'running'].includes(status)
  const created = events.find((e) => e.type === 'session.created')
  const agentName = (created?.payload as { agent?: { name?: string } } | undefined)?.agent?.name

  const stop = async () => {
    setStopping(true)
    setError(null)
    try {
      await interruptSession(sessionId)
    } catch (err) {
      if (err instanceof UnauthorizedError) {
        onUnauthorized()
        return
      }
      setError('Could not stop. Try again.')
    } finally {
      setStopping(false)
    }
  }

  const commands = [
    ...(picker ? [{ name: '/model', hint: 'Switch model' }] : []),
    ...(canStop ? [{ name: '/stop', hint: 'Interrupt this chat' }] : []),
  ]
  const typed = /^\/\S*$/.test(draft) ? draft.toLowerCase() : null
  const matches = typed === null ? [] : commands.filter((c) => c.name.startsWith(typed))
  const slashOpen = matches.length > 0 && closedAt !== draft
  const activeIndex = Math.min(active, matches.length - 1)

  const runCommand = (name: string) => {
    setDraft('')
    if (name === '/stop') {
      void stop()
      return
    }
    modelRef.current?.focus()
    modelRef.current?.showPicker()
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (!slashOpen) return
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((activeIndex + (e.key === 'ArrowDown' ? 1 : matches.length - 1)) % matches.length)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      runCommand(matches[activeIndex].name)
    } else if (e.key === 'Escape') {
      setClosedAt(draft)
    }
  }

  return (
    <div className="flex min-h-svh flex-1 flex-col">
      <header className="flex flex-col gap-1 border-b border-line px-4 py-3">
        <div className="flex items-center gap-2">
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
          {hasSession && (
            <span
              className={`inline-flex items-center gap-2 rounded-full bg-sunk py-0.5 pr-3 pl-1 text-sm ${
                status === 'failed' ? 'text-danger' : 'text-ink2'
              }`}
            >
              <JellyGlyph status={status} size={12} />
              {statusLabel[status]}
            </span>
          )}
          {isIncognito && (
            <span className="rounded-full bg-sunk px-2 py-0.5 text-xs text-ink2">Incognito</span>
          )}
          {canStop && (
            <button type="button" onClick={stop} disabled={stopping} className="btn btn-danger btn-sm">
              Stop
            </button>
          )}
        </div>
        {(agentName || picker) && (
          <div className="flex flex-wrap items-center gap-x-2 text-sm text-muted">
            {agentName && <span>{agentName}</span>}
            {agentName && picker && <span aria-hidden="true">·</span>}
            {renderPicker(modelRef)}
          </div>
        )}
      </header>

      <main className="flex-1 space-y-3 overflow-y-auto px-4 py-6">
        {loading && <p className="text-center text-muted">Loading…</p>}
        {failed && (
          <p className="text-center text-danger">Could not open this chat.</p>
        )}
        {!loading && !failed && bubbles.length === 0 && (
          <p className="text-center text-muted">Say something to start the chat.</p>
        )}
        {transcript.map((item) => {
          if (Array.isArray(item)) return <ToolCalls key={item[0].seq} calls={item} />
          const e = item
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
          const text = bubbleText(e.payload)
          const thought = e.type === 'llm.response' ? thinkingText(e.payload) : ''
          return (
            <Fragment key={e.seq}>
              {thought && <Thought text={thought} />}
              {text && (
                <div
                  className={`max-w-md whitespace-pre-wrap ${
                    e.type === 'user.message' ? 'ml-auto rounded-card bg-accent-tint px-4 py-2' : 'mr-auto'
                  }`}
                >
                  {text}
                </div>
              )}
            </Fragment>
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
            <input
              type="checkbox"
              className="accent-accent-ink"
              checked={incognito}
              onChange={(e) => setIncognito(e.target.checked)}
            />
            Incognito: no memory in this chat
          </label>
        )}
        <div className="overflow-hidden rounded-panel border border-accent-line bg-surface">
          {slashOpen && (
            <div
              id="slash-menu"
              role="listbox"
              aria-label="Slash commands"
              className="flex flex-col gap-0.5 border-b border-line bg-bg p-2"
            >
              {matches.map((c, i) => (
                <div
                  key={c.name}
                  id={`slash-${c.name.slice(1)}`}
                  role="option"
                  aria-selected={i === activeIndex}
                  tabIndex={-1}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => runCommand(c.name)}
                  className={`flex cursor-pointer flex-col rounded-btn px-3 py-2 ${i === activeIndex ? 'bg-accent-tint' : ''}`}
                >
                  <code className="font-mono text-sm text-ink">{c.name}</code>
                  <span className="text-xs text-muted">{c.hint}</span>
                </div>
              ))}
            </div>
          )}
          <div className="flex items-center gap-2 p-2">
            <input
              role="combobox"
              aria-label="Message"
              aria-autocomplete="list"
              aria-expanded={slashOpen}
              aria-controls={slashOpen ? 'slash-menu' : undefined}
              aria-activedescendant={slashOpen ? `slash-${matches[activeIndex].name.slice(1)}` : undefined}
              className="input min-w-0 flex-1 border-0 bg-transparent"
              value={draft}
              onChange={(e) => {
                setDraft(e.target.value)
                setActive(0)
              }}
              onKeyDown={onKeyDown}
              placeholder="Message, or type / for commands"
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
        </div>
        {steering && <p className="px-2 pt-2 text-xs text-muted">Running · your message steers it</p>}
      </form>
    </div>
  )
}
