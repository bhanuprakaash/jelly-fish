import { Fragment, useCallback, useEffect, useRef, useState, type KeyboardEvent, type Ref, type SubmitEvent } from 'react'
import {
  changeModel,
  createSession,
  getMe,
  getMemories,
  getModels,
  interruptSession,
  postMessage,
  type Memory,
  type Message,
  type Models,
  type UIEvent,
} from './lib/api'
import { JellyGlyph } from './JellyGlyph'
import { providerOf, sessionModel } from './lib/sessionModel'
import { sessionNotice } from './lib/sessionNotice'
import { getShowThinking, setShowThinking } from './lib/showThinking'
import { renamedTitle } from './lib/sessionTitle'
import { statusChipTone, statusLabel, type JellyStatus } from './lib/status'
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
}

function bubbleText(payload: unknown): string {
  const message = (payload as { message?: Message }).message
  return message?.parts.map((p) => p.text ?? '').join('') ?? ''
}

export function Chat({ sessionId, isNew, navigate, listTitle, listIncognito, onTitleChanged, onCreated }: Props) {
  const [started, setStarted] = useState(!isNew)
  const { events, partials, thoughts, connected, failed } = useSessionStream(sessionId, started)
  const [showThinking, setShowThinkingState] = useState(getShowThinking)
  // A brand-new chat has a session once its first message creates one; a
  // reopened chat has one once the stream confirms it (streaming's onopen
  // only succeeds once the session exists).
  const hasSession = isNew ? started : connected

  // EventSource cannot see a 401, so a failed stream is checked with /api/me.
  useEffect(() => {
    if (!failed) return
    getMe().catch(() => {})
  }, [failed])

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
      .catch(() => {})
  }, [])

  const [draft, setDraft] = useState('')
  const [active, setActive] = useState(0)
  // closedAt is the draft the slash menu was dismissed at; typing opens it again.
  const [closedAt, setClosedAt] = useState<string | null>(null)
  const [stopping, setStopping] = useState(false)
  const modelRef = useRef<HTMLButtonElement>(null)
  const transcriptRef = useRef<HTMLElement>(null)
  const transcriptBody = useRef<HTMLDivElement>(null)
  // following is whether the transcript is scrolled to its end, so new messages scroll into view.
  const following = useRef(true)
  const lastTop = useRef(0)
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
      following.current = true
      setDraft('')
    } catch {
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
    getMemories().then((d) => setMemories(d.memories), () => {})
  }, [])
  const memoryChips = [...chips.values()].filter((c) => c.memory).length
  useEffect(() => {
    if (memoryChips > 0) loadMemories()
  }, [memoryChips, loadMemories])
  const bubbles = events.filter(
    (e) =>
      e.type === 'user.message' ||
      (e.type === 'llm.response' &&
        (bubbleText(e.payload) !== '' || (showThinking && thinkingText(e.payload) !== ''))) ||
      chips.has(e.seq),
  )
  const transcript: (UIEvent | { turn: string; calls: ToolChip[] })[] = []
  const cards = new Map<string, ToolChip[]>()
  for (const e of bubbles) {
    const chip = chips.get(e.seq)
    if (!chip || chip.memory) {
      transcript.push(e)
      continue
    }
    const card = cards.get(chip.turn)
    if (card) {
      card.push(chip)
    } else {
      const calls = [chip]
      cards.set(chip.turn, calls)
      transcript.push({ turn: chip.turn, calls })
    }
  }
  // Growth with no scroll event (late layout, streamed text) must not end following.
  useEffect(() => {
    const el = transcriptRef.current
    const body = transcriptBody.current
    if (!el || !body) return
    const observer = new ResizeObserver(() => {
      if (following.current) el.scrollTop = el.scrollHeight
    })
    observer.observe(body)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])
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
    } catch {
      setError('Could not switch model. Try again.')
    } finally {
      setSwitching(false)
    }
  }
  const renderPicker = (ref?: Ref<HTMLButtonElement>) =>
    models && current && (
      <ModelPicker ref={ref} models={models} current={current} onPick={pick} disabled={switching || loading} />
    )
  const picker = renderPicker()

  let status: JellyStatus | undefined
  for (const e of events) {
    if (e.type === 'session.status_changed') status = (e.payload as { to: JellyStatus }).to
  }
  const canStop = hasSession && status === 'running'
  const steering = hasSession && (status === 'runnable' || status === 'running')
  const created = events.find((e) => e.type === 'session.created')
  const agentName = (created?.payload as { agent?: { name?: string } } | undefined)?.agent?.name

  const stop = async () => {
    setStopping(true)
    setError(null)
    try {
      await interruptSession(sessionId)
    } catch {
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
    modelRef.current?.click()
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
    <div className="flex h-svh flex-col">
      <header className="flex flex-col gap-2.5 border-b border-line bg-surface px-3 py-2 md:px-10 md:py-5">
        <div className="flex items-center gap-1 md:gap-2.5">
          <a
            href="/"
            aria-label="Back to chats"
            onClick={(e) => {
              e.preventDefault()
              navigate('/')
            }}
            className="inline-flex size-11 shrink-0 items-center justify-center rounded-btn text-ink focus-ring md:hidden"
          >
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
              <path d="M15 5l-7 7 7 7" />
            </svg>
          </a>
          <div className="flex min-w-0 flex-1 flex-col items-start md:flex-row md:items-center md:gap-3">
            <h1 className="max-w-full truncate font-medium md:flex-1">{renamed ?? listTitle ?? 'New chat'}</h1>
            {hasSession && status && (
              <span
                className={`inline-flex shrink-0 items-center gap-2 rounded-full py-0.5 pr-3 pl-1 text-xs md:text-sm ${statusChipTone[status]}`}
              >
                <JellyGlyph status={status} size={12} />
                {statusLabel[status]}
              </span>
            )}
          </div>
          {isIncognito && (
            <span className="shrink-0 rounded-full bg-sunk px-2 py-0.5 text-xs text-ink2">Incognito</span>
          )}
          {canStop && (
            <button type="button" onClick={stop} disabled={stopping} className="btn btn-danger btn-sm shrink-0">
              Stop
            </button>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-x-2 text-sm text-muted">
          {agentName && <span>{agentName}</span>}
          {agentName && picker && <span aria-hidden="true">·</span>}
          {renderPicker(modelRef)}
          <button
            type="button"
            role="switch"
            aria-checked={showThinking}
            onClick={() => {
              setShowThinking(!showThinking)
              setShowThinkingState(!showThinking)
            }}
            className="group ml-auto inline-flex min-h-8 items-center gap-2.5 text-sm text-ink2 focus-visible:outline-none"
          >
            Show thinking
            <span
              className={`flex h-6.5 w-11 rounded-full p-[3px] group-focus-visible:shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent-ink)] ${
                showThinking ? 'justify-end bg-accent-ink' : 'justify-start bg-sunk ring-1 ring-ink2/60 ring-inset'
              }`}
            >
              <span className={`size-5 rounded-full ${showThinking ? 'bg-surface' : 'bg-muted'}`} />
            </span>
          </button>
        </div>
      </header>

      <main
        ref={transcriptRef}
        onScroll={(e) => {
          const el = e.currentTarget
          if (el.scrollHeight - el.scrollTop - el.clientHeight < 80) following.current = true
          else if (el.scrollTop < lastTop.current) following.current = false
          lastTop.current = el.scrollTop
        }}
        className="relative min-h-0 flex-1 overflow-y-auto"
      >
        <div ref={transcriptBody} className="mx-auto flex w-full max-w-[780px] flex-col gap-6 px-4 py-6 md:px-10">
          {loading && <p className="text-center text-muted">Loading…</p>}
          {failed && (
            <p className="text-center text-danger">Could not open this chat.</p>
          )}
          {!loading && !failed && bubbles.length === 0 && (
            <p className="text-center text-muted">Say something to start the chat.</p>
          )}
          {transcript.map((item) => {
            if ('calls' in item) return <ToolCalls key={item.turn} calls={item.calls} />
            const e = item
            const chip = chips.get(e.seq)
            if (chip?.memory) {
              return (
                <MemoryChip
                  key={e.seq}
                  chip={{ ...chip, memory: chip.memory }}
                  memories={memories}
                  onChanged={loadMemories}
                />
              )
            }
            const text = bubbleText(e.payload)
            const thought = showThinking && e.type === 'llm.response' ? thinkingText(e.payload) : ''
            return (
              <Fragment key={e.seq}>
                {thought && <Thought text={thought} />}
                {text && (
                  <div
                    className={`whitespace-pre-wrap [overflow-wrap:anywhere] ${
                      e.type === 'user.message'
                        ? 'ml-auto max-w-[82%] rounded-[20px_20px_6px_20px] border border-accent-line bg-accent-tint px-4 py-3'
                        : 'mr-auto'
                    }`}
                  >
                    {text}
                  </div>
                )}
              </Fragment>
            )
          })}
          {[...new Set([...Object.keys(thoughts), ...Object.keys(partials)])].map((turnId) => (
            <Fragment key={turnId}>
              {showThinking && thoughts[turnId] && <Thought text={thoughts[turnId]} streaming={!(turnId in partials)} />}
              {partials[turnId] && (
                <div className="mr-auto whitespace-pre-wrap [overflow-wrap:anywhere]">{partials[turnId]}</div>
              )}
            </Fragment>
          ))}
          {notice && <SessionNotice sessionId={sessionId} notice={notice} provider={models && current ? providerOf(models, current) : undefined} picker={picker} />}
        </div>
      </main>

      <form onSubmit={send} className="mx-auto w-full max-w-[780px] shrink-0 p-4 md:px-10 md:pb-6">
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
        <div className="overflow-hidden rounded-panel border border-accent-line bg-surface has-[input:focus-visible]:shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent-ink)]">
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
              className="input min-w-0 flex-1 border-0 bg-transparent focus-visible:shadow-none"
              value={draft}
              onChange={(e) => {
                setDraft(e.target.value)
                setActive(0)
                if (e.target.value !== closedAt) setClosedAt(null)
              }}
              onKeyDown={onKeyDown}
              placeholder="Message, or type / for commands"
              disabled={loading}
              autoFocus
            />
            <button
              type="submit"
              disabled={sending || loading || !draft.trim()}
              aria-label="Send"
              className="btn btn-primary size-10 shrink-0 rounded-full p-0"
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
                <path d="M12 19V5M6 11l6-6 6 6" />
              </svg>
            </button>
          </div>
        </div>
        {steering && (
          <p className="px-2 pt-2 text-xs text-muted">
            Running · a message now steers at the next tool result.
            {canStop && (
              <>
                {' '}
                <button
                  type="button"
                  onClick={stop}
                  disabled={stopping}
                  className="cursor-pointer rounded-sm font-semibold text-ink2 underline underline-offset-2 hover:text-ink focus-ring disabled:opacity-50"
                >
                  Interrupt
                </button>
              </>
            )}
          </p>
        )}
      </form>
    </div>
  )
}
