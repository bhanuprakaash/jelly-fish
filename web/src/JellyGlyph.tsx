// JellyStatus is a session status the glyph can show.
export type JellyStatus =
  | 'runnable'
  | 'running'
  | 'awaiting_approval'
  | 'awaiting_children'
  | 'sleeping'
  | 'awaiting_user'
  | 'completed'
  | 'failed'

const tone: Record<JellyStatus, string> = {
  runnable: 'text-accent-ink',
  running: 'text-accent-ink',
  awaiting_approval: 'text-attention',
  awaiting_children: 'text-waiting',
  sleeping: 'text-muted',
  awaiting_user: 'text-muted',
  completed: 'text-success',
  failed: 'text-danger',
}

const motion: Record<JellyStatus, string> = {
  runnable: 'jelly-run',
  running: 'jelly-run',
  awaiting_approval: 'jelly-ask',
  awaiting_children: 'jelly-wait',
  sleeping: 'jelly-sleep',
  awaiting_user: '',
  completed: '',
  failed: '',
}

// JellyGlyph is a session's jellyfish, coloured and animated by its status. It
// is decorative: pair it with a text label so colour is never the only cue.
// size is the drawing's width in px; the tinted disc around it is 1.5 times that.
export function JellyGlyph({ status, size = 20 }: { status: JellyStatus; size?: number }) {
  return (
    <span
      aria-hidden="true"
      className={`inline-flex shrink-0 items-center justify-center rounded-full bg-current/12 ${tone[status]} ${motion[status]}`}
      style={{ width: size * 1.5, height: size * 1.5 }}
    >
      <svg
        width={size}
        height={size}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
      >
        <path
          className="fill-accent/30"
          d="M4 12a8 8 0 0 1 16 0c-1.3 1-2.7 1-4 0-1.3 1-2.7 1-4 0-1.3 1-2.7 1-4 0-1.3 1-2.7 1-4 0z"
        />
        <path d="M8 13.5c-1 2 1 3.5 0 6M12 13.5c-1 2.5 1 4 0 7M16 13.5c-1 2 1 3.5 0 6" />
      </svg>
    </span>
  )
}
