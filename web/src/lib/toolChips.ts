import type { UIEvent } from './api'

export type ToolChip = {
  seq: number
  name: string
  state: 'running' | 'done' | 'failed' | 'interrupted'
  // error is the error text of a failed or interrupted call.
  error?: string
}

type Payload = {
  tool_call_id: string
  tool?: string
  is_error?: boolean
  note?: string
  result?: { parts: { tr?: { parts: { text?: string }[] } }[] }
}

// toolChips is one chip per tool call in the stream, in the order the calls
// were requested.
export function toolChips(events: UIEvent[]): ToolChip[] {
  const chips = new Map<string, ToolChip>()
  for (const e of events) {
    const p = e.payload as Payload
    switch (e.type) {
      case 'tool.call.requested':
        chips.set(p.tool_call_id, { seq: e.seq, name: p.tool ?? '', state: 'running' })
        break
      case 'tool.call.completed': {
        const chip = chips.get(p.tool_call_id)
        if (!chip) break
        chip.state = p.is_error ? 'failed' : 'done'
        if (p.is_error) chip.error = resultText(p)
        break
      }
      case 'tool.call.interrupted': {
        const chip = chips.get(p.tool_call_id)
        if (!chip) break
        chip.state = 'interrupted'
        chip.error = p.note
        break
      }
    }
  }
  return [...chips.values()]
}

function resultText(p: Payload): string {
  const parts = p.result?.parts[0]?.tr?.parts ?? []
  return parts.map((x) => x.text ?? '').join('')
}
