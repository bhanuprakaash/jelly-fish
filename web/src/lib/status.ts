// JellyStatus is a session status the glyph can show.
export type JellyStatus =
  | 'runnable'
  | 'running'
  | 'awaiting_approval'
  | 'sleeping'
  | 'awaiting_user'
  | 'completed'
  | 'failed'

// statusLabel is the words for each status, the cue beside the glyph's colour.
export const statusLabel: Record<JellyStatus, string> = {
  runnable: 'Working…',
  running: 'Working…',
  awaiting_approval: 'Needs approval',
  sleeping: 'Sleeping',
  awaiting_user: 'Your turn',
  completed: 'Done',
  failed: 'Failed',
}

// statusTone is the text colour for each status; the glyph and its label share it.
export const statusTone: Record<JellyStatus, string> = {
  runnable: 'text-accent-ink',
  running: 'text-accent-ink',
  awaiting_approval: 'text-attention',
  sleeping: 'text-muted',
  awaiting_user: 'text-muted',
  completed: 'text-success',
  failed: 'text-danger',
}
