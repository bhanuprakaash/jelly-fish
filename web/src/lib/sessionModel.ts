import type { UIEvent } from './api'

// sessionModel is the model the session's next turn runs on: the latest
// session.config_changed model, else the one session.created set.
export function sessionModel(events: UIEvent[]): string | undefined {
  let model: string | undefined
  for (const e of events) {
    if (e.type === 'session.created') {
      model = (e.payload as { agent?: { model?: string } }).agent?.model
    } else if (e.type === 'session.config_changed') {
      model = (e.payload as { model?: string }).model ?? model
    }
  }
  return model
}
