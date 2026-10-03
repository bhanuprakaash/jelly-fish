import type { UIEvent } from './api'

// renamedTitle is the Title the session's latest session.renamed set, or
// undefined if the stream has none.
export function renamedTitle(events: UIEvent[]): string | undefined {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].type === 'session.renamed') return (events[i].payload as { title: string }).title
  }
  return undefined
}
