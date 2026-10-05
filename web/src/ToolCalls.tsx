import { useState } from 'react'
import type { ToolChip } from './lib/toolChips'

// Thought is the collapsed pill of a model's reasoning; it opens to the text.
export function Thought({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="mr-auto max-w-md space-y-2">
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

const stateColors = {
  running: 'text-accent-ink',
  done: 'text-success',
  failed: 'text-danger',
  stopped: 'text-danger',
  unknown: 'text-muted',
}

function duration(ms: number): string {
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

function StateIcon({ state }: { state: ToolChip['state'] }) {
  const common = {
    width: 14,
    height: 14,
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
          className="size-3.5 rounded-full border-2 border-line2 border-t-accent-ink motion-safe:animate-spin"
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
  return (
    <div className={first ? '' : 'border-t border-line'}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className={`flex w-full items-center gap-3 px-4 py-3 text-left text-sm ${insetRing}`}
      >
        <span className={`flex size-5 shrink-0 items-center justify-center ${stateColors[call.state]}`}>
          <StateIcon state={call.state} />
        </span>
        <span className="font-mono text-ink">{call.name}</span>
        <span className="sr-only">{stateWords[call.state]}</span>
        {call.error && <span className="min-w-0 flex-1 truncate text-muted">{call.error}</span>}
        {!call.error && <span className="flex-1" />}
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
    <div className="mr-auto w-full max-w-md overflow-hidden rounded-card border border-line bg-surface">
      {calls.map((call, i) => (
        <Row key={call.seq} call={call} first={i === 0} />
      ))}
    </div>
  )
}
