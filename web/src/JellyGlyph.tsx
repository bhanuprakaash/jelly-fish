import { statusTone, type JellyStatus } from './lib/status'

const motion: Record<JellyStatus, string> = {
  runnable: 'jelly-run',
  running: 'jelly-run',
  awaiting_approval: 'jelly-ask',
  sleeping: 'jelly-sleep',
  awaiting_user: '',
  completed: '',
  failed: '',
}

// JellyGlyph is a session's jellyfish, coloured and animated by its status. It
// is decorative: pair it with a text label so colour is never the only cue.
// size is the drawing's width in px; the tinted disc around it is 1.5 times
// size. A className sizes the disc itself instead, and its descendant svg
// variants (`[&>svg]:size-5`) size the drawing.
export function JellyGlyph({ status, size = 20, className }: { status: JellyStatus; size?: number; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={`inline-flex shrink-0 items-center justify-center rounded-full bg-current/12 ${statusTone[status]} ${motion[status]} ${className ?? ''}`}
      style={className ? undefined : { width: size * 1.5, height: size * 1.5 }}
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
