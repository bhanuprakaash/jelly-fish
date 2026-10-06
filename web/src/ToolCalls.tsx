import { useEffect, useState } from 'react'
import type { ToolChip } from './lib/toolChips'

// Thought is the collapsed pill of a model's reasoning; it opens to the text.
export function Thought({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="mr-auto space-y-2">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className={`inline-flex min-h-8 items-center gap-2 rounded-full border border-line bg-sunk px-3 text-sm text-muted ${ring}`}
      >
        <svg
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          aria-hidden="true"
          className={open ? 'rotate-90' : ''}
        >
          <path d="M9 6l6 6-6 6" />
        </svg>
        Thought
      </button>
      {open && <p className="whitespace-pre-wrap text-sm text-muted">{text}</p>}
    </div>
  )
}

const ring = 'focus-visible:outline-none focus-visible:shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent-ink)]'
const insetRing =
  'focus-visible:outline-none focus-visible:shadow-[inset_0_0_0_2px_var(--bg),inset_0_0_0_4px_var(--accent-ink)]'

const stateWords = {
  running: 'running',
  done: 'done',
  failed: 'failed',
  stopped: 'stopped',
  unknown: 'outcome unknown',
}

const dangerTint = 'bg-danger-tint text-danger'

const stateCircles = {
  running: 'bg-accent-tint text-accent-ink',
  done: 'bg-success-tint text-success',
  failed: dangerTint,
  stopped: dangerTint,
  unknown: 'bg-sunk text-muted',
}

// argSummary is the first argument's value on one line, for the row's muted text.
function argSummary(args: unknown): string {
  if (typeof args !== 'object' || args === null) return ''
  const first = Object.values(args)[0]
  if (first === undefined) return ''
  return typeof first === 'string' ? first : JSON.stringify(first)
}

// useElapsedSeconds ticks every second while active.
function useElapsedSeconds(since: number, active: boolean): number {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (!active) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [active])
  return Math.max(0, Math.floor((now - since) / 1000))
}

function duration(ms: number): string {
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

function StateIcon({ state }: { state: ToolChip['state'] }) {
  const common = {
    width: 12,
    height: 12,
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 2.5,
    strokeLinecap: 'round' as const,
    'aria-hidden': true,
  }
  switch (state) {
    case 'running':
      return (
        <span
          aria-hidden="true"
          className="size-3 rounded-full border-2 border-accent-line border-t-accent-ink motion-safe:animate-spin"
        />
      )
    case 'done':
      return (
        <svg {...common}>
          <path d="M5 12l5 5 9-10" />
        </svg>
      )
    case 'unknown':
      return <span aria-hidden="true" className="text-sm font-semibold leading-none">?</span>
    default:
      return (
        <svg {...common}>
          <path d="M6 6l12 12M18 6L6 18" />
        </svg>
      )
  }
}

const codeClass =
  'overflow-x-auto whitespace-pre-wrap break-words rounded-btn border border-line bg-sunk p-3 font-mono text-xs text-ink2'

function Row({ call, first }: { call: ToolChip; first: boolean }) {
  const [open, setOpen] = useState(false)
  const args = call.args === undefined || call.args === null ? '' : JSON.stringify(call.args, null, 2)
  const elapsed = useElapsedSeconds(call.startedAt, call.state === 'running')
  const summary = argSummary(call.args)
  const note =
    call.state === 'running'
      ? `running · ${elapsed}s`
      : call.state === 'unknown'
        ? 'outcome unknown — check before retrying'
        : call.error
  const noteTone = call.state === 'failed' || call.state === 'stopped' ? 'text-danger' : 'text-muted'
  return (
    <div className={first ? '' : 'border-t border-line'}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className={`flex w-full items-start gap-3 px-4 py-3 text-left text-sm ${insetRing}`}
      >
        <span className={`mt-px flex size-5 shrink-0 items-center justify-center rounded-full ${stateCircles[call.state]}`}>
          <StateIcon state={call.state} />
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex min-w-0 gap-2">
            <span className="shrink-0 font-mono text-ink">{call.name}</span>
            <span className="sr-only">{stateWords[call.state]}</span>
            {summary && <span className="truncate text-muted">{summary}</span>}
          </span>
          {note && <span className={`line-clamp-2 text-[12.5px] ${noteTone}`}>{note}</span>}
        </span>
        {call.durationMs !== undefined && call.durationMs > 0 && (
          <span className="shrink-0 text-xs text-muted">{duration(call.durationMs)}</span>
        )}
      </button>
      {open && (
        <div className="space-y-2 px-4 pb-3">
          {args && <pre className={codeClass}>{args}</pre>}
          {call.result && <pre className={codeClass}>{call.result}</pre>}
        </div>
      )}
    </div>
  )
}

// ToolCalls is the card of one turn's tool calls: a row each, which opens to
// the call's arguments and result.
export function ToolCalls({ calls }: { calls: ToolChip[] }) {
  return (
    <div className="w-full overflow-hidden rounded-card border border-line bg-surface">
      {calls.map((call, i) => (
        <Row key={call.seq} call={call} first={i === 0} />
      ))}
    </div>
  )
}
